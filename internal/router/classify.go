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

// hasEnforce reports whether any entry can restrict routing.
func hasEnforce(entries []classifier.Entry) bool {
	for _, e := range entries {
		if e.Mode == classifier.Enforce {
			return true
		}
	}
	return false
}
