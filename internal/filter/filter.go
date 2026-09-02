// Package filter is the core-side seam for request-transform plugins (the
// spec's filter chain ⑥). It holds ONLY the interface + a name registry — the
// concrete filters live under plugins/<name>/ and register themselves via a
// blank import in cmd/mayu, exactly like providers. Core packages
// (server, router) import this interface, never a concrete plugin, so the
// plugin surface stays isolated (ADR-009).
package filter

import "sort"

// RequestFilter transforms request TEXT before it is forwarded upstream. A
// filter operates on extracted text only (never structural fields like
// cache_control / tool blocks / the system prompt); the caller is responsible
// for scoping which text spans are passed in. Mask returns the transformed text
// and the number of substitutions made (0 = unchanged).
type RequestFilter interface {
	Name() string
	Mask(text string) (masked string, redactions int)
}

// Detection is the typed detector result (strategy Phase 2): the number of
// protected spans the filter chain found in the request text. It is
// derived from the SAME Mask pass enforcement uses (run detect-only, output
// discarded), so detection and transformation can never disagree about
// what counts as protected.
type Detection struct {
	Redactions int
	// Kinds counts redactions per detector kind (e.g. "email", "card") when
	// the filter can report them (KindReporter below); nil otherwise. Kind
	// names are filter-declared constants, never input text — safe for the
	// audit record and for bounded metric labels.
	Kinds map[string]int
}

// Clean reports whether the detector chain found nothing protected.
func (d Detection) Clean() bool { return d.Redactions == 0 }

// Add folds another detection into d (summing kinds; nil-safe).
func (d *Detection) Add(o Detection) {
	d.Redactions += o.Redactions
	if len(o.Kinds) > 0 {
		if d.Kinds == nil {
			d.Kinds = map[string]int{}
		}
		for k, v := range o.Kinds {
			d.Kinds[k] += v
		}
	}
}

// KindReporter is an OPTIONAL filter capability (type-asserted, like the
// provider capabilities): a filter that can say WHAT kinds of protected
// spans it found, not just how many — the detector-evidence half of the
// strategy Phase 2 contract. A filter without it still works; its
// detections just carry no kind breakdown.
type KindReporter interface {
	MaskKinds(text string) (masked string, det Detection)
}

// Detect runs f over text, preferring the KindReporter capability so the
// caller gets per-kind counts when the filter has them.
func Detect(f RequestFilter, text string) (string, Detection) {
	if kr, ok := f.(KindReporter); ok {
		return kr.MaskKinds(text)
	}
	masked, n := f.Mask(text)
	return masked, Detection{Redactions: n}
}

// Masking is the resolved, per-request masking decision the assembly builds from
// the `plugins` config + the registry, and injects into the request handlers.
// It pairs the resolved filter with the team scope (Global = all teams). A nil
// *Masking, or one with a nil Filter, means masking is off — Enabled is
// false-safe so handlers can hold a single nil field.
type Masking struct {
	Filter RequestFilter
	Global bool
	Teams  map[string]bool
}

// Enabled reports whether the given team's requests must be masked.
func (m *Masking) Enabled(team string) bool {
	if m == nil || m.Filter == nil {
		return false
	}
	return m.Global || m.Teams[team]
}

var registry = map[string]RequestFilter{}

// Register adds a filter under its Name(). Called from a plugin's init(); a
// duplicate name panics (a programming error, surfaced at startup).
func Register(f RequestFilter) {
	name := f.Name()
	if _, dup := registry[name]; dup {
		panic("filter: duplicate registration for " + name)
	}
	registry[name] = f
}

// Get returns the registered filter for name (ok=false if absent). Config
// validation uses this to reject an unknown plugin name at load.
func Get(name string) (RequestFilter, bool) {
	f, ok := registry[name]
	return f, ok
}

// Names returns the registered filter names, sorted (for diagnostics/tests).
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
