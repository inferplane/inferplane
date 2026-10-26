package audit

import (
	"bufio"
	"io"
	"os"
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
	return &WAL{f: f, path: path}, nil
}

func (w *WAL) Append(rec []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.f.Write(rec); err != nil {
		return err
	}
	if _, err := w.f.Write([]byte{'\n'}); err != nil {
		return err
	}
	return w.f.Sync()
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
	_, err := w.f.Seek(0, 0)
	return err
}

func (w *WAL) Close() error { return w.f.Close() }
