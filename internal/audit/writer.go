package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/inferplane/inferplane/internal/metrics"
)

const (
	genesisHash   = "sha256:genesis"
	queueCapacity = 1024
	blockedBuffer = 1
	blockedWAL    = 2
	blockedRecord = 3
)

// WriterOption configures boot-time audit durability behavior.
type WriterOption func(*writerOptions)

type writerOptions struct{ failureMode string }

// WithFailureMode selects the supported buffer_then_block admission contract.
// Empty uses that default; unsupported modes refuse construction.
func WithFailureMode(mode string) WriterOption {
	return func(o *writerOptions) { o.failureMode = mode }
}

// chainHead is the immutable {head hash, record count} snapshot published as ONE
// atomic value (ADR-012) so HeadHash() can never return a torn hash/count mix.
type chainHead struct {
	hash  string
	count int64
}

// Writer is the SINGLE writer goroutine for the audit chain. Handlers enqueue
// records via Append (non-blocking); the goroutine assigns prev_hash, persists
// to the WAL, and flushes to sinks — strictly serialized so concurrent
// started/completed records can't race on prev_hash (r4 implementation note).
type Writer struct {
	instance string
	queue    chan Record
	done     chan struct{}
	wal      *WAL
	sinks    []Sink
	prevHash string
	pending  int
	count    int64                     // records written (loop-local; published via head)
	head     atomic.Pointer[chainHead] // race-safe chain-head snapshot for anchoring (ADR-012)
	metrics  *metrics.Metrics          // nil-safe: no-op when nil
	blocked  atomic.Uint32             // once set, admission stays closed for this process
}

// Ready reports whether new inference may be admitted. Reasons are fixed,
// content-free operational messages. Existing requests may still append their
// settlement records after the latch closes.
func (w *Writer) Ready() (bool, string) {
	if w == nil {
		return true, ""
	}
	switch w.blocked.Load() {
	case blockedBuffer:
		return false, "audit buffer full; preserve and repair retained WAL before restart"
	case blockedWAL:
		return false, "audit WAL unavailable; repair storage before restart"
	case blockedRecord:
		return false, "audit record unavailable; restart required"
	default:
		return true, ""
	}
}

// HeadHash returns a race-safe snapshot of the current chain head (the last
// durably-written record's hash) and the number of records written. Before the
// first record it is (genesis, 0). Used by the audit anchor worker (ADR-012).
func (w *Writer) HeadHash() (string, int64) {
	h := w.head.Load()
	if h == nil {
		return genesisHash, 0
	}
	return h.hash, h.count
}

// Instance returns this writer's chain-segment identity — the value every
// record it writes carries in the `instance` field (see Verify/HeadAtCount),
// and what an Anchorer's AnchorPoint.Instance is stamped with (anchorOnce).
// Immutable for the writer's lifetime; safe to read from any goroutine.
func (w *Writer) Instance() string { return w.instance }

// SetMetrics attaches the Prometheus metrics sink. On a required-sink write
// failure the writer bumps audit_write_failures_total; the WAL buffer
// utilization gauge tracks records persisted-but-not-yet-delivered. Must be
// called before the writer is busy (e.g. right after NewWriter). nil disables.
func (w *Writer) SetMetrics(m *metrics.Metrics) {
	w.metrics = m
	m.SetAuditBufferUtilization(float64(w.pending) / float64(cap(w.queue)))
}

func NewWriter(instance, walPath string, sinks []Sink, opts ...WriterOption) (*Writer, error) {
	var o writerOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.failureMode != "" && o.failureMode != "buffer_then_block" {
		return nil, errors.New("audit: unsupported failure mode; use buffer_then_block")
	}
	wal, err := OpenWAL(walPath)
	if err != nil {
		return nil, err
	}
	w := &Writer{
		instance: instance,
		queue:    make(chan Record, queueCapacity),
		done:     make(chan struct{}),
		wal:      wal,
		sinks:    sinks,
		prevHash: genesisHash,
	}
	// Do not replay into arbitrary sinks: a previous partial fan-out may
	// already have delivered some records. Counting retained bytes preserves
	// admission pressure without duplicate delivery or evidence truncation.
	w.pending, err = retainedRecords(wal)
	if err != nil {
		_ = wal.Close()
		return nil, err
	}
	if w.pending >= cap(w.queue) {
		w.blocked.Store(blockedBuffer)
	}
	go w.loop()
	return w, nil
}

// Append enqueues a record. Non-blocking unless the queue is full (back-pressure).
func (w *Writer) Append(rec Record) { w.queue <- rec }

func (w *Writer) loop() {
	defer close(w.done)
	for rec := range w.queue {
		rec.Instance = w.instance
		rec.PrevHash = w.prevHash
		canon, err := rec.Canonical()
		if err != nil {
			writeFailuresTotal.Add(1)
			w.blocked.CompareAndSwap(0, blockedRecord)
			continue
		}
		// chain advances on the canonical bytes actually emitted
		sum := sha256.Sum256(canon)
		w.prevHash = "sha256:" + hex.EncodeToString(sum[:])

		// durability first: WAL, then sinks. A required-sink failure leaves the
		// record in the WAL for replay (buffer_then_block; §5.4).
		walErr := w.wal.Append(canon)
		if walErr == nil {
			w.pending++ // retained records, including earlier undelivered records
		} else {
			w.walFailure()
		}
		// Publish the chain head ONLY after the record is DURABLE (ADR-012 P4
		// CRITICAL): on a WAL-append failure the record may be lost on crash, so an
		// anchor must NOT witness it — advance the published head/count only when
		// the WAL write succeeded. (The chain link `prevHash` already advanced, so
		// chaining continues; the witnessed head simply lags an undurable record.)
		if walErr == nil {
			w.count++
			w.head.Store(&chainHead{hash: w.prevHash, count: w.count})
		}
		flushedAll := true
		for _, s := range w.sinks {
			if err := s.Write(canon); err != nil {
				if s.Required() {
					writeFailuresTotal.Add(1)
					bufferedRecords.Add(1)
					flushedAll = false
					w.metrics.IncAuditFailure(s.Name())
				}
			}
		}
		// Truncate ONLY when this record delivered to all required sinks AND it
		// is the sole record in the WAL — i.e. no earlier record is still
		// buffered-undelivered. A later success must never drop an earlier
		// buffered record (buffer_then_block durability). Once any record is
		// buffered, the WAL retains everything for operator-managed recovery.
		// Restart alone must not erase that backlog or clear full pressure.
		if walErr == nil && flushedAll && w.pending == 1 {
			if err := w.wal.Truncate(); err != nil {
				w.walFailure()
			} else {
				w.pending = 0
			}
		}
		if w.pending >= cap(w.queue) {
			w.blocked.CompareAndSwap(0, blockedBuffer)
		}
		// Buffer utilization: records persisted to the WAL but not yet delivered
		// to all required sinks, over the in-flight queue capacity (observability
		// approximation of buffer_then_block pressure, §5.4).
		w.metrics.SetAuditBufferUtilization(float64(w.pending) / float64(cap(w.queue)))
	}
}

func (w *Writer) walFailure() {
	writeFailuresTotal.Add(1)
	w.metrics.IncAuditFailure("wal")
	w.blocked.CompareAndSwap(0, blockedWAL)
}

// retainedRecords validates complete JSONL framing and internal chain links.
// The first retained record may link to already delivered history, so its
// predecessor cannot be reconstructed from this partial WAL alone.
func retainedRecords(wal *WAL) (int, error) {
	stat, err := wal.f.Stat()
	if err != nil {
		return 0, fmt.Errorf("audit: inspect retained WAL: %w", err)
	}
	if stat.Size() == 0 {
		return 0, nil
	}
	invalid := errors.New("audit: retained WAL is malformed or truncated; preserve and repair it before restart")
	var last [1]byte
	if _, err := wal.f.ReadAt(last[:], stat.Size()-1); err != nil {
		return 0, fmt.Errorf("audit: inspect retained WAL framing: %w", err)
	}
	if last[0] != '\n' {
		return 0, invalid
	}
	count := 0
	var instance, expected string
	if err := wal.Replay(func(line []byte) error {
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil || rec.Event == "" || rec.Instance == "" || rec.PrevHash == "" {
			return invalid
		}
		if rec.PrevHash != genesisHash {
			digest, ok := strings.CutPrefix(rec.PrevHash, "sha256:")
			if !ok || len(digest) != sha256.Size*2 {
				return invalid
			}
			if _, err := hex.DecodeString(digest); err != nil {
				return invalid
			}
		}
		if count > 0 {
			if rec.Instance != instance {
				expected = genesisHash
			}
			if rec.PrevHash != expected {
				return invalid
			}
		}
		sum := sha256.Sum256(line)
		instance = rec.Instance
		expected = "sha256:" + hex.EncodeToString(sum[:])
		count++
		return nil
	}); err != nil {
		// Scanner and decoder details must not disclose retained record values.
		return 0, invalid
	}
	return count, nil
}

// Close drains the queue and closes the WAL.
func (w *Writer) Close() error {
	close(w.queue)
	<-w.done
	return w.wal.Close()
}
