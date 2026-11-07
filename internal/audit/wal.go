package audit

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// WAL is a disk-backed, append-only buffer for audit records that a required
// sink failed to accept. Synced records remain across restart; the writer
// validates/counts them on reopen without automatically replaying them into
// sinks. Recovery must account for any previous partial delivery before
// clearing retained evidence. Newline-delimited, same framing as the sinks.
type WAL struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

func OpenWAL(path string) (*WAL, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	// fsync(file) does not make a newly created directory entry durable.
	// Do this even for an existing file: its creation may have been interrupted
	// by a crash before the directory was synced.
	if err := syncWALDirectory(path); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &WAL{f: f, path: path}, nil
}

func syncWALDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("audit: open journal directory: %w", err)
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return fmt.Errorf("audit: sync journal directory: %w", err)
	}
	if err := dir.Close(); err != nil {
		return fmt.Errorf("audit: close journal directory: %w", err)
	}
	return nil
}

func (w *WAL) Append(rec []byte) error {
	return w.AppendBatch([][]byte{rec})
}

// AppendBatch makes a whole ordered group durable with one fsync.
func (w *WAL) AppendBatch(records [][]byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, rec := range records {
		if _, err := w.f.Write(rec); err != nil {
			return err
		}
		if _, err := w.f.Write([]byte{'\n'}); err != nil {
			return err
		}
	}
	return w.f.Sync()
}

func (w *WAL) checkIdentity() error {
	opened, err := w.f.Stat()
	if err != nil {
		return fmt.Errorf("audit: inspect open journal: %w", err)
	}
	current, err := os.Lstat(w.path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return errors.New("audit: active journal changed")
	}
	return nil
}

// Replay invokes fn for each buffered record in order.
func (w *WAL) Replay(fn func(rec []byte) error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.f.Seek(0, 0); err != nil {
		return err
	}
	// Append/Record.Canonical have no per-record 1-MiB limit. A Scanner
	// ceiling would refuse valid writer output after restart. ReadBytes owns
	// each line's storage and preserves exact bytes without a second copy.
	reader := bufio.NewReader(w.f)
	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
		}
		if len(line) > 0 {
			if err := fn(line); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
	}
}

// Truncate empties the WAL after its records have been durably flushed to sinks.
func (w *WAL) Truncate() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.f.Truncate(0); err != nil {
		return err
	}
	if _, err := w.f.Seek(0, 0); err != nil {
		return err
	}
	return w.f.Sync()
}

func (w *WAL) Close() error { return w.f.Close() }
