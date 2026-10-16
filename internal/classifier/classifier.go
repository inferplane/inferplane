// Package classifier is the leaf seam for opt-in PII classifiers. A classifier
// reads request text and answers Clear, Flagged or Abstain; the router turns an
// Enforce answer into a sensitiveData restriction. A classifier never edits a
// request body. Concrete types live in plugins/<type> and register from init(),
// the same isolation model as providers. This package imports no internal
// package.
package classifier

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Verdict is a classifier's answer for one request.
type Verdict int

const (
	Clear Verdict = iota
	Flagged
	Abstain
)

// Mode decides whether an entry's answer can restrict routing.
type Mode string

const (
	Shadow  Mode = "Shadow"
	Enforce Mode = "Enforce"
)

// Input holds the units not served from the verdict cache. The texts are
// borrowed for the call and must not be retained.
type Input struct {
	Texts []string
}

// Output is a classifier's answer. Clear[i] reports that Texts[i] was read
// completely and judged clear; only those units may be cached. A nil Clear
// means none. Windows counts the windows sent, for metrics only.
type Output struct {
	Verdict Verdict
	Clear   []bool
	Windows int
}

// Classifier is one configured backend. Units splits request segments into the
// deterministic units the classifier reads and the cache stores; Classify
// receives only units that Units produced. Units must carry the text of every
// non-empty segment; a unit set that dropped text would let Run report Clear
// for content no classifier read.
type Classifier interface {
	Units(segments []string) []string
	Classify(ctx context.Context, in Input) (Output, error)
}

// Config is one validated entry. BearerToken is the resolved api_key_ref and
// must never be logged or wrapped into an error.
type Config struct {
	Name, Type, Revision, Endpoint string
	BearerToken                    string
	Threshold                      float64
	Options                        map[string]json.RawMessage
}

// Error classes are the only failure detail a plugin may return. They carry
// no request text, response body, endpoint or credential.
var (
	ErrTimeout   = errors.New("classifier: timeout")
	ErrTransport = errors.New("classifier: transport")
	ErrStatus    = errors.New("classifier: status")
	ErrDecode    = errors.New("classifier: decode")
)

// Factory builds a classifier and validates its type-specific options.
type Factory func(Config) (Classifier, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register is called from a plugin's init(). An empty type, a nil factory or a
// duplicate type panics.
func Register(typ string, f Factory) {
	if typ == "" || f == nil {
		panic("classifier: Register needs a type and a factory")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[typ]; dup {
		panic("classifier: duplicate type " + typ)
	}
	registry[typ] = f
}

// Types lists the registered types in sorted order.
func Types() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for typ := range registry {
		out = append(out, typ)
	}
	slices.Sort(out)
	return out
}

// New builds a classifier through its registered factory.
func New(cfg Config) (Classifier, error) {
	registryMu.RLock()
	f, ok := registry[cfg.Type]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("classifier %q: unknown type %q (registered: %s)", cfg.Name, cfg.Type, strings.Join(Types(), ", "))
	}
	return f(cfg)
}

// SettingsDigest covers every setting that changes a classifier's answer. It
// is part of every cache key, so a changed setting never reuses a verdict. The
// bearer token is excluded: rotating it does not change answers.
func SettingsDigest(cfg Config) [32]byte {
	h := sha256.New()
	field := func(s string) { fmt.Fprintf(h, "%d:%s;", len(s), s) }
	field(cfg.Type)
	field(cfg.Endpoint)
	field(cfg.Revision)
	field(strconv.FormatFloat(cfg.Threshold, 'g', -1, 64))
	keys := make([]string, 0, len(cfg.Options))
	for k := range cfg.Options {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		var compact bytes.Buffer
		if err := json.Compact(&compact, cfg.Options[k]); err != nil {
			compact.Reset()
			compact.Write(cfg.Options[k])
		}
		field(k)
		field(compact.String())
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
