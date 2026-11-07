package filter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

var (
	ErrReadOnly = errors.New("masking control is read-only")
	ErrConflict = errors.New("masking state changed; reload before applying")
	ErrStorage  = errors.New("masking state unavailable")
)

type ControlState struct {
	Enabled  bool   `json:"enabled"`
	Revision string `json:"revision"`
	Writable bool   `json:"writable"`
}

// Control is a local administrator's optional masking choice. It never changes
// GovernancePolicy requirements. Request reads are lock-free.
type Control struct {
	mu     sync.Mutex
	path   string
	raw    []byte
	state  atomic.Pointer[ControlState]
	broken atomic.Bool
}

func OpenControl(path string, enabled bool) (*Control, error) {
	c := &Control{path: path}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, ErrStorage
		}
		if err == nil {
			var state struct {
				Enabled *bool `json:"enabled"`
			}
			d := json.NewDecoder(bytes.NewReader(raw))
			d.DisallowUnknownFields()
			if d.Decode(&state) != nil || state.Enabled == nil || d.Decode(new(any)) != io.EOF {
				return nil, ErrStorage
			}
			enabled, c.raw = *state.Enabled, raw
		}
	}
	c.publish(enabled)
	return c, nil
}

func (c *Control) publish(enabled bool) {
	// Include actual file bytes in the revision so an external edit is visible.
	raw := c.raw
	if raw == nil {
		raw, _ = json.Marshal(struct{ Enabled bool }{enabled})
	}
	sum := sha256.Sum256(raw)
	c.state.Store(&ControlState{enabled, "sha256:" + hex.EncodeToString(sum[:]), c.path != ""})
}

func (c *Control) Snapshot() ControlState {
	state := *c.state.Load()
	if c.broken.Load() {
		state.Enabled, state.Writable = true, false
	}
	return state
}
func (c *Control) enabled() bool {
	// An ambiguous persistence failure cannot turn masking off.
	return c.broken.Load() || c.state.Load().Enabled
}

func (c *Control) Set(enabled bool, expected string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == "" {
		return ErrReadOnly
	}
	if c.broken.Load() {
		return ErrStorage
	}
	if expected == "" || expected != c.state.Load().Revision {
		return ErrConflict
	}
	current, err := os.ReadFile(c.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrStorage
	}
	if !bytes.Equal(current, c.raw) {
		return ErrConflict
	}
	raw, _ := json.Marshal(struct {
		Enabled bool `json:"enabled"`
	}{enabled})
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".masking-*.tmp")
	if err != nil {
		return ErrStorage
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil || closeErr != nil {
		return ErrStorage
	}
	if os.Rename(tmp.Name(), c.path) != nil {
		return ErrStorage
	}
	dir, err := os.Open(filepath.Dir(c.path))
	if err != nil {
		c.broken.Store(true)
		return ErrStorage
	}
	err = dir.Sync()
	closeErr = dir.Close()
	if err != nil || closeErr != nil {
		c.broken.Store(true)
		return ErrStorage
	}
	c.raw = raw
	c.publish(enabled)
	return nil
}
