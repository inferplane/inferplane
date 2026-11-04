package pgstore

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/pkg/ulid"
)

const (
	defaultPollInterval    = 5 * time.Second
	defaultLeaseTTL        = 15 * time.Second
	defaultMaxLinesPerTick = 5000
)

// AggregatorConfig configures the Mode B audit tailing loop.
type AggregatorConfig struct {
	AggregatedAuditDir string
	PollInterval       time.Duration
	LeaseTTL           time.Duration
	MaxLinesPerTick    int
}

// Aggregator tails the operator-provided aggregate audit directory while this
// process holds the fenced Mode B lease.
type Aggregator struct {
	store      *Store
	cfg        AggregatorConfig
	instanceID string
}

// NewAggregator builds the tailing/fencing loop bound to store s.
func NewAggregator(s *Store, cfg AggregatorConfig) *Aggregator {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = defaultLeaseTTL
	}
	if cfg.MaxLinesPerTick <= 0 {
		cfg.MaxLinesPerTick = defaultMaxLinesPerTick
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	id := host + "-" + ulid.New()
	s.instanceID = id
	return &Aggregator{store: s, cfg: cfg, instanceID: id}
}

// Run polls until ctx is cancelled. Lost leadership is a steady-state no-op.
// A tick error (a transient filesystem hiccup, a segment rotation race, a
// momentary Postgres blip) is logged and retried next poll — never
// propagated to kill the loop (same best-effort retry posture as the
// gateway's anchorWorker): capabilities keeps advertising Mode B for the
// life of the process, so a permanently-dead aggregator behind it would
// silently go stale forever with no way to recover short of a restart.
func (a *Aggregator) Run(ctx context.Context) error {
	for {
		if err := a.tick(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "inferplane: analytics mode_b tick failed (will retry):", err)
		}
		timer := time.NewTimer(a.cfg.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (a *Aggregator) tick(ctx context.Context) error {
	epoch, ok, err := tryAcquireLease(ctx, a.store.db, a.instanceID, a.cfg.LeaseTTL)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	entries, err := os.ReadDir(a.cfg.AggregatedAuditDir)
	if err != nil {
		return fmt.Errorf("read aggregate audit dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	remaining := a.cfg.MaxLinesPerTick
	items := []ingestItem{}
	checkpoints := map[string]int64{}
	for _, name := range names {
		if remaining <= 0 {
			break
		}
		offset, err := a.checkpoint(ctx, name)
		if err != nil {
			return err
		}
		path := filepath.Join(a.cfg.AggregatedAuditDir, name)
		read, newOffset, lines, err := readCompleteLines(path, offset, remaining)
		if err != nil {
			return err
		}
		if newOffset > offset {
			checkpoints[name] = newOffset
			items = append(items, read...)
			remaining -= lines
		}
	}
	if len(checkpoints) == 0 {
		return nil
	}
	if err := a.store.ingestItems(ctx, a.instanceID, epoch, items, checkpoints); err != nil {
		if errors.Is(err, errFenced) {
			return nil
		}
		return err
	}
	return nil
}

func (a *Aggregator) checkpoint(ctx context.Context, segment string) (int64, error) {
	var offset int64
	err := a.store.db.QueryRow(ctx, `SELECT byte_offset FROM checkpoints WHERE segment=$1`, segment).Scan(&offset)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read analytics checkpoint %q: %w", segment, err)
	}
	return offset, nil
}

// readCompleteLines returns each decodable complete line with its exact bytes
// (newline trimmed), so the console row keeps the chain line verbatim.
func readCompleteLines(path string, start int64, maxLines int) ([]ingestItem, int64, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, start, 0, fmt.Errorf("open aggregate audit segment %q: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, start, 0, fmt.Errorf("seek aggregate audit segment %q: %w", path, err)
	}

	br := bufio.NewReader(f)
	offset := start
	lines := 0
	items := []ingestItem{}
	for lines < maxLines {
		line, readErr := br.ReadString('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			offset += int64(len(line))
			lines++
			var rec audit.Record
			if json.Unmarshal([]byte(line), &rec) == nil {
				items = append(items, ingestItem{rec: rec, line: []byte(strings.TrimRight(line, "\r\n"))})
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, offset, lines, fmt.Errorf("read aggregate audit segment %q: %w", path, readErr)
		}
	}
	return items, offset, lines, nil
}
