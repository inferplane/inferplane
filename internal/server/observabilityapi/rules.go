package observabilityapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"unicode"

	sigyaml "sigs.k8s.io/yaml"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/policy"
)

const (
	maxRulesBody    = 1 << 20
	maxConsoleRules = 256
)

type rulesRequest struct {
	DryRun           bool   `json:"dry_run"`
	ExpectedRevision string `json:"expected_revision"`
	Rules            []Rule `json:"rules"`
}

type ruleChange struct {
	Name   string `json:"name"`
	Action string `json:"action"` // added | changed | removed
}

type ruleSummary struct {
	Added     int `json:"added"`
	Changed   int `json:"changed"`
	Removed   int `json:"removed"`
	Unchanged int `json:"unchanged"`
}

// decision is the policy-level observability decision for one subject,
// before runtime limits (policy.MergeObservability).
type decision struct {
	Traces          []TraceTarget `json:"traces"`
	Audit           []string      `json:"audit"`
	Content         string        `json:"content,omitempty"`
	ContentConflict bool          `json:"content_conflict,omitempty"`
}

type decisionChange struct {
	Subject Subject  `json:"subject"`
	Before  decision `json:"before"`
	After   decision `json:"after"`
}

type rulesResult struct {
	DryRun          bool             `json:"dry_run"`
	CurrentRevision string           `json:"current_revision"`
	ResultRevision  string           `json:"result_revision"`
	Changes         []ruleChange     `json:"changes"`
	Summary         ruleSummary      `json:"summary"`
	Decisions       []decisionChange `json:"decisions"`
}

// putRules is PUT /admin/observability/rules: it replaces the console-owned
// rules. An apply writes the console policy file and reloads the local file
// channel; when the reload fails the previous bytes are restored, so the file
// on disk matches the running policy set.
func (c *Console) putRules(w http.ResponseWriter, r *http.Request) {
	// Read-only check and body decode touch no shared state, so they run
	// before the lock: a stalled client must not block other edits.
	if reason := c.rulesReadOnly(); reason != "" {
		writeError(w, http.StatusConflict, apiError{Error: "console rules are read-only on this gateway", Code: "read_only", Reason: reason})
		return
	}
	var req rulesRequest
	if !decode(w, r, maxRulesBody, &req) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	current, currentBytes, existed, err := c.readConsole()
	if errors.Is(err, errForeign) {
		writeError(w, http.StatusConflict, apiError{Error: err.Error() + "; edit it by hand", Code: "console_file_foreign"})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, apiError{Error: "the console policy file could not be read", Code: "read_failed"})
		return
	}
	currentRev := otelexport.Revision(currentBytes)
	if !checkRevision(w, req.DryRun, req.ExpectedRevision, currentRev) {
		return
	}
	next, err := normalizeRules(req.Rules)
	if err != nil {
		writeError(w, http.StatusBadRequest, apiError{Error: err.Error(), Code: "invalid_rules"})
		return
	}
	if err := c.contentGate(current, next); err != nil {
		writeError(w, http.StatusBadRequest, apiError{Error: err.Error(), Code: "content_not_allowed"})
		return
	}
	nextBytes, candidate, err := encodeRules(next)
	if err != nil {
		writeError(w, http.StatusBadRequest, apiError{Error: err.Error(), Code: "invalid_rules"})
		return
	}
	live := c.opts.Policies.Policies()
	owned := map[string]bool{}
	for _, rule := range current {
		owned[rule.Name] = true
	}
	after := make([]*policy.Policy, 0, len(live)+len(candidate))
	taken := map[string]bool{}
	for _, p := range live {
		if !owned[p.Name] {
			after = append(after, p)
			taken[p.Name] = true
		}
	}
	for _, p := range candidate {
		if taken[p.Name] {
			writeError(w, http.StatusBadRequest, apiError{Error: fmt.Sprintf("rule %q: a policy with this name already exists in another policy file", p.Name), Code: "invalid_rules"})
			return
		}
	}
	after = append(after, candidate...)
	if err := c.opts.Policies.CheckLocal(after); err != nil {
		writeError(w, http.StatusBadRequest, apiError{Error: err.Error(), Code: "invalid_rules"})
		return
	}
	changes, summary := ruleChanges(current, next)
	res := rulesResult{DryRun: req.DryRun, CurrentRevision: currentRev, ResultRevision: otelexport.Revision(nextBytes),
		Changes: changes, Summary: summary, Decisions: decisionChanges(live, after)}
	if len(changes) == 0 {
		// A semantic no-op: the same rules in another byte layout change
		// nothing, so nothing is rewritten, reloaded or recorded.
		res.ResultRevision = currentRev
	}
	if req.DryRun || res.ResultRevision == currentRev {
		writeJSON(w, http.StatusOK, res)
		return
	}
	if !c.auditReady(w) {
		return
	}
	org, ok := c.organization(w, r)
	if !ok {
		return
	}
	persistErr, reloadErr, restoreErr := commitFile(c.opts.PolicyFile, currentBytes, existed, nextBytes, c.opts.Policies.Reload)
	if persistErr != nil {
		fmt.Fprintln(os.Stderr, "inferplane: observability console: persist rules:", persistErr)
		writeError(w, http.StatusInternalServerError, apiError{Error: "the console rules could not be saved; nothing changed", Code: "persist_failed"})
		return
	}
	if reloadErr != nil {
		fmt.Fprintln(os.Stderr, "inferplane: observability console: reload rules:", reloadErr)
		if restoreErr != nil {
			// Not policy_reload_failed: that code says the file was restored.
			fmt.Fprintln(os.Stderr, "inferplane: observability console: restore rules:", restoreErr)
			writeError(w, http.StatusInternalServerError, apiError{Error: "the policy set did not reload and the console file could not be restored: it may still hold the unapplied rules, and the policy watcher may install them later without a change record: " + reloadErr.Error(), Code: "restore_failed"})
			return
		}
		writeError(w, http.StatusConflict, apiError{Error: "the policy set did not reload, so the console file was restored: " + reloadErr.Error(), Code: "policy_reload_failed"})
		return
	}
	c.emitChange(r, org, "observability_rules", currentRev, res.ResultRevision, changedRules(changes))
	writeJSON(w, http.StatusOK, res)
}

// normalizeRules validates console rules and returns them sorted by name, with
// trace sample percents written explicitly (0 means 100).
func normalizeRules(in []Rule) ([]Rule, error) {
	if len(in) > maxConsoleRules {
		return nil, fmt.Errorf("at most %d console rules", maxConsoleRules)
	}
	out := make([]Rule, 0, len(in))
	seen := map[string]bool{}
	for _, r := range in {
		if !consoleNameRE.MatchString(r.Name) {
			return nil, fmt.Errorf("rule name %q must be console- followed by lowercase letters, digits and '-' (at most 63 characters)", r.Name)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("duplicate rule name %q", r.Name)
		}
		seen[r.Name] = true
		if r.Subject.Team == "" && r.Subject.User == "" {
			return nil, fmt.Errorf("rule %q: the subject must name a team and/or a user", r.Name)
		}
		for _, v := range []string{r.Subject.Team, r.Subject.User} {
			if len(v) > 256 || strings.ContainsFunc(v, unicode.IsControl) {
				return nil, fmt.Errorf("rule %q: subject values must be at most 256 bytes without control characters", r.Name)
			}
		}
		if strings.Contains(r.Subject.Team, "/") {
			return nil, fmt.Errorf("rule %q: the team must not contain '/'", r.Name)
		}
		if r.Traces == nil && r.Audit == nil && r.Content == nil {
			return nil, fmt.Errorf("rule %q: select at least one of traces, audit or content", r.Name)
		}
		n := Rule{Name: r.Name, Subject: r.Subject}
		if r.Traces != nil {
			t := *r.Traces
			if t.SamplePercent == 0 {
				t.SamplePercent = 100
			}
			if t.SamplePercent < 1 || t.SamplePercent > 100 {
				return nil, fmt.Errorf("rule %q: traces.sample_percent must be in [1, 100]", r.Name)
			}
			n.Traces = &t
		}
		if r.Audit != nil {
			a := *r.Audit
			n.Audit = &a
		}
		if r.Content != nil {
			ct := *r.Content
			n.Content = &ct
		}
		for _, name := range n.exporters() {
			if !policy.ValidExporterName(name) {
				return nil, fmt.Errorf("rule %q: exporter %q must be a declared exporter name (lowercase letters, digits, '-')", r.Name, name)
			}
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// exporters lists the exporter names r selects.
func (r Rule) exporters() []string {
	var out []string
	if r.Traces != nil {
		out = append(out, r.Traces.Exporter)
	}
	if r.Audit != nil {
		out = append(out, r.Audit.Exporter)
	}
	if r.Content != nil {
		out = append(out, r.Content.Exporter)
	}
	return out
}

// contentGate keeps content export a bootstrap decision: without
// observability.console.content_rules an edit may keep or remove an existing
// content selection, never add one or move it to another subject or exporter.
func (c *Console) contentGate(current, next []Rule) error {
	if c.opts.ContentRules {
		return nil
	}
	had := map[string]Rule{}
	for _, r := range current {
		if r.Content != nil {
			had[r.Name] = r
		}
	}
	for _, r := range next {
		if r.Content == nil {
			continue
		}
		prev, ok := had[r.Name]
		if !ok || prev.Subject != r.Subject || prev.Content.Exporter != r.Content.Exporter {
			return fmt.Errorf("rule %q: console rules cannot add or change content export unless observability.console.content_rules is true", r.Name)
		}
	}
	return nil
}

// doc is the GovernancePolicy document the console writes for r.
func (r Rule) doc() v1alpha1.GovernancePolicy {
	o := &v1alpha1.ObservabilityRule{}
	if r.Traces != nil {
		pct := r.Traces.SamplePercent
		o.Traces = &v1alpha1.TraceExport{Exporter: r.Traces.Exporter, SamplePercent: &pct}
	}
	if r.Audit != nil {
		o.Audit = &v1alpha1.SignalExport{Exporter: r.Audit.Exporter}
	}
	if r.Content != nil {
		o.Content = &v1alpha1.SignalExport{Exporter: r.Content.Exporter}
	}
	return v1alpha1.GovernancePolicy{
		TypeMeta: v1alpha1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.KindGovernancePolicy},
		Metadata: v1alpha1.ObjectMeta{Name: r.Name},
		Spec: v1alpha1.GovernancePolicySpec{
			Subject: v1alpha1.Subject{Team: r.Subject.Team, User: r.Subject.User},
			Rules:   []v1alpha1.Rule{{Name: consoleRuleName, FailurePolicy: v1alpha1.FailOpen, Observability: o}},
		},
	}
}

// encodeRules renders normalized rules as the console policy file: one
// document per rule in name order, "---" between documents, and zero bytes for
// no rules (an empty file is a valid policy file). Every document goes through
// policy.FromV1Alpha1, the loader's own validation.
func encodeRules(rules []Rule) ([]byte, []*policy.Policy, error) {
	var buf bytes.Buffer
	policies := make([]*policy.Policy, 0, len(rules))
	for i, r := range rules {
		doc := r.doc()
		p, err := policy.FromV1Alpha1(&doc)
		if err != nil {
			return nil, nil, fmt.Errorf("rule %q: %w", r.Name, err)
		}
		y, err := sigyaml.Marshal(doc)
		if err != nil {
			return nil, nil, fmt.Errorf("rule %q: %w", r.Name, err)
		}
		if i > 0 {
			buf.WriteString("---\n")
		}
		buf.Write(y)
		policies = append(policies, p)
	}
	return buf.Bytes(), policies, nil
}

// ruleChanges diffs console rules by name.
func ruleChanges(current, next []Rule) ([]ruleChange, ruleSummary) {
	prev := map[string]string{}
	for _, r := range current {
		b, _ := json.Marshal(r)
		prev[r.Name] = string(b)
	}
	out := []ruleChange{}
	var sum ruleSummary
	seen := map[string]bool{}
	for _, r := range next {
		seen[r.Name] = true
		b, _ := json.Marshal(r)
		old, ok := prev[r.Name]
		switch {
		case !ok:
			out = append(out, ruleChange{Name: r.Name, Action: "added"})
			sum.Added++
		case old != string(b):
			out = append(out, ruleChange{Name: r.Name, Action: "changed"})
			sum.Changed++
		default:
			sum.Unchanged++
		}
	}
	for _, r := range current {
		if !seen[r.Name] {
			out = append(out, ruleChange{Name: r.Name, Action: "removed"})
			sum.Removed++
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, sum
}

// decisionChanges lists, for every subject an observability rule names before
// or after the edit, the policy decision when it differs.
func decisionChanges(before, after []*policy.Policy) []decisionChange {
	subjects := map[Subject]bool{}
	for _, set := range [][]*policy.Policy{before, after} {
		for _, p := range set {
			for _, r := range p.Rules {
				if r.Observability != nil {
					subjects[Subject{Team: p.Subject.Team, User: p.Subject.User}] = true
				}
			}
		}
	}
	keys := make([]Subject, 0, len(subjects))
	for s := range subjects {
		keys = append(keys, s)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Team != keys[j].Team {
			return keys[i].Team < keys[j].Team
		}
		return keys[i].User < keys[j].User
	})
	out := []decisionChange{}
	for _, s := range keys {
		b := decisionOf(policy.MergeObservability(before, s.Team, s.User))
		a := decisionOf(policy.MergeObservability(after, s.Team, s.User))
		if !reflect.DeepEqual(b, a) {
			out = append(out, decisionChange{Subject: s, Before: b, After: a})
		}
	}
	return out
}

func decisionOf(d policy.ObservabilityDecision) decision {
	out := decision{Traces: []TraceTarget{}, Audit: append([]string{}, d.Audit...), Content: d.Content, ContentConflict: d.ContentConflict}
	for _, t := range d.Traces {
		out.Traces = append(out.Traces, TraceTarget{Exporter: t.Exporter, SamplePercent: t.SamplePercent})
	}
	return out
}

func changedRules(changes []ruleChange) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		out = append(out, c.Name)
	}
	return out
}
