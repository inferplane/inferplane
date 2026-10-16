# systemone PII Classifier and Korean Sample Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship an opt-in `systemone` PII classifier that restricts routing on the full retransmitted request content, with a team-scoped verdict cache, plus a Korean sample: published fine-tuned Laya weights, a CPU sidecar image, Helm values, an example config and policy, a model card and the training recipe.

**Architecture:**
- **Core.** A stdlib-only leaf package, `internal/classifier`, holds the registry, the per-generation `Set`, the HMAC verdict cache and the parallel runner.
- **Input.** `internal/sensitivity.Segments` hands it exactly the text the regex inspector reads.
- **Plugin.** `plugins/systemone` turns segments into units and windows, and talks to a `/v1/systemone` server.
- **Routing.** `router.RouteRequest` runs the Set's entries for the principal's team after regex inspection. Enforce results map onto the existing `sensitiveData` actions. Audit and metrics record only bounded enums.

**Tech Stack:**
- Go 1.26.9 standard library only: `net/http`, `crypto/hmac`, `container/list`.
- Prometheus `client_golang`, already a dependency.
- Helm 3.
- Docker buildx with a Python 3.12 slim base for `laya-serve` 0.4.6.
- GitHub Actions `workflow_dispatch`.

**Spec:** `docs/superpowers/specs/2026-10-16-systemone-korean-pii-sample-design.md` (approved 2026-10-16), amended by the "Spec amendments" section below in the same commit as this plan.

## Global Constraints

- Go 1.26.9; `CGO_ENABLED=0`; no new third-party Go dependency (stdlib plus the existing Prometheus client).
- Absent `pii_classification` means today's behavior and cost, byte for byte.
- `data_boundary` is required and must be `internal`. `external` is rejected as unsupported in this version.
- A classifier never edits a request body, never retains request text after the call, and returns only the bounded error classes `timeout`, `transport`, `status` and `decode`.
- Never return Clear for a request whose remaining content was not read (PR-08).
- Only Enforce entries restrict routing. Shadow entries are recorded and never change routing.
- count_tokens requests make no classifier call and stay HTTP 200.
- Audit: `audit.RoutingRef.Classifiers` is append-only and `omitempty`. Records without classifiers keep their exact bytes. The field never carries text, windows, probabilities, digests, endpoints or tokens.
- Metric labels are limited to config names, revisions and bounded enums. They never include key_id.
- Defaults:
  - `timeout_ms` 500; `max_input_bytes` 2,097,152; `cache.max_entries` 100,000; `cache.ttl_seconds` 3,600.
  - systemone: `instructions` = "Does this text contain personal information about a specific person, or a secret credential?", `batch` true, `concurrency` 4, `window_runes` 1000, `window_overlap_runes` 150 (when unset, capped at `window_runes`/2), `max_windows` 64, `threshold` 0.5, no `abstain_band`.
- Bounds:
  - `timeout_ms` 1–30000; `max_input_bytes` 1 KiB–16 MiB; `cache.max_entries` 0–1,000,000; `cache.ttl_seconds` 60–86,400.
  - At most 4 classifiers. Names `[a-z0-9-]{1,32}`; revisions `[a-z0-9._-]{1,32}`.
  - systemone `concurrency` 1–16, `window_runes` 200–4000, `window_overlap_runes` 0 to `window_runes`/2, `max_windows` 1–64.
  - `abstain_band` needs 0 < low < threshold < high < 1. `instructions` and `criteria` strings are 1–512 characters; `model` matches `[a-z0-9._-]{1,64}`.
- Security (user CLAUDE.md):
  - The sidecar binds `127.0.0.1`, with no Service port.
  - The GPU profile is a ClusterIP Service plus a NetworkPolicy that admits only gateway pods.
  - No `0.0.0.0/0` ingress, no LoadBalancer, no public IP.
- Every commit is DCO signed off (`git commit -s`) and ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Publication to Hugging Face or GHCR happens only in Task 15, after its gates and an explicit user confirmation for each push. Pushing the branch and opening the PR also need explicit user confirmation.

## Review Focus

1. **A tool result longer than `max_windows` windows**, such as a large file read that stays in the history. Expected: the requests abstain only until every unit has been read across a few turns, then become Clear with zero calls. The session is never permanently restricted. Pinned in Task 2 (`TestRunWarmsAcrossRequestsUntilEverythingIsRead`) and Task 5 (`TestMaxWindowsReadsPrefixAndAbstains`).
2. **Real Claude-Code-shaped bodies**: a system prompt, dozens of tool schemas, JSON keys and encoded arguments. Expected: accuracy close to the document-level evaluation, and no blanket false positives from the framing. Pinned in Task 3 (`TestSegmentsCoverEveryProtectedPosition`) and Task 14 (the live wrapped-body evaluation).
3. **A sidecar that is not ready, restarting or overloaded**: connection refused, or a 503 from `LAYA_MAX_CONCURRENT`. Expected: a bounded `error` result, no retries, Shadow routing unchanged, Enforce fails closed. Pinned in Task 5 (`TestErrorsAreBoundedClasses`) and Task 8 (`TestClassifierErrorAppliesOnUninspectable`).
4. **A reload or revision change while requests are in flight.** Expected: each request uses the Set and cache of the generation it captured, and metric series of a replaced revision disappear. Pinned in Task 8 (`TestClassifierUsesCapturedGeneration`) and Task 7 (`TestRetainPIIClassifiersDeletesReplacedSeries`).
5. **Principals outside the classifier scope**: no team, or another team, or a team with classifiers but no privacy rule. Expected: no call and no added latency. Pinned in Task 8 (`TestClassifierNotCalledOutsideScope`).

## Spec amendments (applied to the spec in the same commit as this plan)

These amendments are refinements found while mapping the spec to the code. None of them widens what a classifier may do.

1. **Units, not whole segments, are the cache grain.** `classifier.Classifier` gains `Units(segments []string) []string`.
   - `systemone` returns each short segment unchanged. It splits a segment longer than `window_runes` into deterministic overlapping chunks.
   - The cache stores clear units, so a long tool result is read across a few requests instead of abstaining forever. Review Focus 1 covers this.
   - The "Known limit" in spec §3 applies to units in the same way.
2. **Secret reference format.** The codebase has no `secret:` scheme. The token is `api_key_ref: {"env": NAME}` or `{"file": "/abs"}` (`config.SecretRef`), and an inline `api_key` is rejected. This replaces the spec's "`env:`, `file:` and `secret:` refs" wording.
3. **Strict block decoding.** `config.LoadRaw` ignores unknown keys elsewhere. The `pii_classification` block is decoded with `DisallowUnknownFields`, so a misspelled `mode` or `teams` fails the load instead of silently widening scope.
4. **Sample file locations follow the repository's example tests.**
   - The config is `examples/config.pii-classifier-ko.json`, which `internal/config/examples_test.go` loads automatically.
   - The Helm values file is `examples/helm.pii-classifier-ko.yaml`, following the `examples/helm.shared-governance.yaml` precedent.
   - The policy is `examples/pii-classifier-ko/governance.yaml`.
   - Everything else stays under `examples/pii-classifier-ko/`.
5. **Egress hygiene.** The systemone HTTP client sets `Proxy: nil` and does not follow redirects. Request text then never travels through an environment proxy, and a redirect never moves it to another host.
6. **Image weights stage.** The Dockerfile takes the weights either from the Hugging Face repository at a pinned commit (`WEIGHTS=hf`, the published build) or from the build context (`WEIGHTS=local`, used for acceptance before publication). Both paths verify `SHA256SUMS`.

## File structure

| Path | Responsibility |
|---|---|
| `internal/classifier/classifier.go` | Verdict, Mode, Input/Output, Classifier interface, Config, error classes, factory registry, `SettingsDigest` |
| `internal/classifier/set.go` | `Entry`, `Set`, `NewSet`, `For` |
| `internal/classifier/cache.go` | HMAC-keyed, TTL-bounded LRU of clear units |
| `internal/classifier/run.go` | `Request`, `Result`, `Outcome`, `Set.Run`, `Combine`, `Failed` |
| `internal/sensitivity/segments.go` | `Segments`: the inspector walker's visit sequence |
| `plugins/systemone/window.go` | `splitRunes`, `buildWindows` |
| `plugins/systemone/systemone.go` | options, factory, `Units`, `Classify`, verdict rule |
| `plugins/systemone/client.go` | batch and single wire, bounded errors |
| `internal/config/piiclassification.go` | `PIIClassificationConfig`, strict decode, validation |
| `internal/live/classifiers.go` | build the `*classifier.Set` for a State; boot warning |
| `internal/router/request_routing.go` (modify) | run classifiers, map signals to rule actions, `model_pii` |
| `internal/router/classify.go` | `Router.classify`, `ClassifierRef` |
| `internal/audit/routing.go` (modify) | `ClassifierRef`, clone |
| `internal/server/requestpolicy/evidence.go` (modify) | project classifier refs into audit |
| `internal/metrics/classifier.go` | three collectors, observe and retain |
| `cmd/mayu/main.go`, `cmd/mayu/gateway.go` (modify) | blank import, metric retention after each swap |
| `charts/inferplane/templates/deployment.yaml`, `values.yaml` (modify) | opt-in Laya sidecar |
| `internal/config/chart_classifier_test.go` | chart render test |
| `examples/config.pii-classifier-ko.json`, `examples/helm.pii-classifier-ko.yaml`, `examples/pii-classifier-ko/**` | the sample |
| `cmd/mayu/pii_classifier_example_test.go` | example config, policy and Set build |
| `cmd/mayu/pii_classifier_live_test.go` | `//go:build liveclassifier` acceptance against a real server |
| `.github/workflows/laya-ko-pii-image.yml` | manual multi-arch image publication |
| docs, `CHANGELOG.md`, `internal/CLAUDE.md`, `plugins/CLAUDE.md`, root `CLAUDE.md` (modify) | documentation |

---

### Task 1: Classifier seam, registry and verdict cache

**Files:**
- Create: `internal/classifier/classifier.go`, `internal/classifier/set.go`, `internal/classifier/cache.go`
- Test: `internal/classifier/classifier_test.go`, `internal/classifier/cache_test.go`

**Interfaces:**
- Consumes: nothing (leaf; stdlib only).
- Produces:
  - `type Verdict int` with `Clear`, `Flagged`, `Abstain`; `type Mode string` with `Shadow`, `Enforce`.
  - `type Input struct{ Texts []string }`; `type Output struct{ Verdict Verdict; Clear []bool; Windows int }`.
  - `type Classifier interface{ Units([]string) []string; Classify(context.Context, Input) (Output, error) }`.
  - `type Config struct{ Name, Type, Revision, Endpoint, BearerToken string; Threshold float64; Options map[string]json.RawMessage }`.
  - `ErrTimeout`, `ErrTransport`, `ErrStatus`, `ErrDecode`.
  - `type Factory func(Config) (Classifier, error)`; `Register(string, Factory)`; `Types() []string`; `New(Config) (Classifier, error)`; `SettingsDigest(Config) [32]byte`.
  - `type Entry struct{ Name, Revision string; Mode Mode; Classifier Classifier; Global bool; Teams map[string]bool; Settings [32]byte }`.
  - `type Set struct{ Entries []Entry; Timeout time.Duration; MaxInputBytes int; cache *verdictCache }`.
  - `NewSet(entries []Entry, timeout time.Duration, maxInputBytes, cacheEntries int, cacheTTL time.Duration) (*Set, error)`; `(*Set).For(team string) []Entry`.
  - `type PolicyRef struct{ Name string; Generation int64; Rule string }`.
  - Unexported `newVerdictCache(max int, ttl time.Duration, now func() time.Time) (*verdictCache, error)` and methods `digest(team string, e *Entry, policies []PolicyRef, text string) [32]byte`, `clear([32]byte) bool`, `store([32]byte)`, `len() int`.

- [ ] **Step 1: Write the failing tests**

`internal/classifier/classifier_test.go`:

```go
package classifier

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type stubClassifier struct{}

func (stubClassifier) Units(s []string) []string                       { return s }
func (stubClassifier) Classify(context.Context, Input) (Output, error) { return Output{}, nil }

func stubFactory(Config) (Classifier, error) { return stubClassifier{}, nil }

// Registered once per process so the tests survive -count=N.
func init() {
	Register("classifier-test-dup", stubFactory)
	Register("classifier-test-known", stubFactory)
}

func TestRegisterRejectsDuplicateEmptyAndNil(t *testing.T) {
	for name, call := range map[string]func(){
		"duplicate":   func() { Register("classifier-test-dup", stubFactory) },
		"empty type":  func() { Register("", stubFactory) },
		"nil factory": func() { Register("classifier-test-nil", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected a panic")
				}
			}()
			call()
		})
	}
}

func TestNewUnknownTypeNamesRegisteredTypes(t *testing.T) {
	_, err := New(Config{Name: "x", Type: "nope"})
	if err == nil || !strings.Contains(err.Error(), `unknown type "nope"`) || !strings.Contains(err.Error(), "classifier-test-known") {
		t.Fatalf("err = %v", err)
	}
}

func TestSettingsDigestCoversAnswerSettings(t *testing.T) {
	base := Config{Type: "systemone", Endpoint: "http://127.0.0.1:8095", Revision: "r1", Threshold: 0.5,
		Options: map[string]json.RawMessage{"model": json.RawMessage(`"multilingual"`), "max_windows": json.RawMessage(`4`)}}
	want := SettingsDigest(base)
	same := base
	same.Options = map[string]json.RawMessage{"max_windows": json.RawMessage(` 4 `), "model": json.RawMessage(`"multilingual"`)}
	same.BearerToken = "rotated-bearer"
	if SettingsDigest(same) != want {
		t.Fatal("whitespace, option order or a rotated token changed the digest")
	}
	for name, edit := range map[string]func(*Config){
		"type":      func(c *Config) { c.Type = "other" },
		"endpoint":  func(c *Config) { c.Endpoint = "http://127.0.0.1:8096" },
		"revision":  func(c *Config) { c.Revision = "r2" },
		"threshold": func(c *Config) { c.Threshold = 0.6 },
		"option": func(c *Config) {
			c.Options = map[string]json.RawMessage{"model": json.RawMessage(`"english"`), "max_windows": json.RawMessage(`4`)}
		},
	} {
		c := base
		edit(&c)
		if SettingsDigest(c) == want {
			t.Errorf("a %s change kept the digest", name)
		}
	}
}

func TestForScopesEntries(t *testing.T) {
	var none *Set
	if none.For("kr") != nil {
		t.Fatal("a nil Set returned entries")
	}
	s, err := NewSet([]Entry{{Name: "global", Global: true}, {Name: "scoped", Teams: map[string]bool{"kr": true}}}, time.Second, 1<<20, 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for team, want := range map[string]string{"kr": "global,scoped", "other": "global", "": "global"} {
		if got := entryNames(s.For(team)); got != want {
			t.Errorf("For(%q) = %s, want %s", team, got, want)
		}
	}
}

func entryNames(entries []Entry) string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return strings.Join(out, ",")
}
```

`internal/classifier/cache_test.go`:

```go
package classifier

import (
	"testing"
	"time"
)

func TestCacheHitsUntilTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	c, err := newVerdictCache(4, time.Minute, func() time.Time { return now })
	if err != nil || c == nil {
		t.Fatal(c, err)
	}
	k := c.digest("kr", &Entry{Name: "laya"}, nil, "hello")
	if c.clear(k) {
		t.Fatal("hit on an empty cache")
	}
	c.store(k)
	if !c.clear(k) {
		t.Fatal("a stored key missed")
	}
	now = now.Add(time.Minute)
	if c.clear(k) || c.len() != 0 {
		t.Fatal("an expired key hit or stayed resident")
	}
}

func TestCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c, _ := newVerdictCache(2, time.Hour, time.Now)
	e := &Entry{Name: "laya"}
	a, b, d := c.digest("t", e, nil, "a"), c.digest("t", e, nil, "b"), c.digest("t", e, nil, "d")
	c.store(a)
	c.store(b)
	c.clear(a) // a is now the most recent
	c.store(d) // evicts b
	if !c.clear(a) || c.clear(b) || !c.clear(d) || c.len() != 2 {
		t.Fatal("LRU order not respected")
	}
}

func TestCacheKeysSeparateEveryScope(t *testing.T) {
	c, _ := newVerdictCache(10, time.Hour, time.Now)
	e := &Entry{Name: "laya", Settings: [32]byte{1}}
	pol := []PolicyRef{{Name: "p", Generation: 1, Rule: "r"}}
	base := c.digest("kr", e, pol, "text")
	renamed, retuned := *e, *e
	renamed.Name = "other"
	retuned.Settings = [32]byte{2}
	for name, k := range map[string][32]byte{
		"team":              c.digest("us", e, pol, "text"),
		"entry":             c.digest("kr", &renamed, pol, "text"),
		"settings":          c.digest("kr", &retuned, pol, "text"),
		"policy generation": c.digest("kr", e, []PolicyRef{{Name: "p", Generation: 2, Rule: "r"}}, "text"),
		"no policy":         c.digest("kr", e, nil, "text"),
		"text":              c.digest("kr", e, pol, "text2"),
		"field boundary":    c.digest("k", e, pol, "rtext"),
	} {
		if k == base {
			t.Errorf("a %s change kept the key", name)
		}
	}
	other, _ := newVerdictCache(10, time.Hour, time.Now)
	if other.digest("kr", e, pol, "text") == base {
		t.Fatal("two generations share a key")
	}
}

func TestCacheDisabledAtZero(t *testing.T) {
	c, err := newVerdictCache(0, time.Hour, time.Now)
	if c != nil || err != nil {
		t.Fatal("max_entries 0 must disable the cache")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/classifier/ -run 'TestRegister|TestNew|TestSettings|TestFor|TestCache' -v`
Expected: FAIL to compile (`undefined: Register`, `undefined: newVerdictCache`, …).

- [ ] **Step 3: Implement `classifier.go`**

```go
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
// receives only units that Units produced.
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
```

- [ ] **Step 4: Implement `set.go`**

```go
package classifier

import (
	"slices"
	"time"
)

// Entry is one resolved classifier of a generation.
type Entry struct {
	Name, Revision string
	Mode           Mode
	Classifier     Classifier
	Global         bool
	Teams          map[string]bool
	// Settings is SettingsDigest of the entry's Config; part of every cache key.
	Settings [32]byte
}

// Set is the classifier view of one live.State generation, with its own
// verdict cache and HMAC key. A nil Set has no entries.
type Set struct {
	Entries       []Entry
	Timeout       time.Duration
	MaxInputBytes int
	cache         *verdictCache
}

// NewSet builds a Set. cacheEntries == 0 disables the verdict cache.
func NewSet(entries []Entry, timeout time.Duration, maxInputBytes, cacheEntries int, cacheTTL time.Duration) (*Set, error) {
	c, err := newVerdictCache(cacheEntries, cacheTTL, time.Now)
	if err != nil {
		return nil, err
	}
	return &Set{Entries: slices.Clone(entries), Timeout: timeout, MaxInputBytes: maxInputBytes, cache: c}, nil
}

// For returns the entries that apply to team, in configuration order.
func (s *Set) For(team string) []Entry {
	if s == nil {
		return nil
	}
	var out []Entry
	for _, e := range s.Entries {
		if e.Global || e.Teams[team] {
			out = append(out, e)
		}
	}
	return out
}
```

- [ ] **Step 5: Implement `cache.go`**

```go
package classifier

import (
	"container/list"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"strconv"
	"sync"
	"time"
)

// PolicyRef identifies an applicable privacy rule; part of every cache key.
type PolicyRef struct {
	Name       string
	Generation int64
	Rule       string
}

// verdictCache remembers which units a classifier already judged clear. Keys
// are HMAC-SHA256 under a per-generation random key, so neither a key nor its
// presence means anything outside this generation. It stores no text and no
// verdict other than Clear.
type verdictCache struct {
	mu    sync.Mutex
	key   []byte
	max   int
	ttl   time.Duration
	now   func() time.Time
	order *list.List // front = most recently used
	items map[[32]byte]*list.Element
}

type cacheItem struct {
	key [32]byte
	at  time.Time
}

// newVerdictCache returns nil, nil when max <= 0 (cache disabled).
func newVerdictCache(max int, ttl time.Duration, now func() time.Time) (*verdictCache, error) {
	if max <= 0 {
		return nil, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("classifier: verdict cache key: %w", err)
	}
	return &verdictCache{key: key, max: max, ttl: ttl, now: now, order: list.New(), items: make(map[[32]byte]*list.Element)}, nil
}

func (c *verdictCache) digest(team string, e *Entry, policies []PolicyRef, text string) [32]byte {
	m := hmac.New(sha256.New, c.key)
	field(m, team)
	field(m, e.Name)
	field(m, string(e.Settings[:]))
	field(m, strconv.Itoa(len(policies)))
	for _, p := range policies {
		field(m, p.Name)
		field(m, strconv.FormatInt(p.Generation, 10))
		field(m, p.Rule)
	}
	field(m, text)
	var out [32]byte
	copy(out[:], m.Sum(nil))
	return out
}

// field writes a length-prefixed value so adjacent fields cannot run together.
func field(h hash.Hash, s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	h.Write(n[:])
	h.Write([]byte(s))
}

// clear reports a live hit and refreshes its recency; an expired entry is removed.
func (c *verdictCache) clear(k [32]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[k]
	if !ok {
		return false
	}
	if c.now().Sub(el.Value.(*cacheItem).at) >= c.ttl {
		c.order.Remove(el)
		delete(c.items, k)
		return false
	}
	c.order.MoveToFront(el)
	return true
}

func (c *verdictCache) store(k [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		el.Value.(*cacheItem).at = c.now()
		c.order.MoveToFront(el)
		return
	}
	c.items[k] = c.order.PushFront(&cacheItem{key: k, at: c.now()})
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(*cacheItem).key)
	}
}

func (c *verdictCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/classifier/ -race -v`
Expected: PASS for all six tests.

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/classifier && test -z "$(gofmt -l internal/classifier)" && go vet ./internal/classifier/
git add internal/classifier
git commit -s -m "feat(classifier): leaf seam, registry and HMAC verdict cache" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 2: Parallel runner and Enforce combination

**Files:**
- Create: `internal/classifier/run.go`
- Test: `internal/classifier/run_test.go`

**Interfaces:**
- Consumes: Task 1 (`Set`, `Entry`, `Classifier`, `Output`, `PolicyRef`, cache methods).
- Produces:
  - `type Outcome string` with `OutcomeFlagged` "flagged", `OutcomeClear` "clear", `OutcomeAbstain` "abstain", `OutcomeError` "error", `OutcomeSkipped` "skipped".
  - `type Request struct{ Team string; Policies []PolicyRef; Segments []string }`.
  - `type Result struct{ Name, Revision string; Mode Mode; Outcome Outcome; Duration time.Duration; Windows, CacheHits, CacheMisses int }`.
  - `(*Set).Run(ctx context.Context, entries []Entry, req Request) []Result`; `Combine([]Result) (classified, uninspectable bool)`; `Failed([]Entry) []Result`.

- [ ] **Step 1: Write the failing tests** (`internal/classifier/run_test.go`)

```go
package classifier

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fake struct {
	mu     sync.Mutex
	calls  [][]string
	units  func([]string) []string
	answer func(context.Context, Input) (Output, error)
}

func (f *fake) Units(s []string) []string {
	if f.units != nil {
		return f.units(s)
	}
	return s
}

func (f *fake) Classify(ctx context.Context, in Input) (Output, error) {
	f.mu.Lock()
	f.calls = append(f.calls, slices.Clone(in.Texts))
	f.mu.Unlock()
	return f.answer(ctx, in)
}

func (f *fake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func allClear(_ context.Context, in Input) (Output, error) {
	ok := make([]bool, len(in.Texts))
	for i := range ok {
		ok[i] = true
	}
	return Output{Verdict: Clear, Clear: ok, Windows: 1}, nil
}

func testSet(t *testing.T, cache int, entries ...Entry) *Set {
	t.Helper()
	s, err := NewSet(entries, 2*time.Second, 1<<20, cache, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func enforce(name string, c Classifier) Entry {
	return Entry{Name: name, Mode: Enforce, Classifier: c, Global: true}
}

func req(team string, segments ...string) Request {
	return Request{Team: team, Policies: []PolicyRef{{Name: "privacy", Generation: 7, Rule: "protect"}}, Segments: segments}
}

func TestRunSecondIdenticalRequestIsServedFromCache(t *testing.T) {
	f := &fake{answer: allClear}
	s := testSet(t, 100, enforce("laya", f))
	first := s.Run(context.Background(), s.Entries, req("kr", "system prompt", "hello"))
	second := s.Run(context.Background(), s.Entries, req("kr", "system prompt", "hello"))
	if first[0].Outcome != OutcomeClear || second[0].Outcome != OutcomeClear || f.callCount() != 1 {
		t.Fatalf("first=%+v second=%+v calls=%d", first, second, f.callCount())
	}
	if second[0].CacheHits != 2 || second[0].CacheMisses != 0 {
		t.Fatalf("cache accounting %+v", second[0])
	}
}

func TestRunNeverCachesFlaggedAbstainOrError(t *testing.T) {
	for name, answer := range map[string]func(context.Context, Input) (Output, error){
		"flagged": func(context.Context, Input) (Output, error) { return Output{Verdict: Flagged}, nil },
		"abstain": func(context.Context, Input) (Output, error) { return Output{Verdict: Abstain}, nil },
		"error":   func(context.Context, Input) (Output, error) { return Output{}, ErrStatus },
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{answer: answer}
			s := testSet(t, 100, enforce("laya", f))
			s.Run(context.Background(), s.Entries, req("kr", "김민수 010-2345-6789"))
			s.Run(context.Background(), s.Entries, req("kr", "김민수 010-2345-6789"))
			if f.callCount() != 2 {
				t.Fatalf("a %s answer was cached", name)
			}
		})
	}
}

func TestRunCachesOnlyReportedClearUnits(t *testing.T) {
	f := &fake{answer: func(_ context.Context, in Input) (Output, error) {
		return Output{Verdict: Abstain, Clear: []bool{true, false}}, nil
	}}
	s := testSet(t, 100, enforce("laya", f))
	s.Run(context.Background(), s.Entries, req("kr", "read", "unread"))
	f.answer = allClear
	s.Run(context.Background(), s.Entries, req("kr", "read", "unread"))
	if got := f.calls[1]; !slices.Equal(got, []string{"unread"}) {
		t.Fatalf("second call sent %q", got)
	}
}

// Review Focus 1: a long tool result is read across requests, never abstaining forever.
func TestRunWarmsAcrossRequestsUntilEverythingIsRead(t *testing.T) {
	const perCall = 2
	f := &fake{
		units: func(segments []string) []string { // split each segment into 3-rune chunks
			var out []string
			for _, s := range segments {
				r := []rune(s)
				for len(r) > 3 {
					out, r = append(out, string(r[:3])), r[3:]
				}
				out = append(out, string(r))
			}
			return out
		},
		answer: func(_ context.Context, in Input) (Output, error) {
			ok := make([]bool, len(in.Texts))
			for i := 0; i < len(ok) && i < perCall; i++ {
				ok[i] = true
			}
			if len(in.Texts) > perCall {
				return Output{Verdict: Abstain, Clear: ok}, nil
			}
			return Output{Verdict: Clear, Clear: ok}, nil
		},
	}
	s := testSet(t, 100, enforce("laya", f))
	long := "가나다라마바사아자차카타파하거" // five distinct 3-rune units
	var outcomes []Outcome
	for range 4 {
		outcomes = append(outcomes, s.Run(context.Background(), s.Entries, req("kr", long))[0].Outcome)
	}
	if !slices.Equal(outcomes, []Outcome{OutcomeAbstain, OutcomeAbstain, OutcomeClear, OutcomeClear}) || f.callCount() != 3 {
		t.Fatalf("outcomes %v, calls %d", outcomes, f.callCount())
	}
}

func TestRunDeduplicatesIdenticalUnits(t *testing.T) {
	f := &fake{answer: allClear}
	s := testSet(t, 100, enforce("laya", f))
	s.Run(context.Background(), s.Entries, req("kr", "role", "user", "role", "hello"))
	if !slices.Equal(f.calls[0], []string{"role", "user", "hello"}) {
		t.Fatalf("sent %q", f.calls[0])
	}
}

func TestRunCacheIsScopedByTeamAndPolicy(t *testing.T) {
	f := &fake{answer: allClear}
	s := testSet(t, 100, enforce("laya", f))
	s.Run(context.Background(), s.Entries, req("kr", "hello"))
	s.Run(context.Background(), s.Entries, req("us", "hello"))
	changed := req("kr", "hello")
	changed.Policies = []PolicyRef{{Name: "privacy", Generation: 8, Rule: "protect"}}
	s.Run(context.Background(), s.Entries, changed)
	if f.callCount() != 3 {
		t.Fatalf("calls = %d, want 3 (team and policy generation scope the cache)", f.callCount())
	}
}

func TestRunSkipsEmptyAndAbstainsOverInputCap(t *testing.T) {
	f := &fake{answer: allClear}
	s := testSet(t, 100, enforce("laya", f))
	if got := s.Run(context.Background(), s.Entries, req("kr", "", "")); got[0].Outcome != OutcomeSkipped {
		t.Fatalf("empty input: %+v", got)
	}
	s.MaxInputBytes = 4
	if got := s.Run(context.Background(), s.Entries, req("kr", "12345")); got[0].Outcome != OutcomeAbstain {
		t.Fatalf("over cap: %+v", got)
	}
	if f.callCount() != 0 {
		t.Fatal("a skipped or over-cap request reached the classifier")
	}
}

func TestRunDeadlineIsAnErrorAndCachesNothing(t *testing.T) {
	f := &fake{answer: func(ctx context.Context, in Input) (Output, error) {
		<-ctx.Done()
		return allClear(ctx, in) // a plugin that ignores the deadline must still not populate the cache
	}}
	s := testSet(t, 100, enforce("laya", f))
	s.Timeout = 20 * time.Millisecond
	if got := s.Run(context.Background(), s.Entries, req("kr", "hello")); got[0].Outcome != OutcomeError {
		t.Fatalf("deadline: %+v", got)
	}
	f.answer = allClear
	s.Timeout = time.Second
	s.Run(context.Background(), s.Entries, req("kr", "hello"))
	if f.callCount() != 2 {
		t.Fatal("a late answer populated the cache")
	}
}

func TestRunRejectsMismatchedClearAndUnknownVerdict(t *testing.T) {
	for name, out := range map[string]Output{
		"short clear":     {Verdict: Clear, Clear: []bool{true}},
		"unknown verdict": {Verdict: Verdict(42)},
	} {
		f := &fake{answer: func(context.Context, Input) (Output, error) { return out, nil }}
		s := testSet(t, 100, enforce("laya", f))
		if got := s.Run(context.Background(), s.Entries, req("kr", "a", "b")); got[0].Outcome != OutcomeError {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestRunEntriesInParallel(t *testing.T) {
	var arrived atomic.Int32
	ready := make(chan struct{})
	both := func(ctx context.Context, in Input) (Output, error) {
		if arrived.Add(1) == 2 {
			close(ready)
		}
		select {
		case <-ready: // reached only if both entries are in flight at once
			return allClear(ctx, in)
		case <-ctx.Done():
			return Output{}, ErrTimeout
		}
	}
	a, b := &fake{answer: both}, &fake{answer: both}
	s := testSet(t, 0, enforce("a", a), Entry{Name: "b", Mode: Shadow, Classifier: b, Global: true})
	for _, r := range s.Run(context.Background(), s.Entries, req("kr", "hello")) {
		if r.Outcome != OutcomeClear {
			t.Fatalf("entries did not run in parallel: %+v", r)
		}
	}
}

func TestRunWithoutCacheStillClassifies(t *testing.T) {
	f := &fake{answer: allClear}
	s := testSet(t, 0, enforce("laya", f))
	s.Run(context.Background(), s.Entries, req("kr", "hello"))
	s.Run(context.Background(), s.Entries, req("kr", "hello"))
	if f.callCount() != 2 {
		t.Fatal("a disabled cache served a hit")
	}
}

func TestCombineCountsOnlyEnforce(t *testing.T) {
	for _, tc := range []struct {
		results                   []Result
		classified, uninspectable bool
	}{
		{[]Result{{Mode: Shadow, Outcome: OutcomeFlagged}}, false, false},
		{[]Result{{Mode: Enforce, Outcome: OutcomeFlagged}, {Mode: Enforce, Outcome: OutcomeError}}, true, false},
		{[]Result{{Mode: Enforce, Outcome: OutcomeAbstain}}, false, true},
		{[]Result{{Mode: Enforce, Outcome: OutcomeError}, {Mode: Shadow, Outcome: OutcomeFlagged}}, false, true},
		{[]Result{{Mode: Enforce, Outcome: OutcomeClear}, {Mode: Enforce, Outcome: OutcomeSkipped}}, false, false},
	} {
		c, u := Combine(tc.results)
		if c != tc.classified || u != tc.uninspectable {
			t.Errorf("Combine(%+v) = %v %v", tc.results, c, u)
		}
	}
}

func TestFailedMarksEveryEntryError(t *testing.T) {
	got := Failed([]Entry{{Name: "a", Mode: Enforce}, {Name: "b", Mode: Shadow}})
	if len(got) != 2 || got[0].Outcome != OutcomeError || got[1].Outcome != OutcomeError || got[1].Mode != Shadow {
		t.Fatalf("%+v", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/classifier/ -run 'TestRun|TestCombine|TestFailed' -v`
Expected: FAIL to compile (`undefined: OutcomeClear`, `s.Run undefined`).

- [ ] **Step 3: Implement `run.go`**

```go
package classifier

import (
	"context"
	"sync"
	"time"
)

// Outcome is the bounded audit and metric result of one entry on one request.
type Outcome string

const (
	OutcomeFlagged Outcome = "flagged"
	OutcomeClear   Outcome = "clear"
	OutcomeAbstain Outcome = "abstain"
	OutcomeError   Outcome = "error"
	OutcomeSkipped Outcome = "skipped"
)

// Request is one routing request's classifier input. Segments are borrowed.
type Request struct {
	Team     string
	Policies []PolicyRef
	Segments []string
}

// Result reports one entry's outcome. It carries no text.
type Result struct {
	Name, Revision string
	Mode           Mode
	Outcome        Outcome
	Duration       time.Duration
	Windows        int
	CacheHits      int
	CacheMisses    int
}

// Run classifies req with every entry in parallel under the Set's timeout. It
// never retries. An entry still running at the deadline is an error, and its
// answer, if it arrives later, is not cached.
func (s *Set) Run(ctx context.Context, entries []Entry, req Request) []Result {
	results := make([]Result, len(entries))
	for i, e := range entries {
		results[i] = Result{Name: e.Name, Revision: e.Revision, Mode: e.Mode}
	}
	total, empty := 0, true
	for _, t := range req.Segments {
		total += len(t)
		empty = empty && t == ""
	}
	switch {
	case len(entries) == 0:
		return results
	case empty:
		return withOutcome(results, OutcomeSkipped)
	case total > s.MaxInputBytes:
		return withOutcome(results, OutcomeAbstain)
	}
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	var wg sync.WaitGroup
	for i := range entries {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.runEntry(ctx, &entries[i], req, &results[i])
		}(i)
	}
	wg.Wait()
	return results
}

func (s *Set) runEntry(ctx context.Context, e *Entry, req Request, res *Result) {
	start := time.Now()
	defer func() { res.Duration = time.Since(start) }()
	var texts []string
	var keys [][32]byte
	seen := make(map[string]bool)
	for _, unit := range e.Classifier.Units(req.Segments) {
		if unit == "" || seen[unit] {
			continue
		}
		seen[unit] = true
		if s.cache != nil {
			k := s.cache.digest(req.Team, e, req.Policies, unit)
			if s.cache.clear(k) {
				res.CacheHits++
				continue
			}
			keys = append(keys, k)
		}
		res.CacheMisses++
		texts = append(texts, unit)
	}
	if len(texts) == 0 {
		res.Outcome = OutcomeClear
		return
	}
	out, err := e.Classifier.Classify(ctx, Input{Texts: texts})
	res.Windows = out.Windows
	if err != nil || ctx.Err() != nil || (out.Clear != nil && len(out.Clear) != len(texts)) {
		res.Outcome = OutcomeError
		return
	}
	switch out.Verdict {
	case Clear:
		res.Outcome = OutcomeClear
	case Flagged:
		res.Outcome = OutcomeFlagged
	case Abstain:
		res.Outcome = OutcomeAbstain
	default:
		res.Outcome = OutcomeError
		return
	}
	if s.cache != nil {
		for i, ok := range out.Clear {
			if ok {
				s.cache.store(keys[i])
			}
		}
	}
}

func withOutcome(results []Result, o Outcome) []Result {
	for i := range results {
		results[i].Outcome = o
	}
	return results
}

// Failed reports every entry as an error, for an input that could not be extracted.
func Failed(entries []Entry) []Result {
	results := make([]Result, len(entries))
	for i, e := range entries {
		results[i] = Result{Name: e.Name, Revision: e.Revision, Mode: e.Mode, Outcome: OutcomeError}
	}
	return results
}

// Combine applies the Enforce-only rule: any Enforce flagged classifies the
// request; otherwise any Enforce abstain or error makes it uninspectable.
func Combine(results []Result) (classified, uninspectable bool) {
	for _, r := range results {
		if r.Mode != Enforce {
			continue
		}
		switch r.Outcome {
		case OutcomeFlagged:
			classified = true
		case OutcomeAbstain, OutcomeError:
			uninspectable = true
		}
	}
	if classified {
		uninspectable = false
	}
	return classified, uninspectable
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/classifier/ -race -count=3 -v`
Expected: PASS. `-count=3` shakes out timing assumptions in the deadline and parallel tests.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/classifier && test -z "$(gofmt -l internal/classifier)" && go vet ./internal/classifier/
git add internal/classifier
git commit -s -m "feat(classifier): parallel runner with unit cache and Enforce-only combination" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 3: Content extraction `sensitivity.Segments`

**Files:**
- Create: `internal/sensitivity/segments.go`
- Test: `internal/sensitivity/segments_test.go`

**Interfaces:**
- Consumes: the unexported `walker`, `Result.inspectShape`, `outputBudget` and `errProtocol` in `internal/sensitivity`.
- Produces: `func Segments(ctx context.Context, protocol string, raw []byte) ([]string, error)`.
  - It returns, in body order, every text `Inspect`'s `visit` sees: object keys, string values, number spellings and the decoded pieces of encoded JSON strings.
  - It fails on exactly the inputs `Inspect` rejects.

- [ ] **Step 1: Write the failing tests** (`internal/sensitivity/segments_test.go`)

```go
package sensitivity

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestSegmentsFollowTheInspectorVisitOrder(t *testing.T) {
	raw := []byte(`{"system":"sys","messages":[{"role":"user","content":"hello 김민수"}],"max_tokens":32}`)
	got, err := Segments(context.Background(), "anthropic", raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"system", "sys", "messages", "role", "user", "content", "hello 김민수", "max_tokens", "32"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

// Review Focus 2 and PR-08: every retransmitted position is classifier input,
// including system input, tool schemas, assistant history, tool calls and results.
func TestSegmentsCoverEveryProtectedPosition(t *testing.T) {
	for _, tc := range []struct {
		protocol, body string
		marks          []string
	}{
		{"anthropic", `{"system":[{"type":"text","text":"SYSTEM-MARK"}],"tools":[{"name":"read_file","description":"TOOL-DESC-MARK","input_schema":{"type":"object","properties":{"path":{"type":"string","description":"SCHEMA-MARK"}}}}],"messages":[{"role":"user","content":"first question"},{"role":"assistant","content":[{"type":"text","text":"HISTORY-MARK"},{"type":"tool_use","id":"t1","name":"read_file","input":{"path":"ARG-MARK"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"RESULT-MARK"}]}],"max_tokens":64}`,
			[]string{"SYSTEM-MARK", "TOOL-DESC-MARK", "SCHEMA-MARK", "HISTORY-MARK", "ARG-MARK", "RESULT-MARK"}},
		{"openai", `{"messages":[{"role":"system","content":"SYSTEM-MARK"},{"role":"user","content":"q"},{"role":"assistant","content":"HISTORY-MARK","tool_calls":[{"id":"c1","type":"function","function":{"name":"lookup","arguments":"{\"name\":\"\\uae40\\ubbfc\\uc218\"}"}}]},{"role":"tool","tool_call_id":"c1","content":"RESULT-MARK"}],"tools":[{"type":"function","function":{"name":"lookup","description":"TOOL-DESC-MARK","parameters":{"type":"object","properties":{}}}}]}`,
			[]string{"SYSTEM-MARK", "HISTORY-MARK", "김민수", "RESULT-MARK", "TOOL-DESC-MARK"}},
		{"responses", `{"model":"m","instructions":"SYSTEM-MARK","tools":[{"type":"function","name":"lookup","description":"TOOL-DESC-MARK","parameters":{"type":"object","properties":{}}}],"input":[{"role":"user","content":[{"type":"input_text","text":"q"}]},{"role":"assistant","content":[{"type":"output_text","text":"HISTORY-MARK"}]},{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{\"q\":\"ARG-MARK\"}"},{"type":"function_call_output","call_id":"c1","output":"RESULT-MARK"}]}`,
			[]string{"SYSTEM-MARK", "TOOL-DESC-MARK", "HISTORY-MARK", "ARG-MARK", "RESULT-MARK"}},
		{"bedrock", `{"anthropic_version":"bedrock-2023-05-31","system":"SYSTEM-MARK","max_tokens":64,"messages":[{"role":"user","content":"q"},{"role":"assistant","content":"HISTORY-MARK"},{"role":"user","content":"RESULT-MARK"}]}`,
			[]string{"SYSTEM-MARK", "HISTORY-MARK", "RESULT-MARK"}},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			if _, err := NewInspector().Inspect(context.Background(), tc.protocol, []byte(tc.body)); err != nil {
				t.Fatalf("fixture rejected by Inspect (fix the fixture, not Segments): %v", err)
			}
			got, err := Segments(context.Background(), tc.protocol, []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(got, "\n")
			for _, mark := range tc.marks {
				if !strings.Contains(joined, mark) {
					t.Errorf("segments miss %s", mark)
				}
			}
		})
	}
}

func TestSegmentsFailWhereInspectFails(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		ctx      context.Context
		protocol string
		body     string
	}{
		"malformed":     {context.Background(), "anthropic", `{"messages":[`},
		"duplicate key": {context.Background(), "anthropic", `{"messages":[],"messages":[]}`},
		"protocol":      {context.Background(), "gemini", `{"messages":[]}`},
		"cancelled":     {cancelled, "anthropic", `{"messages":[]}`},
		"budget":        {context.Background(), "anthropic", `{"messages":[{"role":"user","content":"x"}],"max_tokens":"lots"}`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewInspector().Inspect(tc.ctx, tc.protocol, []byte(tc.body)); err == nil {
				t.Fatal("Inspect accepted the case; the case is wrong")
			}
			if got, err := Segments(tc.ctx, tc.protocol, []byte(tc.body)); err == nil {
				t.Fatalf("Segments accepted it: %q", got)
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/sensitivity/ -run TestSegments -v`
Expected: FAIL to compile (`undefined: Segments`).

- [ ] **Step 3: Implement `segments.go`**

```go
package sensitivity

import "context"

// Segments returns, in body order, every text the request inspector visits:
// object keys, string values, number spellings and the decoded pieces of
// encoded JSON strings (which therefore appear twice, encoded and decoded). It
// fails on exactly the inputs Inspect rejects. The strings are decoded copies;
// callers must not retain them beyond the request.
func Segments(ctx context.Context, protocol string, raw []byte) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch protocol {
	case "anthropic", "openai", "responses", "bedrock":
	default:
		return nil, errProtocol
	}
	var segments []string
	w := walker{ctx: ctx, visit: func(text string) error {
		segments = append(segments, text)
		return nil
	}}
	root, err := w.decode(raw, 0)
	if err != nil {
		return nil, err
	}
	var shape Result
	if err := shape.inspectShape(protocol, root); err != nil {
		return nil, err
	}
	if body, ok := root.(map[string]any); ok {
		if _, err := outputBudget(body); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return segments, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/sensitivity/ -race -v`
Expected: PASS, including every existing sensitivity test.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/sensitivity && test -z "$(gofmt -l internal/sensitivity)" && go vet ./internal/sensitivity/
git add internal/sensitivity/segments.go internal/sensitivity/segments_test.go
git commit -s -m "feat(sensitivity): expose the inspector's visit sequence as classifier segments" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 4: systemone windowing

**Files:**
- Create: `plugins/systemone/window.go`
- Test: `plugins/systemone/window_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces (package-internal):
  - `type window struct{ text string; parts []int }`;
  - `splitRunes(s string, size, overlap int) []string`;
  - `buildWindows(units []string, size, overlap int) []window`.

- [ ] **Step 1: Write the failing tests** (`plugins/systemone/window_test.go`)

```go
package systemone

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBuildWindowsPacksShortUnits(t *testing.T) {
	got := buildWindows([]string{"a", "", "bc"}, 10, 2)
	if len(got) != 1 || got[0].text != "a\n\nbc" || !slices.Equal(got[0].parts, []int{0, 2}) {
		t.Fatalf("%+v", got)
	}
}

func TestBuildWindowsFlushesWhenFull(t *testing.T) {
	got := buildWindows([]string{"aaaa", "bbbb"}, 6, 0)
	if len(got) != 2 || got[0].text != "aaaa" || got[1].text != "bbbb" || got[1].parts[0] != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestBuildWindowsSplitsAnOversizedUnit(t *testing.T) {
	got := buildWindows([]string{"short", strings.Repeat("가", 25)}, 10, 2)
	for i, w := range got[1:] {
		if !slices.Equal(w.parts, []int{1}) || utf8.RuneCountInString(w.text) > 10 {
			t.Fatalf("window %d %+v", i+1, w)
		}
	}
}

func TestSplitRunesPrefersALateNewline(t *testing.T) {
	s := strings.Repeat("a", 8) + "\n" + strings.Repeat("b", 20)
	got := splitRunes(s, 10, 2)
	if got[0] != strings.Repeat("a", 8)+"\n" {
		t.Fatalf("first window %q", got[0])
	}
}

func TestSplitRunesOverlapsAndCoversEverything(t *testing.T) {
	s := strings.Repeat("가나다라마바사\n", 40) + "끝"
	for _, size := range []int{5, 7, 12, 40} {
		for overlap := 0; overlap <= size/2; overlap++ {
			got := splitRunes(s, size, overlap)
			rebuilt := got[0]
			for _, w := range got[1:] {
				prev := []rune(rebuilt)
				if !strings.HasPrefix(w, string(prev[len(prev)-overlap:])) {
					t.Fatalf("size %d overlap %d: window does not repeat the overlap", size, overlap)
				}
				rebuilt += string([]rune(w)[overlap:])
			}
			if rebuilt != s {
				t.Fatalf("size %d overlap %d: windows do not cover the text", size, overlap)
			}
		}
	}
}

func TestSplitRunesNeverBreaksARune(t *testing.T) {
	s := strings.Repeat("가a😀\n김", 50)
	for size := 5; size <= 13; size++ {
		for overlap := 0; overlap <= size/2; overlap++ {
			for _, w := range splitRunes(s, size, overlap) {
				if !utf8.ValidString(w) || utf8.RuneCountInString(w) > size {
					t.Fatalf("size %d overlap %d: bad window %q", size, overlap, w)
				}
			}
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./plugins/systemone/ -run 'TestBuildWindows|TestSplitRunes' -v`
Expected: FAIL to compile (`undefined: buildWindows`).

- [ ] **Step 3: Implement `window.go`**

```go
package systemone

import (
	"strings"
	"unicode/utf8"
)

// window is one /v1/systemone state: text packed from units, and the indexes
// of every unit with any text in it.
type window struct {
	text  string
	parts []int
}

// buildWindows packs consecutive short units into windows of at most size
// runes, joined by a blank line, and splits a unit longer than size with
// splitRunes. Empty units get no window.
func buildWindows(units []string, size, overlap int) []window {
	var out []window
	var cur strings.Builder
	var parts []int
	runes := 0
	flush := func() {
		if len(parts) > 0 {
			out = append(out, window{text: cur.String(), parts: parts})
		}
		cur.Reset()
		parts, runes = nil, 0
	}
	for i, u := range units {
		n := utf8.RuneCountInString(u)
		switch {
		case n == 0:
			continue
		case n > size:
			flush()
			for _, chunk := range splitRunes(u, size, overlap) {
				out = append(out, window{text: chunk, parts: []int{i}})
			}
			continue
		}
		sep := 0
		if len(parts) > 0 {
			sep = 2
		}
		if runes+sep+n > size {
			flush()
			sep = 0
		}
		if sep > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(u)
		parts = append(parts, i)
		runes += sep + n
	}
	flush()
	return out
}

// splitRunes cuts s into windows of at most size runes. Each cut prefers the
// last newline in the final fifth of the window, and each window after the
// first repeats the previous window's last overlap runes. It requires
// 0 <= overlap <= size/2, which the option bounds guarantee.
func splitRunes(s string, size, overlap int) []string {
	r := []rune(s)
	var out []string
	for start := 0; ; {
		end := start + size
		if end >= len(r) {
			return append(out, string(r[start:]))
		}
		for j := end - 1; j >= start+size*4/5; j-- {
			if r[j] == '\n' {
				end = j + 1
				break
			}
		}
		out = append(out, string(r[start:end]))
		next := end - overlap
		if next <= start {
			next = end
		}
		start = next
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./plugins/systemone/ -run 'TestBuildWindows|TestSplitRunes' -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w plugins/systemone && test -z "$(gofmt -l plugins/systemone)" && go vet ./plugins/systemone/
git add plugins/systemone/window.go plugins/systemone/window_test.go
git commit -s -m "feat(systemone): rune-safe window packing and overlapping splits" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 5: systemone classifier: options, verdicts and wire

**Files:**
- Create: `plugins/systemone/systemone.go`, `plugins/systemone/client.go`
- Test: `plugins/systemone/systemone_test.go`

**Interfaces:**
- Consumes:
  - Task 1: `classifier.Register`, `classifier.Config`, `classifier.Classifier`, `classifier.Input`/`Output`, the verdict constants and the error classes.
  - Task 4: `buildWindows`, `splitRunes`.
- Produces:
  - `func New(cfg classifier.Config) (classifier.Classifier, error)`, registered in `init()` as type `"systemone"`.
  - Unexported `*systemone` with `Units` and `Classify`.
  - `defaultInstructions` (the trained statement), `questionID = "pii"`, `maxResponseBytes = 256 << 10`.

- [ ] **Step 1: Write the failing tests** (`plugins/systemone/systemone_test.go`)

```go
package systemone

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/classifier"
)

const bearer = "bearer-value-123"

func options(t *testing.T, raw string) map[string]json.RawMessage {
	t.Helper()
	if raw == "" {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func newClassifier(t *testing.T, endpoint, opts string, threshold float64, token string) classifier.Classifier {
	t.Helper()
	c, err := New(classifier.Config{Name: "laya-ko", Type: "systemone", Endpoint: endpoint, Threshold: threshold, BearerToken: token, Options: options(t, opts)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// batchServer answers /v1/systemone/batch with one probability per state.
func batchServer(t *testing.T, seen func(r *http.Request, body map[string]any), probs func(states []string) []float64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("request body: %v", err)
		}
		if seen != nil {
			seen(r, body)
		}
		var states []string
		for _, s := range body["states"].([]any) {
			states = append(states, s.(string))
		}
		var results []map[string]any
		for _, p := range probs(states) {
			results = append(results, map[string]any{"model": "laya-rl-agent", "answers": map[string]any{"pii": map[string]any{"type": "noul", "noul": p}}})
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results, "total_usage": map[string]int{"input_tokens": 1}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func constant(p float64) func([]string) []float64 {
	return func(states []string) []float64 {
		out := make([]float64, len(states))
		for i := range out {
			out[i] = p
		}
		return out
	}
}

func TestRegistersSystemoneType(t *testing.T) {
	if !slices.Contains(classifier.Types(), "systemone") {
		t.Fatal("systemone is not registered")
	}
}

func TestBatchRequestCarriesTheTrainedQuestion(t *testing.T) {
	srv := batchServer(t, func(r *http.Request, body map[string]any) {
		if r.URL.Path != "/v1/systemone/batch" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "" {
			t.Errorf("path %s, content-type %q, authorization %q", r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Authorization"))
		}
		q := body["questions"].(map[string]any)["pii"].(map[string]any)
		if q["type"] != "noul" || q["instructions"] != defaultInstructions || q["criteria"] != nil || body["model"] != nil {
			t.Errorf("question %v, model %v", q, body["model"])
		}
	}, constant(0.01))
	out, err := newClassifier(t, srv.URL, "", 0.5, "").Classify(context.Background(), classifier.Input{Texts: []string{"hello"}})
	if err != nil || out.Verdict != classifier.Clear || !slices.Equal(out.Clear, []bool{true}) || out.Windows != 1 {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestRequestCarriesConfiguredSettingsVerbatim(t *testing.T) {
	srv := batchServer(t, func(r *http.Request, body map[string]any) {
		q := body["questions"].(map[string]any)["pii"].(map[string]any)
		crit := q["criteria"].(map[string]any)
		if r.Header.Get("Authorization") != "Bearer "+bearer || body["model"] != "multilingual" || q["instructions"] != "Is there PII?" || crit["true"] != "yes" || crit["false"] != "no" {
			t.Errorf("body %v", body)
		}
	}, constant(0.01))
	c := newClassifier(t, srv.URL+"/", `{"model":"multilingual","instructions":"Is there PII?","criteria":{"true":"yes","false":"no"}}`, 0.5, bearer)
	if _, err := c.Classify(context.Background(), classifier.Input{Texts: []string{"hello"}}); err != nil {
		t.Fatal(err)
	}
}

func TestVerdictBoundaries(t *testing.T) {
	for _, tc := range []struct {
		opts string
		p    float64
		want classifier.Verdict
	}{
		{"", 0.4999, classifier.Clear},
		{"", 0.5, classifier.Flagged},
		{`{"abstain_band":{"low":0.1,"high":0.9}}`, 0.1, classifier.Clear},
		{`{"abstain_band":{"low":0.1,"high":0.9}}`, 0.1000001, classifier.Abstain},
		{`{"abstain_band":{"low":0.1,"high":0.9}}`, 0.5, classifier.Abstain},
		{`{"abstain_band":{"low":0.1,"high":0.9}}`, 0.9, classifier.Flagged},
		{`{"abstain_band":{"low":0.1,"high":0.9}}`, 0.05, classifier.Clear},
	} {
		srv := batchServer(t, nil, constant(tc.p))
		out, err := newClassifier(t, srv.URL, tc.opts, 0.5, "").Classify(context.Background(), classifier.Input{Texts: []string{"x"}})
		if err != nil || out.Verdict != tc.want {
			t.Errorf("opts %s p %v: %+v %v", tc.opts, tc.p, out, err)
		}
	}
}

func TestOneFlaggedWindowFlagsTheRequestAndClearsTheRest(t *testing.T) {
	srv := batchServer(t, nil, func(states []string) []float64 { return []float64{0.01, 0.99} })
	units := []string{strings.Repeat("가", 150), strings.Repeat("나", 150)}
	out, err := newClassifier(t, srv.URL, `{"window_runes":200}`, 0.5, "").Classify(context.Background(), classifier.Input{Texts: units})
	if err != nil || out.Verdict != classifier.Flagged || !slices.Equal(out.Clear, []bool{true, false}) {
		t.Fatalf("%+v %v", out, err)
	}
}

// Review Focus 1: read a prefix of windows, abstain, and report what was read.
func TestMaxWindowsReadsPrefixAndAbstains(t *testing.T) {
	var states atomic.Int64
	srv := batchServer(t, func(_ *http.Request, body map[string]any) { states.Store(int64(len(body["states"].([]any)))) }, constant(0.01))
	units := []string{strings.Repeat("가", 150), strings.Repeat("나", 150), strings.Repeat("다", 150)}
	out, err := newClassifier(t, srv.URL, `{"window_runes":200,"max_windows":2}`, 0.5, "").Classify(context.Background(), classifier.Input{Texts: units})
	if err != nil || out.Verdict != classifier.Abstain || !slices.Equal(out.Clear, []bool{true, true, false}) || states.Load() != 2 || out.Windows != 2 {
		t.Fatalf("%+v %v states=%d", out, err, states.Load())
	}
}

func TestUnitsSplitsOnlyLongSegments(t *testing.T) {
	c := newClassifier(t, "http://127.0.0.1:1", `{"window_runes":200,"window_overlap_runes":20}`, 0.5, "")
	units := c.Units([]string{"short", strings.Repeat("가", 450)})
	if units[0] != "short" || len(units) != 4 {
		t.Fatalf("units %d", len(units))
	}
	for _, u := range units[1:] {
		if n := []rune(u); len(n) > 200 {
			t.Fatalf("unit of %d runes", len(n))
		}
	}
}

// Review Focus 3: every failure is a bounded class with no request text or token.
func TestErrorsAreBoundedClasses(t *testing.T) {
	const pii = "김민수 010-2345-6789"
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("a redirect was followed") }))
	t.Cleanup(other.Close)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	for name, tc := range map[string]struct {
		handler http.HandlerFunc
		url     string
		timeout time.Duration
		want    error
	}{
		"500":          {handler: func(w http.ResponseWriter, _ *http.Request) { http.Error(w, pii+" "+bearer, 500) }, want: classifier.ErrStatus},
		"503":          {handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }, want: classifier.ErrStatus},
		"redirect":     {handler: func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) }, want: classifier.ErrStatus},
		"malformed":    {handler: func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"results":[`) }, want: classifier.ErrDecode},
		"count":        {handler: func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"results":[]}`) }, want: classifier.ErrDecode},
		"not noul":     {handler: answerWith(`{"type":"choice","choice":"a"}`), want: classifier.ErrDecode},
		"out of range": {handler: answerWith(`{"type":"noul","noul":1.5}`), want: classifier.ErrDecode},
		"missing":      {handler: answerWith(`{"type":"noul"}`), want: classifier.ErrDecode},
		"oversized": {handler: func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{"results":[{"answers":{"pii":{"type":"noul","noul":0.1}}}],"pad":"`+strings.Repeat("x", maxResponseBytes)+`"}`)
		}, want: classifier.ErrDecode},
		"refused": {url: closed.URL, want: classifier.ErrTransport},
		"timeout": {handler: func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body) // the server notices a closed client only after the body is read
			<-r.Context().Done()
		}, timeout: 50 * time.Millisecond, want: classifier.ErrTimeout},
	} {
		t.Run(name, func(t *testing.T) {
			url := tc.url
			if tc.handler != nil {
				srv := httptest.NewServer(tc.handler)
				t.Cleanup(srv.Close)
				url = srv.URL
			}
			ctx := context.Background()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}
			out, err := newClassifier(t, url, "", 0.5, bearer).Classify(ctx, classifier.Input{Texts: []string{pii}})
			if !errors.Is(err, tc.want) || out.Clear != nil {
				t.Fatalf("got %+v %v, want %v", out, err, tc.want)
			}
			if strings.Contains(err.Error(), "김민수") || strings.Contains(err.Error(), bearer) || strings.Contains(err.Error(), "127.0.0.1") {
				t.Fatalf("error leaks request data: %v", err)
			}
		})
	}
}

func answerWith(item string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"results":[{"answers":{"pii":`+item+`}}]}`)
	}
}

func TestSingleModeBoundsConcurrency(t *testing.T) {
	var inFlight, peak, calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		n := inFlight.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/v1/systemone" || body["state"] == nil {
			t.Errorf("single request %s %v", r.URL.Path, body)
		}
		io.WriteString(w, `{"answers":{"pii":{"type":"noul","noul":0.01}}}`)
	}))
	t.Cleanup(srv.Close)
	units := make([]string, 6)
	for i := range units {
		units[i] = strings.Repeat(string(rune('가'+i)), 150)
	}
	out, err := newClassifier(t, srv.URL, `{"batch":false,"concurrency":2,"window_runes":200}`, 0.5, "").Classify(context.Background(), classifier.Input{Texts: units})
	if err != nil || out.Verdict != classifier.Clear || calls.Load() != 6 || peak.Load() > 2 {
		t.Fatalf("%+v %v calls=%d peak=%d", out, err, calls.Load(), peak.Load())
	}
}

func TestSingleModeFailsOnTheFirstError(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(500)
			return
		}
		io.WriteString(w, `{"answers":{"pii":{"type":"noul","noul":0.01}}}`)
	}))
	t.Cleanup(srv.Close)
	_, err := newClassifier(t, srv.URL, `{"batch":false,"concurrency":1,"window_runes":200}`, 0.5, "").Classify(context.Background(), classifier.Input{Texts: []string{strings.Repeat("가", 150), strings.Repeat("나", 150)}})
	if !errors.Is(err, classifier.ErrStatus) {
		t.Fatalf("err = %v", err)
	}
}

func TestEmptyUnitsAreClearWithoutACall(t *testing.T) {
	c := newClassifier(t, "http://127.0.0.1:1", "", 0.5, "")
	out, err := c.Classify(context.Background(), classifier.Input{Texts: []string{"", ""}})
	if err != nil || out.Verdict != classifier.Clear || !slices.Equal(out.Clear, []bool{true, true}) || out.Windows != 0 {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestClientIgnoresProxiesAndRedirects(t *testing.T) {
	c := newClassifier(t, "http://127.0.0.1:1", "", 0.5, "").(*systemone)
	tr, ok := c.client.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil || c.client.CheckRedirect == nil {
		t.Fatal("classifier text could leave through a proxy or a redirect")
	}
}

func TestOptionValidation(t *testing.T) {
	for opts, wantField := range map[string]string{
		`{"unknown":1}`: "unknown",
		`{"abstain_band":{"low":0.6,"high":0.9}}`:         "abstain_band",
		`{"abstain_band":{"low":0.1}}`:                    "abstain_band",
		`{"window_runes":200,"window_overlap_runes":101}`: "window_overlap_runes",
		`{"max_windows":65}`:                              "max_windows",
		`{"concurrency":0}`:                               "concurrency",
		`{"model":"Multi lingual"}`:                       "model",
		`{"instructions":""}`:                             "instructions",
		`{"criteria":{"true":"yes"}}`:                     "criteria",
		`{"batch":null}`:                                  "batch",
		`{"window_runes":4.5}`:                            "window_runes",
	} {
		_, err := New(classifier.Config{Name: "laya-ko", Type: "systemone", Endpoint: "http://127.0.0.1:1", Threshold: 0.5, Options: options(t, opts)})
		if err == nil || !strings.Contains(err.Error(), wantField) {
			t.Errorf("options %s: err = %v, want it to name %s", opts, err, wantField)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./plugins/systemone/ -v`
Expected: FAIL to compile (`undefined: New`, `undefined: defaultInstructions`).

- [ ] **Step 3: Implement `systemone.go`**

```go
// Package systemone is the `systemone` PII classifier. It asks a self-hosted
// /v1/systemone server (Laya, strands-decider) one noul question per window of
// request text and maps the probabilities to a Clear, Flagged or Abstain
// verdict. It never edits a request, never retains text after a call and never
// puts request text, response bodies or the bearer token into an error.
package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/inferplane/inferplane/internal/classifier"
)

const (
	defaultInstructions = "Does this text contain personal information about a specific person, or a secret credential?"
	questionID          = "pii"
	maxResponseBytes    = 256 << 10
)

func init() { classifier.Register("systemone", New) }

var modelPattern = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)

type band struct{ low, high float64 }

type settings struct {
	instructions string
	criteria     map[string]string // nil when not configured
	model        string
	batch        bool
	concurrency  int
	windowRunes  int
	overlapRunes int
	maxWindows   int
	threshold    float64
	band         *band
}

type systemone struct {
	endpoint string
	token    string
	client   *http.Client
	s        settings
}

// New validates the options and builds the classifier. Endpoint, threshold and
// the token ref are validated by the config layer before New is called.
func New(cfg classifier.Config) (classifier.Classifier, error) {
	s, err := parseOptions(cfg.Options, cfg.Threshold)
	if err != nil {
		return nil, fmt.Errorf("systemone %q: %w", cfg.Name, err)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // request text never leaves through an environment proxy
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &systemone{endpoint: strings.TrimRight(cfg.Endpoint, "/"), token: cfg.BearerToken, client: client, s: s}, nil
}

func parseOptions(raw map[string]json.RawMessage, threshold float64) (settings, error) {
	s := settings{instructions: defaultInstructions, batch: true, concurrency: 4, windowRunes: 1000, overlapRunes: 150, maxWindows: 64, threshold: threshold}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	overlapSet := false
	for _, key := range keys {
		v := raw[key]
		var err error
		switch key {
		case "instructions":
			if err = decodeStrict(v, &s.instructions); err == nil && !runesIn(s.instructions, 1, 512) {
				err = errors.New("must be 1-512 characters")
			}
		case "criteria":
			var c struct {
				True  *string `json:"true"`
				False *string `json:"false"`
			}
			if err = decodeStrict(v, &c); err == nil {
				if c.True == nil || c.False == nil || !runesIn(*c.True, 1, 512) || !runesIn(*c.False, 1, 512) {
					err = errors.New(`needs "true" and "false" strings of 1-512 characters`)
				} else {
					s.criteria = map[string]string{"true": *c.True, "false": *c.False}
				}
			}
		case "model":
			if err = decodeStrict(v, &s.model); err == nil && !modelPattern.MatchString(s.model) {
				err = errors.New("must match [a-z0-9._-]{1,64}")
			}
		case "batch":
			err = decodeStrict(v, &s.batch)
		case "concurrency":
			err = intIn(v, &s.concurrency, 1, 16)
		case "window_runes":
			err = intIn(v, &s.windowRunes, 200, 4000)
		case "window_overlap_runes":
			err = intIn(v, &s.overlapRunes, 0, 2000)
			overlapSet = true
		case "max_windows":
			err = intIn(v, &s.maxWindows, 1, 64)
		case "abstain_band":
			var b struct {
				Low  *float64 `json:"low"`
				High *float64 `json:"high"`
			}
			if err = decodeStrict(v, &b); err == nil {
				if b.Low == nil || b.High == nil || !(0 < *b.Low && *b.Low < threshold && threshold < *b.High && *b.High < 1) {
					err = errors.New("needs 0 < low < threshold < high < 1")
				} else {
					s.band = &band{low: *b.Low, high: *b.High}
				}
			}
		default:
			return settings{}, fmt.Errorf("unknown option %q", key)
		}
		if err != nil {
			return settings{}, fmt.Errorf("option %s: %w", key, err)
		}
	}
	if !overlapSet && s.overlapRunes > s.windowRunes/2 {
		s.overlapRunes = s.windowRunes / 2 // the default never conflicts with a small window
	}
	if s.overlapRunes > s.windowRunes/2 {
		return settings{}, errors.New("option window_overlap_runes: must be at most window_runes/2")
	}
	return s, nil
}

// decodeStrict rejects null, unknown fields and trailing data. Decoder errors
// are replaced because they can quote the value.
func decodeStrict(raw json.RawMessage, into any) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("must not be null")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return errors.New("invalid value")
	}
	if d.More() {
		return errors.New("invalid value")
	}
	return nil
}

func intIn(raw json.RawMessage, into *int, low, high int) error {
	if err := decodeStrict(raw, into); err != nil {
		return err
	}
	if *into < low || *into > high {
		return fmt.Errorf("must be %d-%d", low, high)
	}
	return nil
}

func runesIn(s string, low, high int) bool {
	n := utf8.RuneCountInString(s)
	return n >= low && n <= high
}

// Units keeps short segments whole and splits long ones into deterministic
// overlapping chunks, so the cache can remember each chunk of a long tool
// result separately.
func (c *systemone) Units(segments []string) []string {
	out := make([]string, 0, len(segments))
	for _, seg := range segments {
		if utf8.RuneCountInString(seg) <= c.s.windowRunes {
			out = append(out, seg)
			continue
		}
		out = append(out, splitRunes(seg, c.s.windowRunes, c.s.overlapRunes)...)
	}
	return out
}

// Classify reads at most maxWindows windows. A request with unread windows is
// Abstain even when every read window is clear (PR-08); units wholly inside
// read clear windows are still reported in Output.Clear.
func (c *systemone) Classify(ctx context.Context, in classifier.Input) (classifier.Output, error) {
	windows := buildWindows(in.Texts, c.s.windowRunes, c.s.overlapRunes)
	read := windows
	if len(read) > c.s.maxWindows {
		read = windows[:c.s.maxWindows]
	}
	clear := make([]bool, len(in.Texts))
	for i := range clear {
		clear[i] = true
	}
	if len(read) == 0 {
		return classifier.Output{Verdict: classifier.Clear, Clear: clear}, nil
	}
	probs, err := c.score(ctx, read)
	if err != nil {
		return classifier.Output{Windows: len(read)}, err
	}
	verdict := classifier.Clear
	for i, w := range windows {
		v := classifier.Abstain // unread
		if i < len(read) {
			v = c.judge(probs[i])
		}
		if v == classifier.Clear {
			continue
		}
		for _, p := range w.parts {
			clear[p] = false
		}
		if v == classifier.Flagged || verdict == classifier.Clear {
			verdict = v
		}
	}
	return classifier.Output{Verdict: verdict, Clear: clear, Windows: len(read)}, nil
}

func (c *systemone) judge(p float64) classifier.Verdict {
	if c.s.band != nil && c.s.band.low < p && p < c.s.band.high {
		return classifier.Abstain
	}
	if p >= c.s.threshold {
		return classifier.Flagged
	}
	return classifier.Clear
}
```

The `if v == classifier.Flagged || verdict == classifier.Clear` line keeps Flagged sticky. An Abstain window sets the verdict only while it is still Clear, and a later Flagged window always wins.

- [ ] **Step 4: Implement `client.go`**

```go
package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"sync"

	"github.com/inferplane/inferplane/internal/classifier"
)

type question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

type answer struct {
	Answers map[string]struct {
		Type string   `json:"type"`
		Noul *float64 `json:"noul"`
	} `json:"answers"`
}

func (a answer) probability() (float64, bool) {
	ans, ok := a.Answers[questionID]
	if !ok || ans.Type != "noul" || ans.Noul == nil {
		return 0, false
	}
	p := *ans.Noul
	if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
		return 0, false
	}
	return p, true
}

// score returns one probability per window, in order.
func (c *systemone) score(ctx context.Context, windows []window) ([]float64, error) {
	q := map[string]question{questionID: {Type: "noul", Instructions: c.s.instructions, Criteria: c.s.criteria}}
	if c.s.batch {
		states := make([]string, len(windows))
		for i, w := range windows {
			states[i] = w.text
		}
		body := map[string]any{"states": states, "questions": q}
		if c.s.model != "" {
			body["model"] = c.s.model
		}
		var resp struct {
			Results []answer `json:"results"`
		}
		if err := c.post(ctx, "/v1/systemone/batch", body, &resp); err != nil {
			return nil, err
		}
		if len(resp.Results) != len(windows) {
			return nil, classifier.ErrDecode
		}
		out := make([]float64, len(windows))
		for i, a := range resp.Results {
			p, ok := a.probability()
			if !ok {
				return nil, classifier.ErrDecode
			}
			out[i] = p
		}
		return out, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make([]float64, len(windows))
	sem := make(chan struct{}, c.s.concurrency)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	fail := func(err error) { once.Do(func() { first = err; cancel() }) }
	for i := range windows {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				fail(classifier.ErrTimeout)
				return
			}
			defer func() { <-sem }()
			body := map[string]any{"state": windows[i].text, "questions": q}
			if c.s.model != "" {
				body["model"] = c.s.model
			}
			var a answer
			if err := c.post(ctx, "/v1/systemone", body, &a); err != nil {
				fail(err)
				return
			}
			p, ok := a.probability()
			if !ok {
				fail(classifier.ErrDecode)
				return
			}
			out[i] = p
		}(i)
	}
	wg.Wait()
	if first != nil {
		return nil, first
	}
	return out, nil
}

// post sends one JSON request and decodes a bounded JSON response. Every
// failure maps to a bounded error class.
func (c *systemone) post(ctx context.Context, path string, body, into any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return classifier.ErrDecode
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+path, bytes.NewReader(payload))
	if err != nil {
		return classifier.ErrTransport
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return classifier.ErrTimeout
		}
		return classifier.ErrTransport
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return classifier.ErrTimeout
		}
		return classifier.ErrTransport
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return classifier.ErrStatus
	}
	if len(raw) > maxResponseBytes || json.Unmarshal(raw, into) != nil {
		return classifier.ErrDecode
	}
	return nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./plugins/systemone/ -race -count=2 -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w plugins/systemone && test -z "$(gofmt -l plugins/systemone)" && go vet ./plugins/systemone/
git add plugins/systemone
git commit -s -m "feat(systemone): Laya/strands-decider classifier over the /v1/systemone wire" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 6: `pii_classification` config and the per-generation Set

**Files:**
- Create: `internal/config/piiclassification.go`, `internal/live/classifiers.go`
- Modify:
  - `internal/config/config.go`: the `Config` struct (L510-563), the probe struct in `LoadRaw` (L721-735), the probe checks (after the `virtual_keys` loop, ~L765) and the validation chain (before `return &cfg, nil`, L833);
  - `internal/live/live.go`: the package doc (L1-9), `State` (L42-59) and `BuildStateWith` (after `st.fallbackFamily = ...`, ~L404);
  - `cmd/mayu/main.go`: blank imports (L16-21).
- Test: `internal/config/piiclassification_test.go`, `internal/live/classifiers_test.go`

**Interfaces:**
- Consumes: Task 1 (`classifier.New`, `classifier.Config`, `classifier.Entry`, `classifier.NewSet`, `classifier.SettingsDigest`); `config.SecretRef`, `ValidateSecretRef`, `ResolveSecretRef`.
- Produces:
  - `config.PIIClassificationConfig{TimeoutMS, MaxInputBytes int; Cache *ClassifierCacheConfig; Classifiers []ClassifierConfig}` with methods `Timeout() time.Duration`, `InputCap() int`, `CacheEntries() int`, `CacheTTL() time.Duration`.
  - `config.ClassifierCacheConfig{MaxEntries *int; TTLSeconds int}`.
  - `config.ClassifierConfig{Name, Type, Mode, Revision string; Teams []string; Endpoint, DataBoundary string; APIKeyRef *SecretRef; Threshold *float64; Options map[string]json.RawMessage}` with method `EffectiveThreshold() float64`.
  - `Config.PIIClassification *PIIClassificationConfig` (json `pii_classification`).
  - `(*live.State).Classifiers() *classifier.Set`, nil-safe.

- [ ] **Step 1: Write the failing config tests** (`internal/config/piiclassification_test.go`)

```go
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadPII(t *testing.T, block string) (*Config, error) {
	t.Helper()
	body := `{"providers":{"p":{"type":"anthropic"}}`
	if block != "" {
		body += `,"pii_classification":` + block
	}
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(path, []byte(body+"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadRaw(path)
}

// entry returns one classifier object; a nil value removes a base field.
func entry(over map[string]any) string {
	e := map[string]any{"name": "laya-ko", "type": "systemone", "mode": "Shadow", "endpoint": "http://127.0.0.1:8095", "data_boundary": "internal"}
	for k, v := range over {
		if v == nil {
			delete(e, k)
		} else {
			e[k] = v
		}
	}
	b, _ := json.Marshal(e)
	return string(b)
}

func classifiers(entries ...string) string {
	return `{"classifiers":[` + strings.Join(entries, ",") + `]}`
}

func TestPIIClassificationAbsentIsNil(t *testing.T) {
	cfg, err := loadPII(t, "")
	if err != nil || cfg.PIIClassification != nil {
		t.Fatalf("%+v %v", cfg.PIIClassification, err)
	}
}

func TestPIIClassificationDefaults(t *testing.T) {
	cfg, err := loadPII(t, classifiers(entry(nil)))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.PIIClassification
	if p.Timeout() != 500*time.Millisecond || p.InputCap() != 2<<20 || p.CacheEntries() != 100_000 || p.CacheTTL() != time.Hour || p.Classifiers[0].EffectiveThreshold() != 0.5 {
		t.Fatalf("defaults: %v %d %d %v %v", p.Timeout(), p.InputCap(), p.CacheEntries(), p.CacheTTL(), p.Classifiers[0].EffectiveThreshold())
	}
	off, err := loadPII(t, `{"cache":{"max_entries":0},"classifiers":[`+entry(nil)+`]}`)
	if err != nil || off.PIIClassification.CacheEntries() != 0 {
		t.Fatalf("explicit max_entries 0 must disable the cache: %v", err)
	}
}

func TestPIIClassificationRejects(t *testing.T) {
	five := classifiers(entry(map[string]any{"name": "a"}), entry(map[string]any{"name": "b"}), entry(map[string]any{"name": "c"}), entry(map[string]any{"name": "d"}), entry(map[string]any{"name": "e"}))
	for block, want := range map[string]string{
		`{"classifiers":[]}`: "at least one",
		five:                 "at most 4",
		classifiers(entry(map[string]any{"name": "Laya"})):                                        "name",
		classifiers(entry(nil), entry(nil)):                                                       "duplicate",
		classifiers(entry(map[string]any{"type": nil})):                                           "type",
		classifiers(entry(map[string]any{"revision": "R1!"})):                                     "revision",
		classifiers(entry(map[string]any{"mode": "enforce"})):                                     "mode",
		classifiers(entry(map[string]any{"data_boundary": "external"})):                           "not supported",
		classifiers(entry(map[string]any{"data_boundary": nil})):                                  "data_boundary",
		classifiers(entry(map[string]any{"endpoint": "ftp://127.0.0.1"})):                         "endpoint",
		classifiers(entry(map[string]any{"endpoint": "http://127.0.0.1:1/?k=v"})):                 "query",
		classifiers(entry(map[string]any{"threshold": 1})):                                        "threshold",
		classifiers(entry(map[string]any{"teams": []string{""}})):                                 "teams",
		classifiers(entry(map[string]any{"api_key": "inline-value-123"})):                         "inline api_key",
		classifiers(entry(map[string]any{"api_key_ref": map[string]string{"env": "not a name"}})): "api_key_ref",
		classifiers(entry(map[string]any{"endpont": "http://127.0.0.1:1"})):                       "unknown field",
		`{"timeout_ms":30001,"classifiers":[` + entry(nil) + `]}`:                                 "timeout_ms",
		`{"max_input_bytes":100,"classifiers":[` + entry(nil) + `]}`:                              "max_input_bytes",
		`{"cache":{"max_entries":-1},"classifiers":[` + entry(nil) + `]}`:                         "max_entries",
		`{"cache":{"ttl_seconds":10},"classifiers":[` + entry(nil) + `]}`:                         "ttl_seconds",
		`{"cache":{"max_entries":10,"ttl":60},"classifiers":[` + entry(nil) + `]}`:                "unknown field",
	} {
		if _, err := loadPII(t, block); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("block %s: err = %v, want %q", block, err, want)
		}
	}
}

func TestPIIClassificationEndpointErrorsNeverEchoCredentials(t *testing.T) {
	_, err := loadPII(t, classifiers(entry(map[string]any{"endpoint": "http://user:hunter2-value@127.0.0.1:1"})))
	if err == nil || !strings.Contains(err.Error(), "userinfo") || strings.Contains(err.Error(), "hunter2-value") {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/ -run TestPIIClassification -v`
Expected: FAIL to compile (`cfg.PIIClassification undefined`).

- [ ] **Step 3: Implement `internal/config/piiclassification.go`**

```go
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"time"
)

// PIIClassificationConfig is the opt-in PII classifier block
// (docs/superpowers/specs/2026-10-16-systemone-korean-pii-sample-design.md).
// Absent means no classifier and no overhead. Zero numeric fields take the
// documented defaults through the accessor methods.
type PIIClassificationConfig struct {
	TimeoutMS     int                    `json:"timeout_ms,omitempty"`
	MaxInputBytes int                    `json:"max_input_bytes,omitempty"`
	Cache         *ClassifierCacheConfig `json:"cache,omitempty"`
	Classifiers   []ClassifierConfig     `json:"classifiers"`
}

// ClassifierCacheConfig bounds the verdict cache; max_entries 0 disables it.
type ClassifierCacheConfig struct {
	MaxEntries *int `json:"max_entries,omitempty"`
	TTLSeconds int  `json:"ttl_seconds,omitempty"`
}

// ClassifierConfig is one classifier entry. The token is a reference only.
type ClassifierConfig struct {
	Name         string                     `json:"name"`
	Type         string                     `json:"type"`
	Mode         string                     `json:"mode"`
	Revision     string                     `json:"revision,omitempty"`
	Teams        []string                   `json:"teams,omitempty"`
	Endpoint     string                     `json:"endpoint"`
	DataBoundary string                     `json:"data_boundary"`
	APIKeyRef    *SecretRef                 `json:"api_key_ref,omitempty"`
	Threshold    *float64                   `json:"threshold,omitempty"`
	Options      map[string]json.RawMessage `json:"options,omitempty"`
}

const maxClassifiers = 4

var (
	classifierName     = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	classifierRevision = regexp.MustCompile(`^[a-z0-9._-]{1,32}$`)
)

func (p *PIIClassificationConfig) Timeout() time.Duration {
	if p.TimeoutMS == 0 {
		return 500 * time.Millisecond
	}
	return time.Duration(p.TimeoutMS) * time.Millisecond
}

func (p *PIIClassificationConfig) InputCap() int {
	if p.MaxInputBytes == 0 {
		return 2 << 20
	}
	return p.MaxInputBytes
}

func (p *PIIClassificationConfig) CacheEntries() int {
	if p.Cache == nil || p.Cache.MaxEntries == nil {
		return 100_000
	}
	return *p.Cache.MaxEntries
}

func (p *PIIClassificationConfig) CacheTTL() time.Duration {
	if p.Cache == nil || p.Cache.TTLSeconds == 0 {
		return time.Hour
	}
	return time.Duration(p.Cache.TTLSeconds) * time.Second
}

func (c ClassifierConfig) EffectiveThreshold() float64 {
	if c.Threshold == nil {
		return 0.5
	}
	return *c.Threshold
}

// checkPIIClassificationRaw runs before the structured parse. It rejects an
// inline api_key and any unknown field, so a misspelled key can never silently
// widen a classifier's scope (LoadRaw ignores unknown keys elsewhere).
func checkPIIClassificationRaw(raw json.RawMessage) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var probe struct {
		Classifiers []map[string]json.RawMessage `json:"classifiers"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("config: pii_classification: %w", err)
	}
	for i, c := range probe.Classifiers {
		if _, bad := c["api_key"]; bad {
			return fmt.Errorf("config: pii_classification.classifiers[%d] has inline api_key; use api_key_ref (§7)", i)
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(new(PIIClassificationConfig)); err != nil {
		return fmt.Errorf("config: pii_classification: %w", err)
	}
	return nil
}

func validatePIIClassification(p *PIIClassificationConfig) error {
	if p == nil {
		return nil
	}
	if p.TimeoutMS < 0 || p.TimeoutMS > 30_000 {
		return errors.New("config: pii_classification.timeout_ms must be 1-30000")
	}
	if p.MaxInputBytes != 0 && (p.MaxInputBytes < 1<<10 || p.MaxInputBytes > 16<<20) {
		return errors.New("config: pii_classification.max_input_bytes must be 1024-16777216")
	}
	if p.Cache != nil {
		if m := p.Cache.MaxEntries; m != nil && (*m < 0 || *m > 1_000_000) {
			return errors.New("config: pii_classification.cache.max_entries must be 0-1000000")
		}
		if t := p.Cache.TTLSeconds; t != 0 && (t < 60 || t > 86_400) {
			return errors.New("config: pii_classification.cache.ttl_seconds must be 60-86400")
		}
	}
	if len(p.Classifiers) == 0 {
		return errors.New("config: pii_classification.classifiers must list at least one classifier")
	}
	if len(p.Classifiers) > maxClassifiers {
		return fmt.Errorf("config: pii_classification allows at most %d classifiers", maxClassifiers)
	}
	seen := make(map[string]bool, len(p.Classifiers))
	for i, c := range p.Classifiers {
		at := fmt.Sprintf("config: pii_classification.classifiers[%d]", i)
		switch {
		case !classifierName.MatchString(c.Name):
			return fmt.Errorf("%s: name must match [a-z0-9-]{1,32}", at)
		case seen[c.Name]:
			return fmt.Errorf("%s: duplicate name %q", at, c.Name)
		case c.Type == "":
			return fmt.Errorf("%s: type is required", at)
		case c.Revision != "" && !classifierRevision.MatchString(c.Revision):
			return fmt.Errorf("%s: revision must match [a-z0-9._-]{1,32}", at)
		case c.Mode != "Shadow" && c.Mode != "Enforce":
			return fmt.Errorf("%s: mode must be Shadow or Enforce", at)
		case c.DataBoundary == "external":
			return fmt.Errorf("%s: external classifiers are not supported in this version", at)
		case c.DataBoundary != "internal":
			return fmt.Errorf("%s: data_boundary must be internal", at)
		case c.Threshold != nil && !(*c.Threshold > 0 && *c.Threshold < 1):
			return fmt.Errorf("%s: threshold must be in (0,1)", at)
		}
		seen[c.Name] = true
		if err := validateClassifierEndpoint(c.Endpoint); err != nil {
			return fmt.Errorf("%s: %w", at, err)
		}
		if err := ValidateSecretRef(c.APIKeyRef); err != nil {
			return fmt.Errorf("%s.api_key_ref: %w", at, err)
		}
		for _, team := range c.Teams {
			if team == "" {
				return fmt.Errorf("%s: teams entries must be non-empty", at)
			}
		}
	}
	return nil
}

// validateClassifierEndpoint never echoes the URL: it may carry a credential.
func validateClassifierEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("endpoint must be an absolute http(s) URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("endpoint must not carry userinfo, a query or a fragment")
	}
	return nil
}
```

- [ ] **Step 4: Wire it into `config.go`**

In `Config` (after `ControlPlane`, before `BudgetTimezone`), add:

```go
	// PIIClassification opts in to PII classifiers that restrict routing
	// through sensitiveData rules. Absent means no classifier and no cost.
	PIIClassification *PIIClassificationConfig `json:"pii_classification,omitempty"`
```

In the `LoadRaw` probe struct, add the field:

```go
		PIIClassification json.RawMessage `json:"pii_classification"`
```

After the `virtual_keys` inline-key loop, add:

```go
	if err := checkPIIClassificationRaw(probe.PIIClassification); err != nil {
		return nil, err
	}
```

Immediately before the final `return &cfg, nil` of `LoadRaw`, add:

```go
	if err := validatePIIClassification(cfg.PIIClassification); err != nil {
		return nil, err
	}
```

- [ ] **Step 5: Run the config tests to verify they pass**

Run: `go test ./internal/config/ -race -v -run 'TestPIIClassification|TestLoad|TestExample'`
Expected: PASS.

- [ ] **Step 6: Write the failing live tests** (`internal/live/classifiers_test.go`)

```go
package live

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/classifier"
	"github.com/inferplane/inferplane/internal/config"
)

type liveTestClassifier struct{ cfg classifier.Config }

func (liveTestClassifier) Units(s []string) []string { return s }
func (liveTestClassifier) Classify(context.Context, classifier.Input) (classifier.Output, error) {
	return classifier.Output{}, nil
}

func init() {
	classifier.Register("live-test", func(c classifier.Config) (classifier.Classifier, error) {
		if c.Options["fail"] != nil {
			return nil, errors.New("live-test: bad option")
		}
		return liveTestClassifier{cfg: c}, nil
	})
}

func withClassifiers(entries ...config.ClassifierConfig) *config.Config {
	cfg := sampleConfig()
	cfg.PIIClassification = &config.PIIClassificationConfig{Classifiers: entries}
	return cfg
}

func liveEntry(name, mode string, teams ...string) config.ClassifierConfig {
	return config.ClassifierConfig{Name: name, Type: "live-test", Mode: mode, Teams: teams, Endpoint: "http://127.0.0.1:8095", DataBoundary: "internal"}
}

func TestBuildStateWithoutClassifiersHasNilSet(t *testing.T) {
	st, _, err := BuildState(sampleConfig())
	if err != nil || st.Classifiers() != nil {
		t.Fatalf("%v %v", st.Classifiers(), err)
	}
	var none *State
	if none.Classifiers() != nil {
		t.Fatal("nil State returned a Set")
	}
}

func TestBuildStateBuildsTheClassifierSet(t *testing.T) {
	t.Setenv("LIVE_TEST_CLASSIFIER_TOKEN", "token-value-123")
	scoped := liveEntry("scoped", "Enforce", "kr")
	scoped.APIKeyRef = &config.SecretRef{Env: "LIVE_TEST_CLASSIFIER_TOKEN"}
	st, _, err := BuildState(withClassifiers(liveEntry("global", "Shadow"), scoped))
	if err != nil {
		t.Fatal(err)
	}
	set := st.Classifiers()
	kr, other := set.For("kr"), set.For("us")
	if len(kr) != 2 || len(other) != 1 || other[0].Name != "global" || kr[1].Mode != classifier.Enforce {
		t.Fatalf("scoping kr=%+v us=%+v", kr, other)
	}
	built := kr[1].Classifier.(liveTestClassifier).cfg
	if kr[0].Settings == ([32]byte{}) || kr[1].Settings != classifier.SettingsDigest(built) {
		t.Fatal("entries do not carry the digest of their built config")
	}
	if built.BearerToken != "token-value-123" || built.Threshold != 0.5 || set.Timeout.Milliseconds() != 500 || set.MaxInputBytes != 2<<20 {
		t.Fatalf("built config %+v, set %+v", built, set)
	}
}

func TestBuildStateRejectsBadClassifiers(t *testing.T) {
	unknown := liveEntry("unknown", "Shadow")
	unknown.Type = "nope"
	badOption := liveEntry("bad", "Shadow")
	badOption.Options = map[string]json.RawMessage{"fail": json.RawMessage("true")}
	unset := liveEntry("unset", "Shadow")
	unset.APIKeyRef = &config.SecretRef{Env: "LIVE_TEST_CLASSIFIER_UNSET"}
	for name, tc := range map[string]struct {
		entry config.ClassifierConfig
		want  string
	}{
		"unknown type": {unknown, "unknown type"},
		"bad option":   {badOption, "bad option"},
		"unset token":  {unset, "LIVE_TEST_CLASSIFIER_UNSET"},
	} {
		if _, _, err := BuildState(withClassifiers(tc.entry)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestEveryBuildGetsAFreshSet(t *testing.T) {
	cfg := withClassifiers(liveEntry("global", "Shadow"))
	a, _, errA := BuildState(cfg)
	b, _, errB := BuildState(cfg)
	if errA != nil || errB != nil || a.Classifiers() == b.Classifiers() {
		t.Fatal("generations must not share a Set or its cache")
	}
}
```


- [ ] **Step 7: Run the live tests to verify they fail**

Run: `go test ./internal/live/ -run 'TestBuildState.*Classifier|TestEveryBuild' -v`
Expected: FAIL to compile (`st.Classifiers undefined`).

- [ ] **Step 8: Implement `internal/live/classifiers.go` and wire `State`**

```go
package live

import (
	"fmt"
	"log"

	"github.com/inferplane/inferplane/internal/classifier"
	"github.com/inferplane/inferplane/internal/config"
)

// buildClassifiers resolves the opt-in classifier block into this
// generation's Set, with a fresh verdict cache. nil config means no Set.
func buildClassifiers(p *config.PIIClassificationConfig) (*classifier.Set, error) {
	if p == nil {
		return nil, nil
	}
	entries := make([]classifier.Entry, 0, len(p.Classifiers))
	for _, cc := range p.Classifiers {
		token, err := config.ResolveSecretRef(cc.APIKeyRef)
		if err != nil {
			return nil, fmt.Errorf("live: classifier %q api_key_ref: %w", cc.Name, err)
		}
		cfg := classifier.Config{Name: cc.Name, Type: cc.Type, Revision: cc.Revision, Endpoint: cc.Endpoint,
			BearerToken: token, Threshold: cc.EffectiveThreshold(), Options: cc.Options}
		c, err := classifier.New(cfg)
		if err != nil {
			return nil, fmt.Errorf("live: %w", err)
		}
		e := classifier.Entry{Name: cc.Name, Revision: cc.Revision, Mode: classifier.Mode(cc.Mode), Classifier: c,
			Global: len(cc.Teams) == 0, Settings: classifier.SettingsDigest(cfg)}
		scope := "all teams"
		if !e.Global {
			e.Teams = make(map[string]bool, len(cc.Teams))
			for _, t := range cc.Teams {
				e.Teams[t] = true
			}
			scope = fmt.Sprintf("teams %v", cc.Teams)
		}
		entries = append(entries, e)
		log.Printf("inferplane: WARNING: PII classifier %q (%s, %s) sends request text to %s", cc.Name, cc.Mode, scope, cc.Endpoint)
	}
	return classifier.NewSet(entries, p.Timeout(), p.InputCap(), p.CacheEntries(), p.CacheTTL())
}

// Classifiers returns this generation's PII classifier Set; nil when none.
func (s *State) Classifiers() *classifier.Set {
	if s == nil {
		return nil
	}
	return s.classifiers
}
```

In `live.go`:
- add the field to `State` after `fallbackFamily`:

```go
	// classifiers is the opt-in PII classifier Set of this generation, with
	// its own verdict cache (set by BuildState; nil when not configured).
	classifiers *classifier.Set
```

- in `BuildStateWith`, after `st.fallbackFamily = cfg.FallbackFamilyEnabled()`:

```go
	set, err := buildClassifiers(cfg.PIIClassification)
	if err != nil {
		return nil, nil, err
	}
	st.classifiers = set
```

- add `"github.com/inferplane/inferplane/internal/classifier"` to the imports;
- change the package doc's import sentence to say that live imports only config, providers, pricing and the classifier leaf.

In `cmd/mayu/main.go`, add to the blank imports:

```go
	_ "github.com/inferplane/inferplane/plugins/systemone" // register "systemone" PII classifier
```

- [ ] **Step 9: Run the tests to verify they pass**

Run: `go test ./internal/config/ ./internal/live/ -race && go build ./cmd/mayu`
Expected: PASS; `TestLiveImportsAreLeafSafe` still passes, because `internal/classifier` is not on its banned list.

- [ ] **Step 10: Commit**

```bash
gofmt -w internal/config internal/live cmd/mayu && test -z "$(gofmt -l internal/config internal/live cmd/mayu)" && go vet ./internal/config/ ./internal/live/ ./cmd/mayu/
git add internal/config/piiclassification.go internal/config/piiclassification_test.go internal/config/config.go internal/live/classifiers.go internal/live/classifiers_test.go internal/live/live.go cmd/mayu/main.go
git commit -s -m "feat(config): opt-in pii_classification block built into each live generation" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 7: Classifier metrics and audit DTO

**Files:**
- Create: `internal/metrics/classifier.go`, `internal/metrics/classifier_test.go`
- Modify:
  - `internal/metrics/metrics.go`: the `Metrics` struct, and `New()` after `reg.MustRegister(...)`;
  - `internal/metrics/metrics_test.go`: `TestNilMetricsNoPanic`;
  - `internal/metrics/cardinality_test.go`: `TestNoMetricCarriesAUserLabel`;
  - `internal/audit/routing.go`: `RoutingRef`, `cloneRouting`;
  - `internal/audit/routing_test.go`.

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `(*metrics.Metrics).ObservePIIClassifier(name, revision, mode, result string, seconds float64, windows, hits, misses int)`;
  - `(*metrics.Metrics).RetainPIIClassifiers(current map[string]string)`;
  - `audit.ClassifierRef{Name, Revision, Boundary, Mode, Result string; WouldRestrict bool}`;
  - `audit.RoutingRef.Classifiers []ClassifierRef` (json `classifiers,omitempty`, the last field).

- [ ] **Step 1: Write the failing tests**

`internal/metrics/classifier_test.go`:

```go
package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPIIClassifierLabelsAreBounded(t *testing.T) {
	m := New()
	m.ObservePIIClassifier("laya-ko", "r1", "enforce-ish", "maybe", 0.1, 3, 2, 1)
	if got := testutil.ToFloat64(m.classifiers.decisions.WithLabelValues("laya-ko", "r1", "none", "unknown")); got != 1 {
		t.Fatalf("clamped decision count = %v", got)
	}
	if n := testutil.CollectAndCount(m.classifiers.cache); n != 2 {
		t.Fatalf("cache series = %d, want hit and miss", n)
	}
}

// Review Focus 4: a process exports one revision per classifier after a swap.
func TestRetainPIIClassifiersDeletesReplacedSeries(t *testing.T) {
	m := New()
	m.RetainPIIClassifiers(map[string]string{"laya-ko": "r1", "old": "r1"})
	m.ObservePIIClassifier("laya-ko", "r1", "Shadow", "clear", 0.1, 1, 1, 1)
	m.ObservePIIClassifier("old", "r1", "Shadow", "clear", 0.1, 1, 1, 1)
	m.RetainPIIClassifiers(map[string]string{"laya-ko": "r2"})
	if n := testutil.CollectAndCount(m.classifiers.decisions); n != 0 {
		t.Fatalf("%d decision series survived the swap", n)
	}
	if n := testutil.CollectAndCount(m.classifiers.duration); n != 0 {
		t.Fatalf("%d duration series survived the swap", n)
	}
	if n := testutil.CollectAndCount(m.classifiers.windows); n != 1 {
		t.Fatalf("windows series = %d, want the kept classifier only", n)
	}
	if n := testutil.CollectAndCount(m.classifiers.cache); n != 2 {
		t.Fatalf("cache series = %d, want the kept classifier's hit and miss", n)
	}
}
```

In `TestNilMetricsNoPanic` (`metrics_test.go`), before `// no panic = pass`, add:

```go
	m.ObservePIIClassifier("c", "r", "Shadow", "clear", 0, 1, 1, 1)
	m.RetainPIIClassifiers(nil)
```

In `TestNoMetricCarriesAUserLabel` (`cardinality_test.go`), after `m.ObserveRoutingDecision(...)`, add:

```go
	m.ObservePIIClassifier("laya-ko", "r1", "Shadow", "flagged", 0.1, 2, 1, 1)
```

In `internal/audit/routing_test.go`, add `"bytes"` to the imports (`context` and `encoding/json` are already there), and add:

```go
func TestRoutingClassifierRefsAreAppendOnlyAndCopied(t *testing.T) {
	ref := &RoutingRef{RequestedModel: "m", Reason: "unchanged", Inspection: "complete",
		Classifiers: []ClassifierRef{{Name: "laya-ko", Boundary: "internal", Mode: "Shadow", Result: "flagged", WouldRestrict: true}}}
	b, err := json.Marshal(ref)
	if err != nil || !bytes.HasSuffix(b, []byte(`,"classifiers":[{"name":"laya-ko","boundary":"internal","mode":"Shadow","result":"flagged","would_restrict":true}]}`)) {
		t.Fatalf("%s %v", b, err)
	}
	ctx := WithRouting(context.Background(), ref)
	ref.Classifiers[0].Result = "clear"
	if RoutingFrom(ctx).Classifiers[0].Result != "flagged" {
		t.Fatal("classifier refs were not copied")
	}
	legacy, _ := json.Marshal(&RoutingRef{RequestedModel: "m", Reason: "unchanged", Inspection: "complete"})
	if bytes.Contains(legacy, []byte("classifiers")) {
		t.Fatal("an empty classifier list was serialized")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/metrics/ ./internal/audit/ -run 'PIIClassifier|NilMetrics|UserLabel|ClassifierRefs' -v`
Expected: FAIL to compile (`m.ObservePIIClassifier undefined`, `unknown field Classifiers`).

- [ ] **Step 3: Implement `internal/metrics/classifier.go`**

```go
package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// classifierMetrics are the PII classifier collectors. Labels come only from
// validated config (classifier name, revision) and bounded enums.
type classifierMetrics struct {
	decisions *prometheus.CounterVec   // inferplane_pii_classifier_decisions_total
	duration  *prometheus.HistogramVec // inferplane_pii_classifier_duration_seconds
	windows   *prometheus.HistogramVec // inferplane_pii_classifier_windows
	cache     *prometheus.CounterVec   // inferplane_pii_classifier_cache_segments_total
	mu        sync.Mutex
	current   map[string]string // name → revision of the live generation
}

func newClassifierMetrics(reg *prometheus.Registry) *classifierMetrics {
	c := &classifierMetrics{
		decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "inferplane_pii_classifier_decisions_total", Help: "PII classifier results by bounded mode and result.",
		}, []string{"classifier", "revision", "mode", "result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "inferplane_pii_classifier_duration_seconds", Help: "PII classifier time per request, including cache lookups.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}, []string{"classifier", "revision", "result"}),
		windows: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "inferplane_pii_classifier_windows", Help: "Windows sent to a PII classifier per request; the CPU-versus-GPU sizing signal.",
			Buckets: []float64{1, 2, 4, 8, 16, 32, 64},
		}, []string{"classifier"}),
		cache: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "inferplane_pii_classifier_cache_segments_total", Help: "Verdict-cache lookups of classifier units by hit or miss.",
		}, []string{"classifier", "result"}),
		current: map[string]string{},
	}
	reg.MustRegister(c.decisions, c.duration, c.windows, c.cache)
	return c
}

// ObservePIIClassifier records one entry's result. mode and result are
// clamped to their enums; name and revision are validated config values.
func (m *Metrics) ObservePIIClassifier(name, revision, mode, result string, seconds float64, windows, hits, misses int) {
	if m == nil || m.classifiers == nil {
		return
	}
	switch mode {
	case "Shadow", "Enforce":
	default:
		mode = "none"
	}
	switch result {
	case "flagged", "clear", "abstain", "error", "skipped":
	default:
		result = "unknown"
	}
	c := m.classifiers
	c.decisions.WithLabelValues(name, revision, mode, result).Inc()
	c.duration.WithLabelValues(name, revision, result).Observe(seconds)
	if windows > 0 {
		c.windows.WithLabelValues(name).Observe(float64(windows))
	}
	if hits > 0 {
		c.cache.WithLabelValues(name, "hit").Add(float64(hits))
	}
	if misses > 0 {
		c.cache.WithLabelValues(name, "miss").Add(float64(misses))
	}
}

// RetainPIIClassifiers runs after each generation swap with the new
// generation's name → revision map. It deletes the series of removed
// classifiers and of replaced revisions, so a process exports one revision
// per classifier. A request still finishing on the previous generation can
// re-create an old series until the next swap.
func (m *Metrics) RetainPIIClassifiers(current map[string]string) {
	if m == nil || m.classifiers == nil {
		return
	}
	c := m.classifiers
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, rev := range c.current {
		next, kept := current[name]
		if kept && next == rev {
			continue
		}
		c.decisions.DeletePartialMatch(prometheus.Labels{"classifier": name, "revision": rev})
		c.duration.DeletePartialMatch(prometheus.Labels{"classifier": name, "revision": rev})
		if !kept {
			c.windows.DeletePartialMatch(prometheus.Labels{"classifier": name})
			c.cache.DeletePartialMatch(prometheus.Labels{"classifier": name})
		}
	}
	c.current = make(map[string]string, len(current))
	for name, rev := range current {
		c.current[name] = rev
	}
}
```

In `metrics.go`, add to the `Metrics` struct after `routingDecisions`:

```go
	classifiers *classifierMetrics // PII classifier collectors (classifier.go)
```

In `New()`, directly after the `reg.MustRegister(...)` call, add:

```go
	m.classifiers = newClassifierMetrics(reg)
```

- [ ] **Step 4: Implement the audit DTO**

In `internal/audit/routing.go`, append as the last field of `RoutingRef`, after `Masked`:

```go
	Classifiers     []ClassifierRef         `json:"classifiers,omitempty"`
```

Add the type after `RoutingRecommendation`:

```go
// ClassifierRef is the bounded evidence of one PII classifier entry. It never
// carries text, windows, probabilities, digests, endpoints or tokens.
type ClassifierRef struct {
	Name          string `json:"name"`
	Revision      string `json:"revision,omitempty"`
	Boundary      string `json:"boundary"`
	Mode          string `json:"mode"`
	Result        string `json:"result"`
	WouldRestrict bool   `json:"would_restrict,omitempty"`
}
```

In `cloneRouting`, add `c.Classifiers = slices.Clone(r.Classifiers)`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/metrics/ ./internal/audit/ -race -v`
Expected: PASS, including `TestRoutingLegacyBytesAndMixedChain` and `TestRoutingDTOContainsOnlyScalarMetadata` unchanged.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/metrics internal/audit && test -z "$(gofmt -l internal/metrics internal/audit)" && go vet ./internal/metrics/ ./internal/audit/
git add internal/metrics internal/audit/routing.go internal/audit/routing_test.go
git commit -s -m "feat(metrics,audit): bounded PII classifier collectors and append-only routing evidence" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 8: Routing integration, evidence projection and metric retention

**Files:**
- Create:
  - `internal/router/classify.go`
  - `internal/router/request_routing_classifier_test.go`
  - `internal/server/requestpolicy/classifier_evidence_test.go`
  - `cmd/mayu/classifier_reload_test.go`
- Modify:
  - `internal/router/request_routing.go`: `RoutingDecision` (L88-100), the privacy section of `RouteRequest` (L250-275), `boundedCategories` (L854-864);
  - `internal/server/requestpolicy/evidence.go`: `Observe`, after the recommendations loop;
  - `cmd/mayu/gateway.go`: after `holder.Swap(st)` (L391), `g.holder.Swap(st)` (L1214) and `g.holder.Swap(st)` (L1295), plus a helper.

**Interfaces:**
- Consumes:
  - Task 1/2: `Set.For`, `Set.Run`, `classifier.Combine`, `classifier.Failed`, `classifier.PolicyRef`, `classifier.Request`, `classifier.Result`, outcomes;
  - Task 3: `sensitivity.Segments`;
  - Task 6: `(*live.State).Classifiers()`, `config.PIIClassificationConfig`;
  - Task 7: `(*metrics.Metrics).ObservePIIClassifier`, `RetainPIIClassifiers`, `audit.ClassifierRef`.
- Produces:
  - `router.ClassifierRef{Name, Revision, Boundary, Mode, Result string; WouldRestrict bool}`;
  - `RoutingDecision.Classifiers []ClassifierRef` (json `classifiers,omitempty`);
  - the category `model_pii`.

- [ ] **Step 1: Write the failing router tests** (`internal/router/request_routing_classifier_test.go`)

```go
package router

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/classifier"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/live"
)

var routerFakes sync.Map // classifier name → *routerFake, read at State build time

type routerFake struct {
	mu     sync.Mutex
	inputs [][]string
	decide func([]string) (classifier.Verdict, error)
}

func (f *routerFake) Units(s []string) []string { return s }

func (f *routerFake) Classify(_ context.Context, in classifier.Input) (classifier.Output, error) {
	f.mu.Lock()
	f.inputs = append(f.inputs, slices.Clone(in.Texts))
	f.mu.Unlock()
	v, err := f.decide(in.Texts)
	if err != nil {
		return classifier.Output{}, err
	}
	clear := make([]bool, len(in.Texts))
	for i := range clear {
		clear[i] = v == classifier.Clear
	}
	return classifier.Output{Verdict: v, Clear: clear, Windows: 1}, nil
}

func (f *routerFake) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inputs)
}

func init() {
	classifier.Register("router-test", func(c classifier.Config) (classifier.Classifier, error) {
		f, ok := routerFakes.Load(c.Name)
		if !ok {
			return nil, fmt.Errorf("no fake %q", c.Name)
		}
		return f.(*routerFake), nil
	})
}

func always(v classifier.Verdict) func([]string) (classifier.Verdict, error) {
	return func([]string) (classifier.Verdict, error) { return v, nil }
}

func classifierRouting(mode string, teams ...string) *config.Config {
	cfg := routingConfig()
	cfg.PIIClassification = &config.PIIClassificationConfig{Classifiers: []config.ClassifierConfig{{
		Name: "fake", Type: "router-test", Mode: mode, Teams: teams, Endpoint: "http://127.0.0.1:1", DataBoundary: "internal",
	}}}
	return cfg
}

func classifierSetup(t *testing.T, mode string, decide func([]string) (classifier.Verdict, error), teams ...string) (*Router, RequestRoutingInput, *routerFake) {
	t.Helper()
	f := &routerFake{decide: decide}
	routerFakes.Store("fake", f)
	r, in := routingSetup(t, classifierRouting(mode, teams...))
	return r, in, f
}

func TestClassifierFlaggedRestrictsToInternalModels(t *testing.T) {
	r, in, _ := classifierSetup(t, "Enforce", always(classifier.Flagged))
	installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
	in.AllowedRegions = []string{"eu"}
	got, err := r.RouteRequest(context.Background(), in)
	if err != nil || got.Model != "private" || got.Decision.Privacy != "internal_only" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, ct := range got.Chain {
		if ct.DataBoundary != "internal" {
			t.Fatalf("external target in chain: %+v", ct)
		}
	}
	want := []ClassifierRef{{Name: "fake", Boundary: "internal", Mode: "Enforce", Result: "flagged"}}
	if !slices.Contains(got.Decision.Categories, "model_pii") || !reflect.DeepEqual(got.Decision.Classifiers, want) {
		t.Fatalf("evidence %v %+v", got.Decision.Categories, got.Decision.Classifiers)
	}
}

func TestClassifierShadowNeverChangesRouting(t *testing.T) {
	r0, in0 := routingSetup(t, routingConfig())
	installRoutingPolicies(r0, privatePolicy("privacy", "private-alias"))
	want, err := r0.RouteRequest(context.Background(), in0)
	if err != nil {
		t.Fatal(err)
	}
	r, in, f := classifierSetup(t, "Shadow", always(classifier.Flagged))
	installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
	got, err := r.RouteRequest(context.Background(), in)
	if err != nil || got.Model != want.Model || len(got.Chain) != len(want.Chain) || got.Decision.Reason != want.Decision.Reason || f.calls() != 1 {
		t.Fatalf("Shadow changed routing: %+v %v", got, err)
	}
	if slices.Contains(got.Decision.Categories, "model_pii") || len(got.Decision.Classifiers) != 1 || !got.Decision.Classifiers[0].WouldRestrict {
		t.Fatalf("Shadow evidence %v %+v", got.Decision.Categories, got.Decision.Classifiers)
	}
}

// Review Focus 3: an unavailable or uncertain classifier fails closed in Enforce.
func TestClassifierErrorAppliesOnUninspectable(t *testing.T) {
	fail := func([]string) (classifier.Verdict, error) { return 0, classifier.ErrTransport }
	for name, decide := range map[string]func([]string) (classifier.Verdict, error){"error": fail, "abstain": always(classifier.Abstain)} {
		t.Run(name+"/block", func(t *testing.T) {
			r, in, _ := classifierSetup(t, "Enforce", decide)
			p := privatePolicy("privacy", "private-alias")
			p.Rules[0].SensitiveData.OnUninspectable = v1alpha1.Block
			installRoutingPolicies(r, p)
			got, err := r.RouteRequest(context.Background(), in)
			requireDenied(t, got, err, "sensitive_blocked")
			if got.Decision.Classifiers[0].Result != name {
				t.Fatalf("result %+v", got.Decision.Classifiers)
			}
		})
		t.Run(name+"/internal", func(t *testing.T) {
			r, in, _ := classifierSetup(t, "Enforce", decide)
			installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
			in.AllowedRegions = []string{"eu"}
			got, err := r.RouteRequest(context.Background(), in)
			if err != nil || got.Model != "private" {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestClassifierFlaggedUnderMaskRuleUsesOnUninspectable(t *testing.T) {
	r, in, _ := classifierSetup(t, "Enforce", always(classifier.Flagged))
	installRoutingPolicies(r, maskPolicy()) // onDetected Mask, onUninspectable Block
	calls := 0
	setAdaptiveRedactor(t, r, func(_ context.Context, _ string, raw []byte) ([]byte, error) { calls++; return raw, nil })
	got, err := r.RouteRequest(context.Background(), in)
	requireDenied(t, got, err, "sensitive_blocked")
	if calls != 0 {
		t.Fatal("a Block obligation must win before masking")
	}

	r2, in2, _ := classifierSetup(t, "Enforce", always(classifier.Flagged))
	p := privatePolicy("mask", "private")
	p.Rules[0].SensitiveData.OnDetected = v1alpha1.Mask // onUninspectable stays InternalOnly
	installRoutingPolicies(r2, p)
	protectedInput(&in2)
	in2.AllowedRegions = []string{"eu"}
	setAdaptiveRedactor(t, r2, func(_ context.Context, _ string, raw []byte) ([]byte, error) {
		return bytes.ReplaceAll(raw, []byte("person@example.test"), []byte("REDACTED")), nil
	})
	got2, err := r2.RouteRequest(context.Background(), in2)
	if err != nil || got2.Model != "private" || !got2.Decision.Masked || bytes.Contains(got2.SanitizedBody, []byte("person@example.test")) {
		t.Fatalf("regex Mask plus classifier flag: %+v %v", got2, err)
	}
}

// Review Focus 5: no call, no latency outside the classifier's scope.
func TestClassifierNotCalledOutsideScope(t *testing.T) {
	t.Run("other team", func(t *testing.T) {
		r, in, f := classifierSetup(t, "Enforce", always(classifier.Flagged), "other")
		installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
		got, err := r.RouteRequest(context.Background(), in)
		if err != nil || f.calls() != 0 || got.Decision.Classifiers != nil {
			t.Fatalf("%+v %v calls=%d", got, err, f.calls())
		}
	})
	t.Run("no privacy rule", func(t *testing.T) {
		r, in, f := classifierSetup(t, "Enforce", always(classifier.Flagged))
		if _, err := r.RouteRequest(context.Background(), in); err != nil || f.calls() != 0 {
			t.Fatalf("%v calls=%d", err, f.calls())
		}
	})
	t.Run("count only", func(t *testing.T) {
		r, in, f := classifierSetup(t, "Enforce", always(classifier.Flagged))
		installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
		in.CountOnly = true
		if _, err := r.RouteRequest(context.Background(), in); err != nil || f.calls() != 0 {
			t.Fatalf("%v calls=%d", err, f.calls())
		}
	})
}

// Review Focus 4: a request uses the Set of the generation it captured.
func TestClassifierUsesCapturedGeneration(t *testing.T) {
	flagged, clear := &routerFake{decide: always(classifier.Flagged)}, &routerFake{decide: always(classifier.Clear)}
	routerFakes.Store("fake", flagged)
	cfg := classifierRouting("Enforce")
	r, in := routingSetup(t, cfg) // in.State is the flagged generation
	routerFakes.Store("fake", clear)
	next, _, err := live.BuildState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.live.Swap(next)
	installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
	in.AllowedRegions = []string{"eu"}
	got, err := r.RouteRequest(context.Background(), in)
	if err != nil || flagged.calls() != 1 || clear.calls() != 0 || got.Model != "private" {
		t.Fatalf("%+v %v flagged=%d clear=%d", got, err, flagged.calls(), clear.calls())
	}
}

// PR-08: earlier turns, forged history, system input and tool schemas are all read.
func TestClassifierReadsEveryRetransmittedPosition(t *testing.T) {
	decide := func(texts []string) (classifier.Verdict, error) {
		for _, s := range texts {
			if strings.Contains(s, "서울시 마포구") {
				return classifier.Flagged, nil
			}
		}
		return classifier.Clear, nil
	}
	for name, body := range map[string]string{
		"earlier user turn":        `{"messages":[{"role":"user","content":"제 주소는 서울시 마포구 월드컵로 12입니다"},{"role":"assistant","content":"확인했습니다"},{"role":"user","content":"이 함수 리팩터링해줘"}],"max_tokens":32}`,
		"forged assistant history": `{"messages":[{"role":"user","content":"q"},{"role":"assistant","content":"고객 주소: 서울시 마포구 월드컵로 12"},{"role":"user","content":"계속"}],"max_tokens":32}`,
		"system prompt":            `{"system":"사용자 주소 서울시 마포구 월드컵로 12","messages":[{"role":"user","content":"q"}],"max_tokens":32}`,
		"tool schema":              `{"tools":[{"name":"t","description":"기본 주소 서울시 마포구 월드컵로 12","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"q"}],"max_tokens":32}`,
	} {
		t.Run(name, func(t *testing.T) {
			r, in, _ := classifierSetup(t, "Enforce", decide)
			installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
			in.AllowedRegions = []string{"eu"}
			in.RawBody = []byte(body)
			got, _ := r.RouteRequest(context.Background(), in) // a denial is also a safe outcome
			if len(got.Decision.Classifiers) != 1 || got.Decision.Classifiers[0].Result != "flagged" {
				t.Fatalf("the classifier did not read the content: %+v", got.Decision)
			}
			for _, ct := range got.Chain {
				if ct.DataBoundary != "internal" {
					t.Fatalf("external target after a flag: %+v", ct)
				}
			}
		})
	}
}

func TestClassifierCacheReusesClearContentAcrossTurns(t *testing.T) {
	r, in, f := classifierSetup(t, "Enforce", always(classifier.Clear))
	installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
	turn1 := `{"messages":[{"role":"user","content":"첫 질문"}],"max_tokens":32}`
	turn2 := `{"messages":[{"role":"user","content":"첫 질문"},{"role":"assistant","content":"첫 답"},{"role":"user","content":"둘째 질문"}],"max_tokens":32}`
	for _, body := range []string{turn1, turn2} {
		in.RawBody = []byte(body)
		if _, err := r.RouteRequest(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	if second := f.inputs[1]; slices.Contains(second, "첫 질문") || !slices.Contains(second, "둘째 질문") {
		t.Fatalf("second turn sent %q", second)
	}
	next := privatePolicy("privacy", "private-alias")
	next.Generation = 8 // a policy change reads everything again
	installRoutingPolicies(r, next)
	if _, err := r.RouteRequest(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.inputs[2], "첫 질문") {
		t.Fatalf("a new policy generation reused cached verdicts: %q", f.inputs[2])
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/router/ -run TestClassifier -v`
Expected: FAIL to compile (`undefined: ClassifierRef`).

- [ ] **Step 3: Implement `internal/router/classify.go`**

```go
package router

import (
	"context"

	"github.com/inferplane/inferplane/internal/classifier"
	"github.com/inferplane/inferplane/internal/sensitivity"
)

// ClassifierRef is the bounded routing evidence of one PII classifier entry.
type ClassifierRef struct {
	Name          string `json:"name"`
	Revision      string `json:"revision,omitempty"`
	Boundary      string `json:"boundary"`
	Mode          string `json:"mode"`
	Result        string `json:"result"`
	WouldRestrict bool   `json:"would_restrict,omitempty"`
}

type classifierSignal struct{ classified, uninspectable bool }

// classify runs the captured generation's classifiers for the principal's
// team over every text the regex inspector reads. It is called only for
// privacy-governed requests that are not count-only. Only Enforce entries
// contribute to the returned signal.
func (r *Router) classify(ctx context.Context, in RequestRoutingInput, rules []requestRule, out *RequestRoutingResult) classifierSignal {
	set := in.State.Classifiers()
	entries := set.For(in.Principal.Team)
	if len(entries) == 0 {
		return classifierSignal{}
	}
	var policies []classifier.PolicyRef
	for _, rule := range rules {
		if rule.sensitive != nil {
			policies = append(policies, classifier.PolicyRef{Name: rule.ref.Name, Generation: rule.ref.Generation, Rule: rule.ref.Rule})
		}
	}
	var results []classifier.Result
	if segments, err := sensitivity.Segments(ctx, in.Protocol, in.RawBody); err != nil {
		results = classifier.Failed(entries)
	} else {
		results = set.Run(ctx, entries, classifier.Request{Team: in.Principal.Team, Policies: policies, Segments: segments})
	}
	for _, res := range results {
		ref := ClassifierRef{Name: res.Name, Revision: res.Revision, Boundary: "internal", Mode: string(res.Mode), Result: string(res.Outcome)}
		if res.Mode == classifier.Shadow {
			switch res.Outcome {
			case classifier.OutcomeFlagged, classifier.OutcomeAbstain, classifier.OutcomeError:
				ref.WouldRestrict = true
			}
		}
		out.Decision.Classifiers = append(out.Decision.Classifiers, ref)
		r.metrics.ObservePIIClassifier(res.Name, res.Revision, string(res.Mode), string(res.Outcome), res.Duration.Seconds(), res.Windows, res.CacheHits, res.CacheMisses)
	}
	classified, uninspectable := classifier.Combine(results)
	return classifierSignal{classified: classified, uninspectable: uninspectable}
}
```

- [ ] **Step 4: Wire it into `request_routing.go`**

Add the last field of `RoutingDecision`, after `Masked`:

```go
	Classifiers     []ClassifierRef         `json:"classifiers,omitempty"`
```

In `RouteRequest`, make three changes:
- directly after the block `if !hasPrivacy && !strictBudget { if in.CountOnly { return out, nil }; return r.applyContext(...), nil }` and before the comment `// All triggered privacy rules apply`, insert:

```go
	var signal classifierSignal
	if hasPrivacy && !in.CountOnly {
		signal = r.classify(ctx, in, rules, &out)
		if signal.classified {
			out.Decision.Categories = boundedCategories(append(slices.Clone(inspected.Categories), "model_pii"))
		}
	}
```

- inside the rule loop, compute the classified action before the trigger loop:

```go
		classifiedAction := sd.OnDetected
		if classifiedAction == v1alpha1.Mask {
			classifiedAction = sd.OnUninspectable // a verdict gives Mask nothing to redact
		}
```

- extend the trigger list from
  `{len(inspected.Categories) > 0, sd.OnDetected}, {!inspected.Complete, sd.OnUninspectable},`
  to

```go
			{len(inspected.Categories) > 0, sd.OnDetected}, {!inspected.Complete, sd.OnUninspectable},
			{signal.classified, classifiedAction}, {signal.uninspectable, sd.OnUninspectable},
```

The existing `case v1alpha1.Mask: if index == 0 { MaskRequired } else { block = true }` is unchanged. The new triggers can never carry Mask, because `onUninspectable` is validated to be InternalOnly or Block. An injected Mask still blocks.

In `boundedCategories`, extend the allow-list case to:

```go
		case "email", "phone", "credit_card", "ssn", "ipv4", "korean_rrn", "model_pii":
```

Add `"slices"` to the imports if `request_routing.go` does not already import it. It does: `boundedCategories` uses `slices.Sort`.

- [ ] **Step 5: Run the router tests to verify they pass**

Run: `go test ./internal/router/ -race -v -run 'TestClassifier|TestRequestRouting|TestAdaptive'`
Expected: PASS for the new tests and every existing routing and Mask test.

- [ ] **Step 6: Project classifier evidence into audit**

Write the failing test `internal/server/requestpolicy/classifier_evidence_test.go`:

```go
package requestpolicy

import (
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/router"
)

func TestObserveProjectsClassifierRefs(t *testing.T) {
	result := router.RequestRoutingResult{Decision: router.RoutingDecision{
		RequestedModel: "m", Reason: "unchanged", Inspection: "complete",
		Policies:    []router.RoutingPolicyRef{{Name: "privacy", Generation: 7, Rule: "protect"}},
		Classifiers: []router.ClassifierRef{{Name: "laya-ko", Revision: "r1", Boundary: "internal", Mode: "Shadow", Result: "abstain", WouldRestrict: true}},
	}}
	req := Observe(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", nil), result, nil)
	got := audit.RoutingFrom(req.Context())
	want := []audit.ClassifierRef{{Name: "laya-ko", Revision: "r1", Boundary: "internal", Mode: "Shadow", Result: "abstain", WouldRestrict: true}}
	if got == nil || !reflect.DeepEqual(got.Classifiers, want) {
		t.Fatalf("%+v", got)
	}
}
```

Run: `go test ./internal/server/requestpolicy/ -run TestObserveProjectsClassifierRefs -v`
Expected: FAIL (`got.Classifiers` is nil).

In `Observe` (`evidence.go`), after the `d.Recommendations` loop, add:

```go
	for _, c := range d.Classifiers {
		ref.Classifiers = append(ref.Classifiers, audit.ClassifierRef{Name: c.Name, Revision: c.Revision, Boundary: c.Boundary, Mode: c.Mode, Result: c.Result, WouldRestrict: c.WouldRestrict})
	}
```

Run: `go test ./internal/server/... -race`
Expected: PASS.

- [ ] **Step 7: Retain metric series after every swap**

Write the failing test `cmd/mayu/classifier_reload_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func classifierReloadConfig(dir, upstreamURL, revision string) string {
	base := reloadBaseConfig(dir, upstreamURL)
	block := `,"pii_classification":{"classifiers":[{"name":"laya-ko","type":"systemone","mode":"Shadow","revision":"` + revision +
		`","endpoint":"http://127.0.0.1:1","data_boundary":"internal"}]}}`
	return strings.TrimSuffix(strings.TrimSpace(base), "}") + block
}

func TestReloadRetainsOnlyTheCurrentClassifierRevision(t *testing.T) {
	up := newAnthropicUpstream(t)
	t.Setenv("E2E_UPSTREAM_KEY", e2eUpstreamKey)
	t.Setenv("E2E_ADMIN_TOKEN", e2eAdminToken)
	dir := t.TempDir()
	cfgPath := dir + "/config.json"
	rewriteConfig(t, cfgPath, classifierReloadConfig(dir, up.srv.URL, "r1"))
	g, err := newGateway(cfgPath)
	if err != nil {
		t.Fatalf("newGateway: %v", err)
	}
	if g.holder.Load().Classifiers() == nil {
		t.Fatal("boot did not build the classifier Set")
	}
	g.metrics.ObservePIIClassifier("laya-ko", "r1", "Shadow", "clear", 0.01, 1, 1, 1)
	rewriteConfig(t, cfgPath, classifierReloadConfig(dir, up.srv.URL, "r2"))
	if err := g.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	families, err := g.metrics.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		if !strings.HasPrefix(mf.GetName(), "inferplane_pii_classifier_decisions") {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, l := range metric.GetLabel() {
				if l.GetName() == "revision" && l.GetValue() == "r1" {
					t.Fatal("a replaced revision's series survived the reload")
				}
			}
		}
	}
}
```

Run: `go test ./cmd/mayu/ -run TestReloadRetainsOnlyTheCurrentClassifierRevision -v`
Expected: FAIL (the r1 series survives).

In `cmd/mayu/gateway.go`, add the helper near `buildMasking`:

```go
// classifierRevisions names the PII classifiers of a generation, for metric
// series retention after a swap.
func classifierRevisions(st *live.State) map[string]string {
	out := map[string]string{}
	if set := st.Classifiers(); set != nil {
		for _, e := range set.Entries {
			out[e.Name] = e.Revision
		}
	}
	return out
}
```

Add the retention calls:
- after `holder.Swap(st)` at boot (L391): `m.RetainPIIClassifiers(classifierRevisions(st))`, where `m` is the `metrics.New()` value from L144;
- after `g.holder.Swap(st)` in `reloadLocked` (L1214) and in `writeMutation` (L1295): `g.metrics.RetainPIIClassifiers(classifierRevisions(st))`.

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./cmd/mayu/ ./internal/... -race -count=1`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
gofmt -w internal cmd && test -z "$(gofmt -l internal cmd)" && go vet ./...
git add internal/router internal/server/requestpolicy cmd/mayu/gateway.go cmd/mayu/classifier_reload_test.go
git commit -s -m "feat(router): restrict routing on Enforce classifier verdicts, record Shadow evidence" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 9: Sample config, policy, Helm sidecar and an end-to-end gateway test

**Files:**
- Create:
  - `examples/config.pii-classifier-ko.json`, `examples/pii-classifier-ko/governance.yaml`, `examples/helm.pii-classifier-ko.yaml`;
  - `cmd/mayu/pii_classifier_example_test.go`;
  - `internal/config/chart_classifier_test.go`.
- Modify:
  - `charts/inferplane/values.yaml`: append before `policies: {}`;
  - `charts/inferplane/templates/deployment.yaml`: after the `inferplane` container (L74), and in `volumes:` before L93.

**Interfaces:**
- Consumes:
  - Tasks 1–8;
  - the `cmd/mayu` test helpers `routingAcceptanceConfig`, `routingPrivacyDoc`, `bootRoutingAcceptance`, `routingCall`, `routingProvider` (`policy_routing_acceptance_test.go`).
- Produces:
  - the sample files;
  - Helm values `piiClassifier.laya.{enabled,image.repository,image.digest,port,threads,apiKeySecret,resources}`;
  - the container name `laya-ko-pii` and the volume name `laya-tmp`.

- [ ] **Step 1: Write the sample config** (`examples/config.pii-classifier-ko.json`)

```json
{
  "_comment": "Korean PII classifier sample (opt-in). Laya listens on 127.0.0.1:8095 next to the gateway (see examples/pii-classifier-ko/README.md). The classifier starts in Shadow for team pilot-ko. Endpoints, model IDs and rates are illustrative: replace and verify before use. Run from the repository root. Boundary labels are operator assertions.",
  "server": {
    "listen": "127.0.0.1:8080",
    "admin_listen": "127.0.0.1:9090",
    "admin_auth": { "token_refs": [ { "env": "INFERPLANE_ADMIN_TOKEN" } ] }
  },
  "key_store": { "type": "sqlite", "path": "/tmp/inferplane-pii-ko-demo/keys.db" },
  "audit": {
    "failure_mode": "buffer_then_block",
    "buffer": { "path": "/tmp/inferplane-pii-ko-demo/audit.wal" },
    "sinks": [ { "type": "file", "path": "/tmp/inferplane-pii-ko-demo/audit.jsonl" } ]
  },
  "policies": [ "examples/pii-classifier-ko/governance.yaml" ],
  "providers": {
    "approved-public": { "type": "openai_compatible", "base_url": "https://public-models.example.invalid/v1", "data_boundary": "external", "api_key_ref": { "env": "PII_KO_PUBLIC_KEY" } },
    "approved-private": { "type": "openai_compatible", "base_url": "http://127.0.0.1:11434/v1", "data_boundary": "internal" }
  },
  "models": {
    "premium-coder": { "context_window": 32768, "capabilities": ["tools"], "targets": [ { "provider": "approved-public", "model": "premium-v1" } ] },
    "private-coder": { "context_window": 32768, "capabilities": ["tools"], "targets": [ { "provider": "approved-private", "model": "qwen2.5-coder" } ] }
  },
  "model_fallback_family": false,
  "teams": { "pilot-ko": { "allowed_models": [ "premium-coder", "private-coder" ] } },
  "pricing": {
    "on_missing": "block",
    "version": "illustrative-pii-ko-v1",
    "overrides": {
      "approved-public": { "premium-v1": { "input_per_mtok": 5, "output_per_mtok": 15 } },
      "approved-private": { "qwen2.5-coder": { "input_per_mtok": 2, "output_per_mtok": 4 } }
    }
  },
  "pii_classification": {
    "timeout_ms": 3000,
    "cache": { "max_entries": 100000, "ttl_seconds": 3600 },
    "classifiers": [
      {
        "name": "laya-ko",
        "type": "systemone",
        "mode": "Shadow",
        "revision": "laya-ko-pii-v3s1",
        "teams": [ "pilot-ko" ],
        "endpoint": "http://127.0.0.1:8095",
        "data_boundary": "internal",
        "threshold": 0.5,
        "options": { "model": "multilingual", "max_windows": 4, "abstain_band": { "low": 0.1, "high": 0.9 } }
      }
    ]
  }
}
```

`examples/pii-classifier-ko/governance.yaml`:

```yaml
# Korean PII sample. Targets belong to examples/config.pii-classifier-ko.json.
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata:
  name: pilot-ko-privacy
  generation: 1
spec:
  subject:
    team: pilot-ko
  rules:
    - name: protected-content
      failurePolicy: FailClosed
      sensitiveData:
        # A regex detection or an Enforce classifier flag keeps the request on
        # the internal model. An unreachable, slow or uncertain classifier in
        # Enforce does the same (fail closed).
        onDetected: InternalOnly
        onUninspectable: InternalOnly
        internalModels: [private-coder]
```

- [ ] **Step 2: Write the failing example and end-to-end tests** (`cmd/mayu/pii_classifier_example_test.go`)

```go
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/classifier"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/policy"
)

func TestPIIClassifierKoExampleLoads(t *testing.T) {
	t.Setenv("INFERPLANE_ADMIN_TOKEN", "example-placeholder")
	t.Setenv("PII_KO_PUBLIC_KEY", "example-placeholder")
	cfg, err := config.LoadRaw("../../examples/config.pii-classifier-ko.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.ResolveProviders(cfg); err != nil {
		t.Fatal(err)
	}
	st, _, err := live.BuildState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	entries := st.Classifiers().For("pilot-ko")
	if len(entries) != 1 || entries[0].Name != "laya-ko" || entries[0].Mode != classifier.Shadow || entries[0].Revision != "laya-ko-pii-v3s1" || len(st.Classifiers().For("other")) != 0 {
		t.Fatalf("sample classifier scope: %+v", entries)
	}
	h := &live.Holder{}
	h.Swap(st)
	store, err := policy.NewStore(filepath.Join("../..", cfg.Policies[0]))
	if err != nil {
		t.Fatal(err)
	}
	store.SetRoutedAndPriced(h.RoutedAndPriced)
	if err := store.Reload(); err != nil {
		t.Fatalf("sample policy targets: %v", err)
	}
	policies, err := store.MatchingRoutingPolicies("pilot-ko", "")
	if err != nil || len(policies) != 1 {
		t.Fatalf("sample policies: %v %v", policies, err)
	}
	sd := policies[0].Rules[0].SensitiveData
	if sd == nil || sd.OnDetected != v1alpha1.InternalOnly || sd.OnUninspectable != v1alpha1.InternalOnly || !slices.Equal(sd.InternalModels, []string{"private-coder"}) {
		t.Fatalf("sample privacy rule: %+v", sd)
	}
	if st.DataBoundary("approved-private") != "internal" || st.DataBoundary("approved-public") != "external" {
		t.Fatal("sample boundaries lost")
	}
}

// fakeSystemone flags any window that contains marker.
func fakeSystemone(t *testing.T, marker string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			States []string `json:"states"`
		}
		if r.URL.Path != "/v1/systemone/batch" || json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		results := make([]map[string]any, len(body.States))
		for i, s := range body.States {
			p := 0.01
			if strings.Contains(s, marker) {
				p = 0.99
			}
			results[i] = map[string]any{"answers": map[string]any{"pii": map[string]any{"type": "noul", "noul": p}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func classifierGateway(t *testing.T, endpoint, mode string) (*gateway, string) {
	t.Helper()
	doc := routingPrivacyDoc() // team engineering: InternalOnly to "private", Block when uninspectable
	path := routingAcceptanceConfig(t, &doc, func(cfg map[string]any, _ string) {
		cfg["pii_classification"] = map[string]any{"classifiers": []map[string]any{{
			"name": "laya-ko", "type": "systemone", "mode": mode, "revision": "test",
			"endpoint": endpoint, "data_boundary": "internal", "options": map[string]any{"model": "multilingual"},
		}}}
	})
	return bootRoutingAcceptance(t, path)
}

func classifierDecisions(t *testing.T, g *gateway, mode, result string) float64 {
	t.Helper()
	families, err := g.metrics.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		if mf.GetName() != "inferplane_pii_classifier_decisions_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["classifier"] == "laya-ko" && labels["mode"] == mode && labels["result"] == result {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestGatewayClassifierRestrictsDestinationEndToEnd(t *testing.T) {
	const address = "고객 배송지는 서울시 마포구 월드컵로 12입니다"
	t.Run("Enforce", func(t *testing.T) {
		g, key := classifierGateway(t, fakeSystemone(t, "서울시 마포구").URL, "Enforce")
		rec := routingCall(t, g, key, address)
		if rec.Code != 200 || rec.Header().Get("x-inferplane-routing-reason") != "internal_only" {
			body, _ := io.ReadAll(rec.Body)
			t.Fatalf("%d %q %s", rec.Code, rec.Header().Get("x-inferplane-routing-reason"), body)
		}
		if routingProvider(t, g, "private").calls.Load() != 1 || routingProvider(t, g, "public").calls.Load() != 0 {
			t.Fatal("a flagged request reached the external provider")
		}
		if classifierDecisions(t, g, "Enforce", "flagged") != 1 {
			t.Fatal("decision metric missing")
		}
	})
	t.Run("Shadow", func(t *testing.T) {
		g, key := classifierGateway(t, fakeSystemone(t, "서울시 마포구").URL, "Shadow")
		if rec := routingCall(t, g, key, address); rec.Code != 200 {
			t.Fatalf("%d", rec.Code)
		}
		if routingProvider(t, g, "public").calls.Load() != 1 || classifierDecisions(t, g, "Shadow", "flagged") != 1 {
			t.Fatal("Shadow changed routing or recorded nothing")
		}
	})
	t.Run("Enforce with an unreachable classifier fails closed", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		dead.Close()
		g, key := classifierGateway(t, dead.URL, "Enforce")
		if rec := routingCall(t, g, key, "평범한 코딩 질문"); rec.Code != 403 {
			t.Fatalf("status %d, want 403 (onUninspectable Block)", rec.Code)
		}
		if routingProvider(t, g, "public").calls.Load() != 0 || classifierDecisions(t, g, "Enforce", "error") != 1 {
			t.Fatal("an unreachable classifier did not fail closed")
		}
	})
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./cmd/mayu/ -run 'TestPIIClassifierKoExampleLoads|TestGatewayClassifierRestrictsDestinationEndToEnd' -v`
Expected: `TestPIIClassifierKoExampleLoads` FAILs until the sample files exist. The end-to-end test PASSes as soon as Tasks 1–8 are in, since it exercises them through the real gateway; keep it as the regression guard.

Run: `go test ./internal/config/ -run TestExampleConfigsLoad -v`
Expected: PASS once the config file exists; it is globbed automatically.

- [ ] **Step 4: Add the Helm sidecar**

Append to `charts/inferplane/values.yaml`, before `# GovernancePolicy documents`:

```yaml
# Opt-in Korean PII classifier sidecar (examples/pii-classifier-ko). It runs
# laya-serve with the fine-tuned checkpoint baked into the image and listens on
# 127.0.0.1 inside the pod only: no Service port, no containerPort. Point
# config.pii_classification at http://127.0.0.1:<port>
# (examples/helm.pii-classifier-ko.yaml). Off by default.
# Measured 2026-10-16 on CPU: ready in about 14 s; RSS 2.0 GiB idle, 2.6 GiB
# peak; about 0.5 s per 1,000-character Korean window (an evaluation profile).
piiClassifier:
  laya:
    enabled: false
    image:
      repository: ghcr.io/inferplane/laya-ko-pii
      digest: ""          # required when enabled: sha256:...
    port: 8095
    threads: 2
    apiKeySecret: ""      # optional Secret holding key LAYA_API_KEY
    # No CPU limit: throttling adds latency to every classified request.
    resources:
      requests: { cpu: "2", memory: 3Gi }
      limits: { memory: 4Gi }
```

In `charts/inferplane/templates/deployment.yaml`, insert directly after the `inferplane` container's `volumeMounts` block (after the `{{- end }}` on L74, before `      volumes:`):

```yaml
        {{- with .Values.piiClassifier }}{{- with .laya }}{{- if .enabled }}
        {{- if not .image.digest }}{{- fail "piiClassifier.laya.image.digest is required when the sidecar is enabled" }}{{- end }}
        - name: laya-ko-pii
          image: "{{ .image.repository }}@{{ .image.digest }}"
          imagePullPolicy: IfNotPresent
          env:
            - name: LAYA_HOST
              value: "127.0.0.1"
            - name: LAYA_PORT
              value: {{ .port | quote }}
            - name: LAYA_THREADS
              value: {{ .threads | quote }}
            {{- if .apiKeySecret }}
            - name: LAYA_API_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ .apiKeySecret }}
                  key: LAYA_API_KEY
            {{- end }}
          # The server binds 127.0.0.1, so probes run inside the container
          # rather than from the kubelet against the pod IP.
          startupProbe:
            exec:
              command: ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:{{ .port }}/health', timeout=2)"]
            periodSeconds: 5
            failureThreshold: 60
          readinessProbe:
            exec:
              command: ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:{{ .port }}/health', timeout=2)"]
            periodSeconds: 10
          securityContext:
            runAsNonRoot: true
            runAsUser: 65532
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
          resources:
            {{- toYaml .resources | nindent 12 }}
          volumeMounts:
            - name: laya-tmp
              mountPath: /tmp
        {{- end }}{{- end }}{{- end }}
```

In the same file, inside `volumes:` and before `{{- if $stateful }}` (L93), insert:

```yaml
        {{- if dig "laya" "enabled" false (default dict .Values.piiClassifier) }}
        - name: laya-tmp
          emptyDir: {}
        {{- end }}
```

`examples/helm.pii-classifier-ko.yaml`:

```yaml
# Korean PII classifier sample: the Laya sidecar plus a Shadow classifier for
# team pilot-ko. Merge with your own values and replace the internal provider.
# Set piiClassifier.laya.image.digest to the published digest
# (examples/pii-classifier-ko/README.md).
piiClassifier:
  laya:
    enabled: true
    image:
      repository: ghcr.io/inferplane/laya-ko-pii
      digest: ""
config:
  policies: ["/etc/inferplane/policies"]
  providers:
    private-vllm:
      type: openai_compatible
      base_url: http://vllm.inference.svc.cluster.local:8000/v1
      data_boundary: internal
  models:
    private-coder:
      context_window: 32768
      capabilities: [tools]
      targets: [ { provider: private-vllm, model: qwen2.5-coder } ]
  teams:
    pilot-ko: { allowed_models: ["claude-sonnet-4-6", "private-coder"] }
  pii_classification:
    timeout_ms: 3000
    classifiers:
      - name: laya-ko
        type: systemone
        mode: Shadow
        revision: laya-ko-pii-v3s1
        teams: [pilot-ko]
        endpoint: http://127.0.0.1:8095
        data_boundary: internal
        options: { model: multilingual, max_windows: 4, abstain_band: { low: 0.1, high: 0.9 } }
policies:
  pilot-ko-privacy.yaml: |
    apiVersion: inferplane.dev/v1alpha1
    kind: GovernancePolicy
    metadata: { name: pilot-ko-privacy, generation: 1 }
    spec:
      subject: { team: pilot-ko }
      rules:
        - name: protected-content
          failurePolicy: FailClosed
          sensitiveData: { onDetected: InternalOnly, onUninspectable: InternalOnly, internalModels: [private-coder] }
```

- [ ] **Step 5: Write the chart test** (`internal/config/chart_classifier_test.go`)

```go
package config

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const testDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// chartPod renders the chart and returns the Deployment's containers and
// volumes and the rendered gateway config. It fails on any non-data/admin
// Service port.
func chartPod(t *testing.T, args ...string) ([]map[string]any, []any, *Config) {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm unavailable; operator chart validation requires helm")
	}
	out, err := exec.Command(helm, append([]string{"template", "test", "../../charts/inferplane"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("chart failed: %s", out)
	}
	var containers []map[string]any
	var volumes []any
	var cfg *Config
	for _, doc := range strings.Split(string(out), "\n---") {
		var object map[string]any
		if err := yaml.Unmarshal([]byte(doc), &object); err != nil {
			t.Fatal(err)
		}
		switch object["kind"] {
		case "ConfigMap":
			data, _ := object["data"].(map[string]any)
			if raw, ok := data["config.json"].(string); ok {
				cfg = &Config{}
				if err := json.Unmarshal([]byte(raw), cfg); err != nil {
					t.Fatal(err)
				}
			}
		case "Deployment":
			spec, _ := object["spec"].(map[string]any)
			template, _ := spec["template"].(map[string]any)
			pod, _ := template["spec"].(map[string]any)
			for _, c := range pod["containers"].([]any) {
				containers = append(containers, c.(map[string]any))
			}
			volumes, _ = pod["volumes"].([]any)
		case "Service":
			spec, _ := object["spec"].(map[string]any)
			for _, p := range spec["ports"].([]any) {
				if name := p.(map[string]any)["name"]; name != "data" && name != "admin" {
					t.Fatalf("unexpected Service port %v", name)
				}
			}
		}
	}
	return containers, volumes, cfg
}

func envValues(container map[string]any) map[string]string {
	out := map[string]string{}
	for _, e := range container["env"].([]any) {
		m := e.(map[string]any)
		if v, ok := m["value"].(string); ok {
			out[m["name"].(string)] = v
		}
	}
	return out
}

func TestChartLayaSidecarIsOptInAndPodLocal(t *testing.T) {
	if off, _, _ := chartPod(t); len(off) != 1 {
		t.Fatalf("the default chart renders %d containers", len(off))
	}
	helm, _ := exec.LookPath("helm")
	if out, err := exec.Command(helm, "template", "test", "../../charts/inferplane", "--set", "piiClassifier.laya.enabled=true").CombinedOutput(); err == nil || !strings.Contains(string(out), "digest is required") {
		t.Fatalf("an undigested sidecar rendered: %s", out)
	}
	on, volumes, _ := chartPod(t, "--set", "piiClassifier.laya.enabled=true", "--set", "piiClassifier.laya.image.digest="+testDigest)
	if len(on) != 2 || on[0]["name"] != "inferplane" || on[1]["name"] != "laya-ko-pii" {
		t.Fatalf("containers %v", on)
	}
	side := on[1]
	if !strings.HasSuffix(side["image"].(string), "@"+testDigest) {
		t.Fatalf("image %v is not digest-pinned", side["image"])
	}
	if _, exposed := side["ports"]; exposed {
		t.Fatal("the sidecar declares a containerPort")
	}
	if env := envValues(side); env["LAYA_HOST"] != "127.0.0.1" || env["LAYA_PORT"] != "8095" {
		t.Fatalf("env %v", env)
	}
	sc := side["securityContext"].(map[string]any)
	if sc["runAsNonRoot"] != true || sc["readOnlyRootFilesystem"] != true || sc["allowPrivilegeEscalation"] != false {
		t.Fatalf("securityContext %v", sc)
	}
	if limits, _ := side["resources"].(map[string]any)["limits"].(map[string]any); limits["cpu"] != nil {
		t.Fatal("a CPU limit throttles classification latency")
	}
	found := false
	for _, v := range volumes {
		found = found || v.(map[string]any)["name"] == "laya-tmp"
	}
	if !found {
		t.Fatal("no writable /tmp for the read-only sidecar")
	}
}

func TestChartPIIClassifierExampleValuesAgree(t *testing.T) {
	on, _, cfg := chartPod(t, "-f", "../../examples/helm.pii-classifier-ko.yaml", "--set", "piiClassifier.laya.image.digest="+testDigest)
	if len(on) != 2 || cfg == nil || cfg.PIIClassification == nil || len(cfg.PIIClassification.Classifiers) != 1 {
		t.Fatalf("example render: %d containers, cfg %+v", len(on), cfg)
	}
	c := cfg.PIIClassification.Classifiers[0]
	if c.Endpoint != "http://127.0.0.1:"+envValues(on[1])["LAYA_PORT"] || c.Mode != "Shadow" || c.DataBoundary != "internal" {
		t.Fatalf("example classifier %+v does not point at the sidecar", c)
	}
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/config/ ./cmd/mayu/ -race -run 'Chart|Example|PIIClassifier|GatewayClassifier' -v`
Expected: PASS. The two pre-existing chart tests are unchanged: the sidecar is the second container, and the volume is not named `data`.

- [ ] **Step 7: Commit**

```bash
gofmt -w cmd/mayu internal/config && test -z "$(gofmt -l cmd/mayu internal/config)" && go vet ./cmd/mayu/ ./internal/config/
git add examples/config.pii-classifier-ko.json examples/helm.pii-classifier-ko.yaml examples/pii-classifier-ko/governance.yaml charts/inferplane cmd/mayu/pii_classifier_example_test.go internal/config/chart_classifier_test.go
git commit -s -m "feat(examples,chart): Korean PII classifier sample and opt-in Laya sidecar" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 10: The `laya-ko-pii` image and its publication workflow

**Files:**
- Create:
  - `examples/pii-classifier-ko/laya-serve/Dockerfile`, `fetch_weights.py`, `SHA256SUMS`, `requirements.in`, `requirements-amd64.txt`, `requirements-arm64.txt`;
  - `.github/workflows/laya-ko-pii-image.yml`.
- Modify: `.dockerignore`, `.gitignore`.

**Interfaces:**
- Consumes: the checkpoint at `~/inferplane-pii-ft/research/pii-finetune/data/runs/laya-v3-s1/laya-ko-pii` (local build only).
- Produces:
  - the image `laya-ko-pii` with entrypoint `laya-serve`, listening on `127.0.0.1:8095`, user 65532, offline;
  - the build args `WEIGHTS=hf|local`, `HF_REPO`, `HF_REVISION`, and the build context `localweights`.

- [ ] **Step 1: Write the weight manifest and fetcher**

`examples/pii-classifier-ko/laya-serve/SHA256SUMS` (the publishable file set, measured 2026-10-16; 678,206,651 bytes in total):

```
81d6856137aeb1d9cfbc1c1776d5b8196eed2e6ff879e894f517a21a580cc9d3  encoder/config.json
1411ffd8374b88dc465036ef73ed46084e312bb02d25594f317a64be29d92346  model.safetensors
913f5421817f0d68c5003394664b51ff92a314cacaab7fa624e64e9c346f7c6f  questions.json
5a30434bcc3697289f2f7244d45b9e2fd782ff8002b19ed7c14319df0c65f07e  rl_agent_config.json
dbba919c6e5e492bc22beb80abb64e6fcc7e8744dc696e8388b8c977767afa4b  tokenizer/tokenizer.json
f2ff584a8f78ac9f3b6fd0afcf9ab310e41c2d445ef628f9ab4b0d594e992b7a  tokenizer/tokenizer_config.json
```

`examples/pii-classifier-ko/laya-serve/fetch_weights.py`:

```python
"""Download the files listed in SHA256SUMS from a Hugging Face model repository
at one pinned commit, verifying each digest. Standard library only.

usage: fetch_weights.py <org/repo> <40-char commit> <SHA256SUMS> <out dir>
"""

import hashlib
import os
import re
import sys
import urllib.request


def main() -> None:
    repo, revision, sums, out = sys.argv[1:5]
    if not re.fullmatch(r"[A-Za-z0-9._-]+/[A-Za-z0-9._-]+", repo):
        sys.exit("HF_REPO must be <org>/<name>")
    if not re.fullmatch(r"[0-9a-f]{40}", revision):
        sys.exit("HF_REVISION must be a full 40-character commit")
    with open(sums, encoding="utf-8") as f:
        entries = [line.split(None, 1) for line in f if line.strip()]
    for digest, name in entries:
        name = name.strip().lstrip("*")
        if name.startswith("/") or ".." in name.split("/"):
            sys.exit(f"unsafe path in SHA256SUMS: {name}")
        dest = os.path.join(out, name)
        os.makedirs(os.path.dirname(dest), exist_ok=True)
        h = hashlib.sha256()
        url = f"https://huggingface.co/{repo}/resolve/{revision}/{name}"
        with urllib.request.urlopen(url, timeout=600) as resp, open(dest, "wb") as f:
            while chunk := resp.read(1 << 20):
                h.update(chunk)
                f.write(chunk)
        if h.hexdigest() != digest:
            sys.exit(f"sha256 mismatch for {name}")
        print("verified", name, flush=True)


if __name__ == "__main__":
    main()
```

- [ ] **Step 2: Lock the Python dependencies**

`examples/pii-classifier-ko/laya-serve/requirements.in`:

```
laya[serve]==0.4.6
torch==2.14.1
transformers==5.19.0
```

These are the versions that produced the evaluation numbers.

Run (from `examples/pii-classifier-ko/laya-serve/`):

```bash
for pair in amd64:x86_64 arm64:aarch64; do
  arch=${pair%%:*}; plat=${pair##*:}
  uv pip compile requirements.in --python-version 3.12 --python-platform ${plat}-manylinux_2_28 \
    --generate-hashes --emit-index-url --index-url https://download.pytorch.org/whl/cpu \
    --extra-index-url https://pypi.org/simple --index-strategy unsafe-best-match \
    -o requirements-${arch}.txt
done
grep -E '^(laya|torch|transformers)==' requirements-amd64.txt requirements-arm64.txt
```

Expected: both files pin `laya==0.4.6`, `torch==2.14.1+cpu` and `transformers==5.19.0`, with `--hash` lines for every package. A dry run on 2026-10-16 produced 718 hash lines per file.

- [ ] **Step 3: Write the Dockerfile**

```dockerfile
# syntax=docker/dockerfile:1
# laya-serve on CPU with the fine-tuned Korean PII checkpoint baked in. It binds
# 127.0.0.1:8095, runs as uid 65532, needs no egress and only a writable /tmp.
#   published:  --build-arg WEIGHTS=hf --build-arg HF_REPO=<org>/laya-ko-pii --build-arg HF_REVISION=<commit>
#   local test: --build-arg WEIGHTS=local --build-context localweights=<checkpoint dir>
ARG WEIGHTS=hf
ARG PYTHON_IMAGE=python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1

FROM ${PYTHON_IMAGE} AS weights-hf
ARG HF_REPO
ARG HF_REVISION
COPY SHA256SUMS fetch_weights.py /src/
RUN python /src/fetch_weights.py "$HF_REPO" "$HF_REVISION" /src/SHA256SUMS /weights

FROM ${PYTHON_IMAGE} AS weights-local
COPY SHA256SUMS /src/
COPY --from=localweights . /in/
# Copy only the listed files: a training directory also holds optimizer state.
RUN cd /in && sha256sum -c /src/SHA256SUMS \
 && while read -r _ f; do mkdir -p "/weights/$(dirname "$f")" && cp "$f" "/weights/$f"; done < /src/SHA256SUMS

FROM weights-${WEIGHTS} AS weights

FROM ${PYTHON_IMAGE}
ARG TARGETARCH
COPY requirements-amd64.txt requirements-arm64.txt /tmp/req/
RUN pip install --no-cache-dir --require-hashes --no-deps -r /tmp/req/requirements-${TARGETARCH}.txt \
 && rm -rf /tmp/req \
 && useradd --uid 65532 --no-create-home --shell /usr/sbin/nologin laya
COPY --from=weights /weights /models/laya-ko-pii
USER 65532
ENV HOME=/tmp HF_HOME=/tmp/hf HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 \
    LAYA_HOST=127.0.0.1 LAYA_PORT=8095 LAYA_DEVICE=cpu \
    LAYA_MODELS=multilingual LAYA_DEFAULT_MODEL=multilingual \
    LAYA_EXTRA_MODELS='{"multilingual": "/models/laya-ko-pii"}'
ENTRYPOINT ["laya-serve"]
```

- [ ] **Step 4: Build locally and smoke-test the image offline**

```bash
cd examples/pii-classifier-ko/laya-serve
docker buildx build --load --platform linux/arm64 --build-arg WEIGHTS=local \
  --build-context localweights=$HOME/inferplane-pii-ft/research/pii-finetune/data/runs/laya-v3-s1/laya-ko-pii \
  -t laya-ko-pii:local .
docker image inspect laya-ko-pii:local --format '{{.Size}} bytes, user {{.Config.User}}'
docker run -d --rm --name laya-ko-smoke --network none --read-only --tmpfs /tmp:rw,size=512m laya-ko-pii:local
for i in $(seq 1 30); do docker exec laya-ko-smoke python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8095/health', timeout=2)" 2>/dev/null && break; sleep 2; done
docker exec laya-ko-smoke python - <<'EOF'
import json, urllib.request
q = {"pii": {"type": "noul", "instructions": "Does this text contain personal information about a specific person, or a secret credential?"}}
for text in ["고객 김민수(010-2345-6789)의 배송지는 서울시 마포구 월드컵로 12입니다.", "def mask_phone(p): return p[:3] + '-****-' + p[-4:]"]:
    body = json.dumps({"model": "multilingual", "state": text, "questions": q}).encode()
    r = urllib.request.urlopen(urllib.request.Request("http://127.0.0.1:8095/v1/systemone", data=body, headers={"content-type": "application/json"}), timeout=30)
    print(round(json.load(r)["answers"]["pii"]["noul"], 4))
EOF
docker stop laya-ko-smoke
```

Expected:
- The image runs as user `65532`.
- With no network, a read-only root filesystem and only `/tmp` writable, the server becomes healthy.
- The two probes print a value near 0.99 and a value near 0.004, matching the offline check recorded in the spec (0.991 / 0.004).

- [ ] **Step 5: Write the publication workflow** (`.github/workflows/laya-ko-pii-image.yml`)

```yaml
name: laya-ko-pii image

# Manual only. Publishing is gated in docs/superpowers/specs/2026-10-16-systemone-korean-pii-sample-design.md
# (publication gates 1-4); run it only after those gates pass.
on:
  workflow_dispatch:
    inputs:
      hf_repo:
        description: "Hugging Face model repository, <org>/laya-ko-pii"
        required: true
      hf_revision:
        description: "Full 40-character commit of that repository"
        required: true

permissions:
  contents: read

jobs:
  build:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: docker/setup-qemu-action@99012661954931238ded8c8b007157a8430204e1 # v4.4.0
      - uses: docker/setup-buildx-action@f87e5991a6d7451dcb8d9637bfbc97413f497069 # v4.4.1
      - uses: docker/login-action@dbcb813823bdd20940b903addbd779551569679f # v4.6.0
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - id: build
        uses: docker/build-push-action@c3c9e263c25d99ce0380d002d59b67737d91b0dc # v7.4.0
        with:
          context: examples/pii-classifier-ko/laya-serve
          platforms: linux/amd64,linux/arm64
          build-args: |
            WEIGHTS=hf
            HF_REPO=${{ inputs.hf_repo }}
            HF_REVISION=${{ inputs.hf_revision }}
          tags: ghcr.io/inferplane/laya-ko-pii:v3s1-laya0.4.6
          push: true
          provenance: mode=max
          sbom: true
      - name: Digest
        env:
          DIGEST: ${{ steps.build.outputs.digest }}
        run: echo "ghcr.io/inferplane/laya-ko-pii@${DIGEST}" >> "$GITHUB_STEP_SUMMARY"
```

The inputs reach the build only as build arguments, never as shell text. `fetch_weights.py` validates both inputs.

- [ ] **Step 6: Keep training artifacts out of git and build contexts**

Append to `.gitignore`:

```
# Korean PII sample: generated data, model files and local environments
examples/pii-classifier-ko/training/data/
*.safetensors
venv/
.venv/
```

Append to `.dockerignore`:

```
examples/pii-classifier-ko/training/data
**/venv
**/.venv
```

- [ ] **Step 7: Commit**

```bash
git add examples/pii-classifier-ko/laya-serve .github/workflows/laya-ko-pii-image.yml .gitignore .dockerignore
git commit -s -m "feat(examples): offline laya-ko-pii CPU image and manual multi-arch publication workflow" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 11: Training recipe

**Files:**
- Create: `examples/pii-classifier-ko/training/` with:
  - `README.md`, `.gitignore`;
  - `datagen/{kr_values.py,gen_templates.py,fill.py,convert.py}`;
  - `sagemaker/{launch.py,sm_train.py,score_http.py,logs.sh}`;
  - `eval/metrics.py`;
  - `indep/{gen.py,build.py,audit.py,breakdown.py,score_one.sh}`;
  - `regexbaseline/main.go`.
- Source: `~/inferplane-pii-ft/research/pii-finetune/` (uncommitted experiment tree).

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: a reproducible recipe with no account ID, profile name, bucket name or owner tag in it.

- [ ] **Step 1: Copy the recipe files**

```bash
S=$HOME/inferplane-pii-ft/research/pii-finetune
T=examples/pii-classifier-ko/training
mkdir -p $T/datagen $T/sagemaker $T/eval $T/indep $T/regexbaseline
cp $S/datagen/{kr_values.py,gen_templates.py,fill.py,convert.py} $T/datagen/
cp $S/sagemaker/{launch.py,sm_train.py,score_http.py,logs.sh} $T/sagemaker/
cp $S/eval/metrics.py $T/eval/
cp $S/indep/{gen.py,build.py,audit.py,breakdown.py,score_one.sh} $T/indep/
cp $S/regexbaseline/main.go $T/regexbaseline/
printf 'data/\n__pycache__/\n*.pyc\n' > $T/.gitignore
```

The following are deliberately not copied. They are experiment-specific scripts that hard-code run directories: `data/watch*.py`, `data/rescore_laya.sh`, `eval/rescore_laya_cpu.sh` and `indep/score.sh`.

- [ ] **Step 2: Parameterize account-specific values**

In `sagemaker/launch.py`:
- Delete the `ACCOUNT`, `BUCKETS` and `ROLE` constants.
- Add arguments `--bucket` (required), `--role-arn` (required) and `--region` (default `us-east-1`, free text).
- Change `--profile` to default to `None`, so boto3 uses its default credential chain.
- Set `BUCKET = a.bucket` and `RoleArn=a.role_arn`.
- Remove the `owner` tag, keeping only `{"Key": "project", "Value": "inferplane-pii-ft"}`.

In `datagen/gen_templates.py`, `indep/gen.py` and `indep/audit.py`, change `--profile` to `default=None` and the docstring line `Credentials: AWS profile samples-atomoh` to `Credentials: the default AWS credential chain, or --profile`.

In `sagemaker/logs.sh`, replace `P=(--profile samples-atomoh --region us-east-1)` with:

```bash
P=(--region "${AWS_REGION:-us-east-1}")
[ -n "${AWS_PROFILE:-}" ] && P+=(--profile "$AWS_PROFILE")
```

- [ ] **Step 3: Verify that no account-specific value remains and that everything parses**

```bash
T=examples/pii-classifier-ko/training
! grep -rnE '061525506239|180294183052|samples-atomoh|atomoh|inferplane-pii-ft-[0-9]' $T
python3 -m py_compile $T/datagen/*.py $T/sagemaker/*.py $T/eval/*.py $T/indep/*.py
bash -n $T/sagemaker/logs.sh $T/indep/score_one.sh
go vet ./examples/pii-classifier-ko/training/regexbaseline/
```

Expected: no grep match, and no compile, syntax or vet error.

- [ ] **Step 4: Write `training/README.md`**

```markdown
# Training recipe: Korean PII classifier for `systemone`

This recipe produced the `laya-ko-pii-v3s1` checkpoint that the sample image
serves. It needs an AWS account with Amazon Bedrock model access, one private
S3 bucket and a SageMaker execution role scoped to that bucket. It creates no
instance, public IP or security group: training runs as SageMaker training jobs
that end on completion.

## Label policy

The single `noul` question is the trained statement: "Does this text contain
personal information about a specific person, or a secret credential?". A text
is labeled true when it contains at least one of:

- a direct identifier on its own: resident registration number, foreigner
  registration number, passport or driver's license number, phone number,
  personal email, home address, bank account or payment card number;
- a named person together with an attribute about them, such as a birth date,
  health, salary or employee record;
- an internal identifier (employee number or vehicle plate) together with a
  health or salary fact about the same person (rule B);
- a secret credential: password, API key, access token or private key.

It is false for:

- a name with no attribute;
- code, schemas or documents that handle personal-data fields without values;
- masked or placeholder values;
- organisation identifiers;
- lookalike tokens such as UUIDs, hashes and version strings;
- either side of a rule-B pair on its own.

## Steps

1. `datagen/gen_templates.py`: Claude on Amazon Bedrock writes slot templates
   (`⟦PHONE⟧`, …), never values.
2. `datagen/fill.py`: fills the slots locally with synthetic values, adds masked
   minimal pairs, and splits the data by template, with two genres held out.
3. `datagen/convert.py`: writes the Laya and strands-decider training formats.
4. Upload `data/` to `s3://<bucket>/data/`, then run:
   `python3 sagemaker/launch.py --backend laya --bucket <bucket> --role-arn <role> --hp laya_args="--loss soft-ce --grad-accum 2" --hp epochs=8 --hp seed=1`.
   Laya seed 1 trained in 40 minutes on `ml.g5.2xlarge`.
5. `indep/gen.py`, `indep/build.py` and `indep/audit.py`: build an independent
   evaluation set with other generator models, unseen genres and value formats,
   and audit its labels. Score it with `indep/score_one.sh` and
   `eval/metrics.py`, and break it down with `indep/breakdown.py`.

Train several seeds and select on the independent set, not on the
same-generator test split. On 2026-10-14, independent-set F1 varied from 0.934
to 0.953 across three Laya seeds.

## Request settings the checkpoint needs

- Laya: `"model": "multilingual"` in every request, with `LAYA_EXTRA_MODELS`
  mapping `multilingual` to the checkpoint directory.
- strands-decider: the training `criteria`
  `{"false": "no personal information about a specific person and no secret", "true": "contains personal information about a specific person or a secret credential"}`.

Generated data stays out of git (`data/` is ignored). It contains no real
person's data and is not published.
```

- [ ] **Step 5: Commit**

```bash
git add examples/pii-classifier-ko/training
git commit -s -m "docs(examples): reproducible Korean PII training recipe for the systemone sample" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

The secret-scan pre-commit hook runs on these files. A scan of the source tree on 2026-10-16 found no match.

### Task 12: Model card, licenses and sample README

**Files:**
- Create:
  - `examples/pii-classifier-ko/model-card.md`, published as the Hugging Face `README.md`;
  - `examples/pii-classifier-ko/NOTICE`;
  - `examples/pii-classifier-ko/README.md`, `examples/pii-classifier-ko/README.ko.md`.

**Interfaces:**
- Consumes: the evidence in the spec and in Task 14's results.
- Produces: the publication documents for Task 15.

- [ ] **Step 1: Write `model-card.md`**

```markdown
---
license: apache-2.0
language: [ko, en]
base_model: convaiinnovations/laya
library_name: laya
pipeline_tag: text-classification
tags: [pii, korean, privacy, typed-decision, inferplane]
---

# laya-ko-pii (revision laya-ko-pii-v3s1)

A fine-tune of the Laya multilingual typed-decision checkpoint that answers one
yes/no question: does this text contain personal information about a specific
person, or a secret credential? It is tuned for Korean coding-assistant
traffic, and it is the routing signal of inferplane's opt-in `systemone`
classifier sample.

## Intended use

- A gateway-side signal that keeps a request on an internal model (or blocks
  it) when personal data is likely present.
- Not a guarantee of PII detection, not a redaction tool (it outputs no
  spans), and not a basis for decisions about individuals.

## How to use

Serve it with `laya-serve` (Laya 0.4.6):
`LAYA_MODELS=multilingual LAYA_DEFAULT_MODEL=multilingual LAYA_EXTRA_MODELS='{"multilingual": "<dir>"}' laya-serve`.
Every request must carry `"model": "multilingual"` and the trained statement:

    POST /v1/systemone
    {"model": "multilingual", "state": "<text>",
     "questions": {"pii": {"type": "noul", "instructions": "Does this text contain personal information about a specific person, or a secret credential?"}}}

The answer is `answers.pii.noul`, the probability of "yes".

Without `"model": "multilingual"`, Laya routes Latin-script text to its stock
English checkpoint. The image `ghcr.io/inferplane/laya-ko-pii` contains this
revision and the right settings.

## Label policy

See the training recipe in the inferplane repository
(`examples/pii-classifier-ko/training/README.md`), including rule B: an
employee number or vehicle plate together with a health or salary fact about
the same person is personal data.

## Training

- Base: `convaiinnovations/laya` multilingual (mmBERT-base encoder), Laya
  0.4.6, commit `c73908d`.
- Data: 8,168 synthetic training rows (42% positive, 1,512 masked minimal
  pairs). Claude Sonnet 5.5 on Amazon Bedrock wrote slot templates; values were
  generated locally. The data contains no real person's data and is not
  released.
- Instructions: the trained statement plus four paraphrases (two Korean) as
  augmentation.
- Recipe: soft cross-entropy, 8 epochs, effective batch 16, seed 1, about 40
  minutes on one A10G.

## Evaluation

An independent synthetic set of 2,236 rows (after excluding 36 disputed
labels), written by four non-Anthropic generator models in 14 genres absent
from training, with half of the values in unseen formats. Threshold 0.5:

| Model | F1 | Precision | Recall | FPR | AUROC | ECE |
|---|---|---|---|---|---|---|
| This checkpoint | 0.953 | 0.953 | 0.953 | 0.036 | 0.993 | 0.031 |
| Laya multilingual, zero-shot (v1 set) | 0.535 | 0.463 | 0.633 | 0.617 | 0.550 | 0.454 |
| inferplane regex detectors | 0.478 | 0.504 | 0.454 | 0.345 | 0.555 | 0.432 |

The zero-shot row was measured on the earlier v1 independent set (1,679 rows),
whose labels predate rule B.

- Abstain rate with a 0.1 < p < 0.9 band: 2.3%.
- Known errors:
  - one-sided rule-B documents flagged: 14.5%;
  - name-only documents flagged: 3.3%;
  - name-plus-attribute recall: 0.87.
- Seed variance: two other seeds of the same recipe scored F1 0.934 and 0.939.
  This seed was selected on the same set, so 0.953 is optimistic.
- Latency, one request: about 24 ms on an A10G. On CPU (16 vCPU), about
  450 ms per 1,000-character Korean window.

All evaluation data is synthetic and document-level. Accuracy on real traffic
is unknown until measured. inferplane's rollout gates require a Shadow
measurement on labeled samples of real traffic before enforcement.

## Limitations

- **Windowed use.** inferplane reads long requests in windows of about 1,000
  characters. A name and an attribute in different windows, or in different
  turns of a conversation, are not read together.
- **Calibration.** The model is calibrated only on synthetic data (ECE 0.031).
- **Abbreviation styles.** Korean abbreviations and mixed-script identifiers
  outside the training formats may be missed.

## License and lineage

- Apache-2.0.
- Derived from `convaiinnovations/laya` (Apache-2.0), whose multilingual
  encoder is `jhu-clsp/mmBERT-base` (MIT). See NOTICE.
```

- [ ] **Step 2: Write `NOTICE`**

```
laya-ko-pii
Copyright 2026 The inferplane Authors. Licensed under the Apache License, Version 2.0.

This model is a fine-tune of convaiinnovations/laya (multilingual checkpoint),
Copyright Convai Innovations, licensed under the Apache License, Version 2.0.

The Laya multilingual checkpoint uses the jhu-clsp/mmBERT-base encoder. That
repository declares the MIT License in its model card metadata and ships no
LICENSE file (checked 2026-10-16). The MIT License text:

  Copyright (c) the jhu-clsp/mmBERT-base authors

  Permission is hereby granted, free of charge, to any person obtaining a copy
  of this software and associated documentation files (the "Software"), to deal
  in the Software without restriction, including without limitation the rights
  to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
  copies of the Software, and to permit persons to whom the Software is
  furnished to do so, subject to the following conditions:

  The above copyright notice and this permission notice shall be included in
  all copies or substantial portions of the Software.

  THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
  IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
  FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
  AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
  LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
  OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
  SOFTWARE.
```

Publication gate 2 has the user review this attribution, because the upstream
repository names no copyright holder.

- [ ] **Step 3: Write `README.md` and `README.ko.md`**

`examples/pii-classifier-ko/README.md`. `<digest>`, `<namespace>` and `<revision>` are filled in by Task 15, and the Evidence numbers by Task 14:

````markdown
# Korean PII classifier sample (opt-in)

This sample adds a learned Korean PII signal to inferplane's `sensitiveData`
rules. A fine-tuned Laya checkpoint runs next to the gateway, and the
`systemone` classifier asks it whether a request contains personal data. The
classifier only restricts routing: an Enforce flag keeps the request on an
internal model, and it never edits the request.

| File | Purpose |
|---|---|
| `../config.pii-classifier-ko.json` | A complete gateway config: classifier in Shadow for team `pilot-ko` |
| `governance.yaml` | The privacy rule: `InternalOnly` to `private-coder` on detection or when uninspectable |
| `../helm.pii-classifier-ko.yaml` | Helm values: the Laya sidecar and the same classifier |
| `laya-serve/` | The image: Laya 0.4.6 on CPU with the checkpoint baked in, offline, non-root |
| `model-card.md` | The model card published with the weights |
| `training/` | The recipe that produced the checkpoint |

## Run it locally

```bash
docker run -d --rm --name laya-ko -p 127.0.0.1:8095:8095 -e LAYA_HOST=0.0.0.0 \
  ghcr.io/inferplane/laya-ko-pii@<digest>
INFERPLANE_ADMIN_TOKEN=<admin token> PII_KO_PUBLIC_KEY=<provider key> \
  go run ./cmd/mayu serve --config examples/config.pii-classifier-ko.json
```

The container port is published on the host loopback only. Replace the
illustrative providers in the config first.

## Kubernetes

```bash
helm install inferplane charts/inferplane -f examples/helm.pii-classifier-ko.yaml \
  --set piiClassifier.laya.image.digest=<digest>
```

The sidecar listens on `127.0.0.1` inside the gateway pod. It has no Service
port, and its probes run inside the container.

## Profiles

- **CPU (as shipped).** About 0.5 s per 1,000-character Korean window, and a
  ready time of about 14 s. The sample therefore sets `max_windows: 4`,
  `timeout_ms: 3000` and Shadow mode for one pilot team. A request that needs
  more windows records `abstain` without a call, until the verdict cache holds
  its earlier content.
- **GPU (for real traffic).** Build `laya-serve/Dockerfile` with a CUDA torch
  wheel on amd64, and run it as an in-namespace ClusterIP Service on a GPU
  node pool. Use a NetworkPolicy that admits only the gateway pods; never a
  LoadBalancer or a public endpoint. Set `max_windows: 64` and `timeout_ms`
  above the measured p95.

## Evidence

- **Document level.** The checkpoint scores F1 0.953, FPR 0.036 and AUROC
  0.993 on an independent synthetic Korean set of 2,236 rows; the gateway's
  regex detectors score F1 0.478 on the same set. See `model-card.md`.
- **Inside a coding-assistant request** (Task 14, `<date>`, image `<digest>`):
  wrapped-body F1 `<f1>`, FPR `<fpr>`, abstain or error `<n>`.

All evaluation data is synthetic. Accuracy on your traffic is unknown until
you measure it in Shadow.

## Rollout

1. Run Shadow for a pilot team, and compare `would_restrict` in the audit
   records against a labeled sample of that team's traffic.
2. Before Enforce, declare the gates per sensitive type: the false-negative
   rate (including a name in one turn and an attribute in another), the
   false-positive and abstention rates on normal coding requests, the p95
   added latency and resource use, and the fallback during a classifier
   outage.
3. Watch `inferplane_pii_classifier_windows` and the cache hit ratio. A cold
   cache abstains on long first requests, and each gateway process warms its
   own cache.
4. Promote to Enforce only when the gates are met, with `timeout_ms` above the
   measured p95. In Enforce, a timeout restricts the request.
5. A retrained checkpoint gets a new `revision` and starts in Shadow again.

## Limits

- The evaluation is synthetic and document-level.
- The gateway reads long requests in windows. A name and an attribute in
  different windows or turns are not read together, and the cache can widen
  this gap.
- The CPU profile does not cover long coding sessions.
- No span masking: a Mask rule escalates to its `onUninspectable` action.
- Only internal, self-hosted classifiers are supported.

Weights: `<namespace>/laya-ko-pii` at revision `<revision>` on Hugging Face (Apache-2.0).
````

`examples/pii-classifier-ko/README.ko.md` has the same structure and code blocks, in Korean:

````markdown
# 한국어 PII 분류기 샘플 (옵트인)

이 샘플은 inferplane의 `sensitiveData` 규칙에 학습 기반 한국어 PII 신호를
더합니다. 미세조정한 Laya 체크포인트를 게이트웨이 옆에서 실행하고,
`systemone` 분류기가 요청에 개인정보가 있는지 묻습니다. 분류기는 라우팅만
제한합니다. Enforce에서 탐지되면 요청을 내부 모델에만 보내고, 요청 본문은
바꾸지 않습니다.

| 파일 | 용도 |
|---|---|
| `../config.pii-classifier-ko.json` | 전체 게이트웨이 설정: `pilot-ko` 팀에 Shadow로 적용한 분류기 |
| `governance.yaml` | 개인정보 규칙: 탐지되거나 검사할 수 없으면 `private-coder`로만 `InternalOnly` |
| `../helm.pii-classifier-ko.yaml` | Helm 값: Laya 사이드카와 같은 분류기 설정 |
| `laya-serve/` | 이미지: 체크포인트를 구운 CPU용 Laya 0.4.6, 오프라인, non-root |
| `model-card.md` | 가중치와 함께 공개하는 모델 카드 |
| `training/` | 체크포인트를 만든 학습 레시피 |

## 로컬 실행

```bash
docker run -d --rm --name laya-ko -p 127.0.0.1:8095:8095 -e LAYA_HOST=0.0.0.0 \
  ghcr.io/inferplane/laya-ko-pii@<digest>
INFERPLANE_ADMIN_TOKEN=<admin token> PII_KO_PUBLIC_KEY=<provider key> \
  go run ./cmd/mayu serve --config examples/config.pii-classifier-ko.json
```

컨테이너 포트는 호스트 루프백에만 엽니다. 실행 전에 설정의 예시 공급자를
실제 값으로 바꾸세요.

## Kubernetes

```bash
helm install inferplane charts/inferplane -f examples/helm.pii-classifier-ko.yaml \
  --set piiClassifier.laya.image.digest=<digest>
```

사이드카는 게이트웨이 파드 안의 `127.0.0.1`에서만 수신합니다. Service 포트가
없고, 프로브도 컨테이너 안에서 실행합니다.

## 프로필

- **CPU(기본 제공).** 1,000자 한국어 윈도마다 약 0.5초가 걸리고, 준비까지 약
  14초가 걸립니다. 그래서 샘플은 `max_windows: 4`, `timeout_ms: 3000`, Shadow
  모드로 파일럿 팀 하나에만 적용합니다. 윈도가 더 필요한 요청은 판정 캐시에
  앞부분이 쌓일 때까지 호출 없이 `abstain`으로 기록됩니다.
- **GPU(실제 트래픽용).** amd64에서 CUDA torch로 `laya-serve/Dockerfile`을
  빌드하고, GPU 노드 풀에서 같은 네임스페이스의 ClusterIP Service로
  실행합니다. 게이트웨이 파드만 허용하는 NetworkPolicy를 두고,
  LoadBalancer나 공개 엔드포인트는 쓰지 않습니다. `max_windows: 64`로 두고
  `timeout_ms`는 측정한 p95보다 크게 잡습니다.

## 근거

- **문서 단위.** 독립 합성 한국어 평가셋 2,236행에서 F1 0.953, FPR 0.036,
  AUROC 0.993입니다. 같은 셋에서 게이트웨이 정규식 탐지기는 F1 0.478입니다.
  자세한 내용은 `model-card.md`를 보세요.
- **코딩 어시스턴트 요청 안에서**(Task 14, `<date>`, 이미지 `<digest>`):
  감싼 본문 F1 `<f1>`, FPR `<fpr>`, abstain 또는 오류 `<n>`건.

평가 데이터는 모두 합성입니다. 실제 트래픽에서의 정확도는 Shadow로 측정하기
전까지 알 수 없습니다.

## 롤아웃

1. 파일럿 팀에 Shadow로 적용하고, 감사 기록의 `would_restrict`를 그 팀
   트래픽에서 라벨링한 표본과 비교합니다.
2. Enforce 전에 민감 유형별 게이트를 정해 둡니다: 미탐률(이름과 속성이 다른
   turn에 있는 경우 포함), 일반 코딩 요청의 오탐률과 abstain 비율, p95 추가
   지연과 자원 사용량, 분류기 장애 시의 대체 경로.
3. `inferplane_pii_classifier_windows`와 캐시 적중률을 지켜봅니다. 캐시가 비어
   있으면 긴 첫 요청은 abstain이 되고, 게이트웨이 프로세스마다 캐시가 따로
   채워집니다.
4. 게이트를 충족했을 때만 Enforce로 올리고, `timeout_ms`는 측정한 p95보다
   크게 잡습니다. Enforce에서는 타임아웃이 곧 요청 제한입니다.
5. 다시 학습한 체크포인트는 새 `revision`을 받고 Shadow부터 다시 시작합니다.

## 한계

- 평가는 합성 데이터의 문서 단위 결과입니다.
- 게이트웨이는 긴 요청을 윈도 단위로 읽습니다. 서로 다른 윈도나 turn에 있는
  이름과 속성은 함께 판정되지 않으며, 캐시가 이 간극을 넓힐 수 있습니다.
- CPU 프로필은 긴 코딩 세션을 감당하지 못합니다.
- 구간 마스킹은 없습니다. Mask 규칙은 `onUninspectable` 동작으로 격상됩니다.
- 내부 자체 호스팅 분류기만 지원합니다.

가중치: Hugging Face의 `<namespace>/laya-ko-pii`, revision `<revision>` (Apache-2.0).
````

- [ ] **Step 4: Commit**

```bash
git add examples/pii-classifier-ko/model-card.md examples/pii-classifier-ko/NOTICE examples/pii-classifier-ko/README.md examples/pii-classifier-ko/README.ko.md
git commit -s -m "docs(examples): Korean PII sample README, model card and NOTICE" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 13: Reference documentation, module guides and CHANGELOG

**Files:**
- Modify:
  - `docs/reference/security.md` and `docs/reference/security.ko.md`;
  - `docs/reference/agent-llm.md` and `docs/reference/agent-llm.ko.md`;
  - `docs/policy-routing.md` and `docs/policy-routing.ko.md`;
  - `docs/adaptive-routing.md` and `docs/adaptive-routing.ko.md`;
  - `internal/CLAUDE.md`, `plugins/CLAUDE.md`, root `CLAUDE.md`, `CHANGELOG.md`.
- Regenerate: `AGENTS.md` (generated; never hand-edit).

**Interfaces:**
- Consumes: everything above.
- Produces: documentation that passes `scripts/check_docs_translations.py` and `mkdocs build --strict`.

- [ ] **Step 1: Add the security reference section**

In `docs/reference/security.md`, after the `### Sensitive-data destination restrictions (ADR-043)` section, add:

```markdown
### Opt-in PII classifiers (systemone)

`pii_classification` attaches up to four self-hosted classifiers to the existing
`sensitiveData` rules (specification:
`docs/superpowers/specs/2026-10-16-systemone-korean-pii-sample-design.md`).

**Input.** A classifier reads every text the regex inspector reads: system
input, tool schemas, all turns and tool results. Roles and history positions
are never trusted.

**Restriction.** Only Enforce entries restrict routing.
- A flag applies `onDetected`. A Mask rule escalates to `onUninspectable`,
  because a verdict has no spans to redact.
- Abstain, timeout and errors apply `onUninspectable`, so the classifier fails
  closed.
- Shadow entries are recorded and never change routing.
- count_tokens never calls a classifier.

**Boundary.** Only the `internal` boundary is supported. The HTTP client
ignores proxy environment variables and redirects.

**Verdict cache.** A node-local cache stores only clear verdicts, keyed by an
HMAC under a per-generation random key over the team, the classifier settings,
the applicable policy generations and the text. It stores no text, and it is
cleared on every reload and restart.

**Evidence.** Audit and metrics carry bounded enums only. They never carry
text, windows, probabilities, digests or endpoints.

**Known limit.** A name and an attribute in different windows or turns are not
read together.
```

In `docs/reference/security.ko.md`, add the same section in Korean at the same position:

```markdown
### 옵트인 PII 분류기 (systemone)

`pii_classification`은 자체 호스팅 분류기를 최대 4개까지 기존 `sensitiveData`
규칙에 연결합니다(명세:
`docs/superpowers/specs/2026-10-16-systemone-korean-pii-sample-design.md`).

**입력.** 분류기는 정규식 검사기가 읽는 텍스트를 모두 읽습니다. system 입력,
도구 스키마, 모든 turn과 tool result가 여기에 포함됩니다. 역할이나 기록 위치는
신뢰하지 않습니다.

**제한.** 라우팅을 제한하는 것은 Enforce 항목뿐입니다.
- 탐지되면 `onDetected`를 적용합니다. Mask 규칙은 판정에 지울 구간이 없으므로
  `onUninspectable`로 격상합니다.
- Abstain, 타임아웃, 오류는 `onUninspectable`을 적용합니다. 즉 fail-closed입니다.
- Shadow 항목은 기록만 하고 라우팅을 바꾸지 않습니다.
- count_tokens는 분류기를 호출하지 않습니다.

**경계.** `internal` 경계만 지원합니다. HTTP 클라이언트는 프록시 환경 변수와
리다이렉트를 따르지 않습니다.

**판정 캐시.** 노드 로컬 캐시에는 Clear 판정만 저장합니다. 키는 세대마다 새로
만드는 임의 키로 계산한 HMAC이며, 팀, 분류기 설정, 적용되는 정책 세대, 텍스트를
덮습니다. 원문은 저장하지 않고, 재적재나 재시작 때마다 비웁니다.

**증거.** 감사와 메트릭에는 제한된 열거값만 남깁니다. 텍스트, 윈도, 확률,
digest, 엔드포인트는 남기지 않습니다.

**알려진 한계.** 서로 다른 윈도나 turn에 있는 이름과 속성은 함께 판정되지
않습니다.
```

- [ ] **Step 2: Update the routing guides**

In `docs/policy-routing.md`, `## Inspection and capability limits`, after "These signals have false positives/negatives and do not cover every PII type or obfuscation.", add:

```markdown
An opt-in `pii_classification` classifier can add a learned signal to the same
rules; see the Korean sample in `examples/pii-classifier-ko/README.md`.
Classifier results are subject to the same limits: a classifier is a signal,
not a guarantee.
```

In `docs/policy-routing.ko.md`, at the same position:

```markdown
옵트인 `pii_classification` 분류기는 같은 규칙에 학습 기반 신호를 더할 수
있습니다(한국어 샘플은 `examples/pii-classifier-ko/README.md` 참조). 분류기
결과에도 같은 한계가 적용됩니다. 분류기는 신호일 뿐 보장이 아닙니다.
```

In `docs/adaptive-routing.md`, replace the `PII dedicated model` row with:

```markdown
| PII dedicated model | `InternalOnly` with explicit approved models and internal provider metadata; optional `pii_classification` (Shadow/Enforce) adds a learned trigger |
```

In `docs/adaptive-routing.ko.md`, replace the `개인정보 전용 모델` row with:

```markdown
| 개인정보 전용 모델 | 명시적 승인 모델과 내부 공급자 메타데이터의 `InternalOnly`; 선택적 `pii_classification`(Shadow/Enforce)이 학습 기반 트리거를 더함 |
```

In `docs/reference/agent-llm.md`, in the `### 2. Components` table, add after the `PII mask filter` row:

```markdown
| PII classifier (systemone) | `internal/classifier/`, `plugins/systemone/` | opt-in learned PII signal over `/v1/systemone` and `/v1/systemone/batch` (`noul`); restricts routing only; sends the `model`/`criteria` settings the checkpoint was trained with; Korean sample in `examples/pii-classifier-ko/` |
```

In `docs/reference/agent-llm.ko.md`, after the `기존 PII 필터` row:

```markdown
| PII 분류기 (systemone) | `internal/classifier/`, `plugins/systemone/` | `/v1/systemone`·`/v1/systemone/batch`(`noul`)를 쓰는 선택적 학습 기반 PII 신호; 라우팅만 제한; 체크포인트 학습 때의 `model`/`criteria` 설정을 전송; 한국어 샘플은 `examples/pii-classifier-ko/` |
```

- [ ] **Step 3: Update the translation hashes and verify the docs**

```bash
for f in docs/reference/security.md docs/reference/agent-llm.md docs/policy-routing.md docs/adaptive-routing.md; do
  ko=${f%.md}.ko.md; sum=$(sha256sum "$f" | cut -d' ' -f1)
  sed -i "s/^translation_source_sha256: .*/translation_source_sha256: $sum/" "$ko"
done
python3 scripts/check_docs_translations.py
python3 -m venv /tmp/docs-venv && /tmp/docs-venv/bin/pip install -q --require-hashes -r requirements-docs.txt \
  && /tmp/docs-venv/bin/python -m mkdocs build --strict -d /tmp/docs-site
```

Expected: the translation check passes, and mkdocs builds with no warning. The relative links to `examples/…` files resolve through `scripts/docs_hooks.py`.

- [ ] **Step 4: Update the module guides and CHANGELOG**

In `internal/CLAUDE.md`, under `## Key Packages`, add:

```markdown
- `classifier/` — opt-in PII classifier seam (leaf, stdlib): registry, per-generation `Set` with an HMAC clear-only verdict cache, parallel `Run`, Enforce-only `Combine`. Concrete types live in `plugins/<type>`.
```

Also under `## Policy-aware routing (ADR-043)`, add one sentence:

```markdown
Opt-in classifiers run inside `RouteRequest` after regex inspection, only for privacy-governed non-count requests; Enforce flags map to `onDetected` (Mask escalates to `onUninspectable`) and abstain/error map to `onUninspectable`; Shadow never routes.
```

In `plugins/CLAUDE.md`, add under Key packages:

```markdown
- `systemone/` — PII classifier for `/v1/systemone` servers (Laya, strands-decider); registers as `systemone` via `classifier.Register` in `init()`; units, windows, bounded error classes; never edits a body or logs text.
```

In root `CLAUDE.md` `## Project Structure`, add `classifier/      - Opt-in PII classifier seam: registry, verdict cache, Run (sub-project 1 of the PII classifier design)` under `internal/`. Change the `plugins/` line to `plugins/           - Concrete filters (piimask/, ADR-009) and PII classifiers (systemone/)`. Then regenerate `AGENTS.md` with the `co-agent:sync-context` skill. Do not edit `AGENTS.md` by hand.

In `CHANGELOG.md`, under `## [Unreleased]` → `### Added`, add:

```markdown
- **Opt-in PII classifiers with a Korean sample.** A new `pii_classification` block attaches up to four self-hosted `systemone` classifiers (Laya, strands-decider) to the existing `sensitiveData` rules. A classifier reads the whole retransmitted request, including system input, tool schemas and history. Enforce flags apply `onDetected`; Mask escalates to `onUninspectable`; abstain, timeout and errors fail closed; Shadow only records. A node-local, team-scoped, clear-only HMAC verdict cache keeps long sessions affordable and is cleared on every reload. `examples/pii-classifier-ko/` ships the sample: config, policy, an opt-in Helm sidecar (`piiClassifier.laya`), an offline CPU image, a fine-tuned Laya checkpoint with its model card, and the training recipe. Known limits: evaluation is synthetic only (independent-set F1 0.953 for the published checkpoint); a CPU sidecar costs about 0.5 s per 1,000-character Korean window; names and attributes in different windows or turns are not read together; no span masking; only the internal boundary is supported.
```

- [ ] **Step 5: Commit**

```bash
bash tests/run-all.sh
git add docs internal/CLAUDE.md plugins/CLAUDE.md CLAUDE.md AGENTS.md CHANGELOG.md
git commit -s -m "docs: opt-in PII classifier reference, routing guides and Korean sample changelog" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 14: Live acceptance against the real image

**Files:**
- Create:
  - `cmd/mayu/pii_classifier_live_test.go` (`//go:build liveclassifier`);
  - `plugins/systemone/live_eval_test.go` (`//go:build liveclassifier`, package `systemone_test`).
- Modify: `examples/pii-classifier-ko/README.md` and `README.ko.md` (the Evidence section).

**Interfaces:**
- Consumes:
  - Task 9 helpers (`classifierGateway`, `routingCall`, `routingProvider`, `classifierDecisions`);
  - Tasks 1–5;
  - a running `laya-ko-pii:local` container.
- Produces: measured acceptance results for the PR and the README.

- [ ] **Step 1: Write the live gateway test** (`cmd/mayu/pii_classifier_live_test.go`)

```go
//go:build liveclassifier

package main

import (
	"os"
	"testing"
)

// Run: INFERPLANE_LIVE_SYSTEMONE_URL=http://127.0.0.1:18095 go test -tags liveclassifier ./cmd/mayu/ -run TestLive -v
func TestLiveClassifierEnforceAndCache(t *testing.T) {
	url := os.Getenv("INFERPLANE_LIVE_SYSTEMONE_URL")
	if url == "" {
		t.Skip("INFERPLANE_LIVE_SYSTEMONE_URL not set")
	}
	g, key := classifierGateway(t, url, "Enforce")
	if rec := routingCall(t, g, key, "고객 김민수(010-2345-6789)의 배송지는 서울시 마포구 월드컵로 12입니다."); rec.Code != 200 || rec.Header().Get("x-inferplane-routing-reason") != "internal_only" {
		t.Fatalf("PII request: %d %q", rec.Code, rec.Header().Get("x-inferplane-routing-reason"))
	}
	if rec := routingCall(t, g, key, "이 Go 함수의 에러 처리를 errors.Join으로 바꿔줘."); rec.Code != 200 || rec.Header().Get("x-inferplane-routing-reason") == "internal_only" {
		t.Fatalf("clean request: %d %q", rec.Code, rec.Header().Get("x-inferplane-routing-reason"))
	}
	if routingProvider(t, g, "private").calls.Load() != 1 || routingProvider(t, g, "public").calls.Load() != 1 {
		t.Fatal("destinations do not match the verdicts")
	}
	if classifierDecisions(t, g, "Enforce", "flagged") != 1 || classifierDecisions(t, g, "Enforce", "clear") != 1 {
		t.Fatal("decision metrics missing")
	}
	// The second request repeats the JSON keys and the model name that the
	// first one put in the cache, so some of its units must be hits.
	if cacheHits(t, g) == 0 {
		t.Fatal("the second request read nothing from the verdict cache")
	}
}

func cacheHits(t *testing.T, g *gateway) float64 {
	t.Helper()
	families, err := g.metrics.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		if mf.GetName() != "inferplane_pii_classifier_cache_segments_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "result" && l.GetValue() == "hit" {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}
```

- [ ] **Step 2: Write the live wrapped-body evaluation** (`plugins/systemone/live_eval_test.go`)

```go
//go:build liveclassifier

package systemone_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/classifier"
	"github.com/inferplane/inferplane/internal/sensitivity"
	_ "github.com/inferplane/inferplane/plugins/systemone"
)

const codingSystem = "You are a coding assistant working in a Go repository. Follow the repository conventions, run the tests before you report, and keep answers short."

// wrap places a row's text where coding traffic carries it: a tool result
// after a harmless first turn, behind a system prompt and two tool schemas.
func wrap(text string) []byte {
	body := map[string]any{
		"system": codingSystem,
		"tools": []map[string]any{
			{"name": "read_file", "description": "Read a file from the workspace.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}},
			{"name": "run", "description": "Run a shell command.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}}}},
		},
		"messages": []map[string]any{
			{"role": "user", "content": "이 파일 내용을 요약해줘."},
			{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "t1", "name": "read_file", "input": map[string]any{"path": "notes.txt"}}}},
			{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": text}}},
		},
		"max_tokens": 256,
	}
	b, _ := json.Marshal(body)
	return b
}

//	Run: INFERPLANE_LIVE_SYSTEMONE_URL=http://127.0.0.1:18095 INFERPLANE_LIVE_ROWS=<rows_v3.jsonl> INFERPLANE_LIVE_EXCLUDE=<disputed_ids_v3.txt> \
//	  go test -tags liveclassifier ./plugins/systemone/ -run TestLiveWrappedBodies -v -timeout 3h
func TestLiveWrappedBodies(t *testing.T) {
	url, rows := os.Getenv("INFERPLANE_LIVE_SYSTEMONE_URL"), os.Getenv("INFERPLANE_LIVE_ROWS")
	if url == "" || rows == "" {
		t.Skip("INFERPLANE_LIVE_SYSTEMONE_URL and INFERPLANE_LIVE_ROWS not set")
	}
	exclude := map[string]bool{}
	if path := os.Getenv("INFERPLANE_LIVE_EXCLUDE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range strings.Fields(string(raw)) {
			exclude[id] = true
		}
	}
	c, err := classifier.New(classifier.Config{Name: "laya-ko", Type: "systemone", Endpoint: url, Threshold: 0.5,
		Options: map[string]json.RawMessage{"model": json.RawMessage(`"multilingual"`)}})
	if err != nil {
		t.Fatal(err)
	}
	set, err := classifier.NewSet([]classifier.Entry{{Name: "laya-ko", Mode: classifier.Enforce, Classifier: c, Global: true}}, 2*time.Minute, 4<<20, 100_000, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(rows)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var tp, fp, fn, tn, abstain int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r struct {
			ID    string `json:"id"`
			Text  string `json:"text"`
			Label int    `json:"label"`
		}
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if exclude[r.ID] {
			continue
		}
		segments, err := sensitivity.Segments(context.Background(), "anthropic", wrap(r.Text))
		if err != nil {
			t.Fatal(err)
		}
		res := set.Run(context.Background(), set.Entries, classifier.Request{Team: "eval", Segments: segments})[0]
		flagged := res.Outcome == classifier.OutcomeFlagged
		if res.Outcome == classifier.OutcomeAbstain || res.Outcome == classifier.OutcomeError {
			abstain++
		}
		switch {
		case flagged && r.Label == 1:
			tp++
		case flagged:
			fp++
		case r.Label == 1:
			fn++
		default:
			tn++
		}
	}
	f1 := 2 * float64(tp) / float64(2*tp+fp+fn)
	fpr := float64(fp) / float64(fp+tn)
	t.Logf("wrapped bodies: n=%d F1=%.3f FPR=%.3f abstain/error=%d (document-level reference: F1 0.953, FPR 0.036)", tp+fp+fn+tn, f1, fpr, abstain)
	if f1 < 0.90 || fpr > 0.08 {
		t.Fatalf("wrapped-body accuracy below the sanity floor: F1 %.3f FPR %.3f", f1, fpr)
	}
}
```

The floor (F1 0.90, FPR 0.08) is a sanity check that the coding-traffic framing does not destroy accuracy. It is not a rollout gate.

- [ ] **Step 3: Run both against the local image**

```bash
docker run -d --rm --name laya-ko-accept -p 127.0.0.1:18095:8095 -e LAYA_HOST=0.0.0.0 laya-ko-pii:local
until curl -sf http://127.0.0.1:18095/health >/dev/null; do sleep 2; done
INFERPLANE_LIVE_SYSTEMONE_URL=http://127.0.0.1:18095 go test -tags liveclassifier ./cmd/mayu/ -run TestLive -v
D=$HOME/inferplane-pii-ft/research/pii-finetune/data/indep
INFERPLANE_LIVE_SYSTEMONE_URL=http://127.0.0.1:18095 INFERPLANE_LIVE_ROWS=$D/rows_v3.jsonl INFERPLANE_LIVE_EXCLUDE=$D/disputed_ids_v3.txt \
  go test -tags liveclassifier ./plugins/systemone/ -run TestLiveWrappedBodies -v -timeout 3h | tee /tmp/live-eval.log
docker stop laya-ko-accept
```

The container port is published on the host loopback only.

Expected:
- Both tests PASS.
- The evaluation log line reports the wrapped-body F1 and FPR.
- If the floor fails, stop and report it to the user. It means the coding-traffic framing changes the model's answers, which is a design finding, not a test to loosen.

- [ ] **Step 4: Record the results and commit**

Add the measured wrapped-body F1, FPR and abstain count, with the date and the image tag, to the Evidence section of both sample READMEs.

```bash
gofmt -w cmd/mayu plugins/systemone && go vet -tags liveclassifier ./cmd/mayu/ ./plugins/systemone/
git add cmd/mayu/pii_classifier_live_test.go plugins/systemone/live_eval_test.go examples/pii-classifier-ko/README.md examples/pii-classifier-ko/README.ko.md
git commit -s -m "test(classifier): live acceptance and wrapped-body evaluation against the Laya image" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 15: Final verification, publication and PR (each external step needs explicit user confirmation)

**Files:**
- Modify (after publication):
  - `examples/pii-classifier-ko/README.md`, `README.ko.md`;
  - `examples/helm.pii-classifier-ko.yaml` (`image.digest`);
  - `examples/pii-classifier-ko/model-card.md` (image reference).

**Interfaces:**
- Consumes: everything above.
- Produces: the published Hugging Face repository and GHCR image, and the PR.

- [ ] **Step 1: Run the full local gates**

```bash
git fetch origin && git rebase origin/main
gofmt -l . ; go vet ./... ; CGO_ENABLED=0 go build -trimpath -o bin/mayu ./cmd/mayu && CGO_ENABLED=0 go build -trimpath -o bin/inferplaned ./cmd/inferplaned
go test ./... -race -count=1 -timeout 20m > /tmp/full-test.log 2>&1; echo "exit $?"; grep -E '^(FAIL|---)' /tmp/full-test.log
INFERPLANE_ADMIN_TOKEN=lint go run ./cmd/mayu pricing check --config examples/config.json
bash tests/run-all.sh
```

Expected: no gofmt output, vet clean, both builds succeed, the test exit is 0 with no FAIL line, and the pricing check and harness pass. Redirect the test output to a log, as shown, instead of piping it into `tail`.

- [ ] **Step 2: Ask the user to confirm the publication gates**

Ask with `AskUserQuestion`, and do not proceed without an explicit yes. The questions are:
1. Has the organization completed its review of the generation-model terms for training data produced with Claude Sonnet 5.5 on Amazon Bedrock?
2. Which Hugging Face namespace? The CLI is logged in as user `atomwow`; there is no `inferplane` organization on Hugging Face as of 2026-10-16.
3. Has the user reviewed the model card and the NOTICE attribution (mmBERT-base declares MIT without a LICENSE file or a named copyright holder)?

- [ ] **Step 3: Publish the weights to Hugging Face (after an explicit yes)**

```bash
REPO=<namespace>/laya-ko-pii
STAGE=$(mktemp -d)
C=$HOME/inferplane-pii-ft/research/pii-finetune/data/runs/laya-v3-s1/laya-ko-pii
(cd $C && while read -r _ f; do mkdir -p "$STAGE/$(dirname "$f")" && cp "$f" "$STAGE/$f"; done < $OLDPWD/examples/pii-classifier-ko/laya-serve/SHA256SUMS)
cp examples/pii-classifier-ko/model-card.md $STAGE/README.md
cp LICENSE $STAGE/LICENSE && cp examples/pii-classifier-ko/NOTICE $STAGE/NOTICE
(cd $STAGE && sha256sum -c $OLDPWD/examples/pii-classifier-ko/laya-serve/SHA256SUMS)
hf repo create $REPO --repo-type model
hf upload $REPO $STAGE . --commit-message "laya-ko-pii-v3s1"
REV=$(curl -s https://huggingface.co/api/models/$REPO | python3 -c 'import json,sys; print(json.load(sys.stdin)["sha"])')
python3 examples/pii-classifier-ko/laya-serve/fetch_weights.py $REPO $REV examples/pii-classifier-ko/laya-serve/SHA256SUMS $(mktemp -d)
echo "HF revision $REV"
```

Expected: every file verifies after a fresh download from the pinned revision. Record `$REPO` and `$REV` in the sample READMEs.

- [ ] **Step 4: Push the branch and open the PR (after an explicit yes)**

Push `feat/pii-classifier-systemone` and open a PR against `main`. The PR body must state:
- what changed, by task;
- the measured acceptance results;
- the publication state (Hugging Face done, image pending the workflow);
- the known limits;
- `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.

The image workflow can be dispatched only after it exists on the default branch, so the image is published after the merge.

- [ ] **Step 5: After the merge, publish the image (after an explicit yes)**

```bash
gh workflow run laya-ko-pii-image.yml -f hf_repo=$REPO -f hf_revision=$REV
gh run watch "$(gh run list --workflow laya-ko-pii-image.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
```

Read the digest from the run summary. Pull the image on this host and repeat the Task 10 Step 4 smoke test, with `ghcr.io/inferplane/laya-ko-pii@<digest>` in place of `laya-ko-pii:local`.

- [ ] **Step 6: Pin the digest in a follow-up PR**

Set `piiClassifier.laya.image.digest` in `examples/helm.pii-classifier-ko.yaml`, and add the image reference to both READMEs and the model card (also re-upload the model card to Hugging Face). Then run the Step 1 gates and open the follow-up PR, again after an explicit yes.
