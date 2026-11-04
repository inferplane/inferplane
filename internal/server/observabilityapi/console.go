package observabilityapi

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/policy"
)

// consoleRuleName is the single rule of every console document.
const consoleRuleName = "export"

// consoleNameRE is a console document name: "console-" and a DNS-label tail,
// at most 63 characters in total.
var consoleNameRE = regexp.MustCompile(`^console-[a-z0-9]([-a-z0-9]{0,53}[a-z0-9])?$`)

// errForeign marks a console policy file that holds something other than
// console documents (a hand edit, or another tool): the console keeps it
// read-only rather than overwrite it.
var errForeign = errors.New("the console policy file holds documents the console does not manage")

// Subject is a console rule's policy subject.
type Subject struct {
	Team string `json:"team,omitempty"`
	User string `json:"user,omitempty"`
}

// TraceTarget exports the subject's spans to Exporter at SamplePercent (1..100).
type TraceTarget struct {
	Exporter      string `json:"exporter"`
	SamplePercent int    `json:"sample_percent"`
}

// SignalTarget exports one log signal to Exporter.
type SignalTarget struct {
	Exporter string `json:"exporter"`
}

// Rule is one console-owned selection, stored as a GovernancePolicy document
// with a single FailOpen observability rule named "export".
type Rule struct {
	Name    string        `json:"name"`
	Subject Subject       `json:"subject"`
	Traces  *TraceTarget  `json:"traces,omitempty"`
	Audit   *SignalTarget `json:"audit,omitempty"`
	Content *SignalTarget `json:"content,omitempty"`
}

// ruleFromDoc reads a console document back; any other shape is errForeign.
func ruleFromDoc(d v1alpha1.GovernancePolicy) (Rule, error) {
	if !consoleNameRE.MatchString(d.Metadata.Name) || len(d.Spec.Rules) != 1 {
		return Rule{}, errForeign
	}
	wr := d.Spec.Rules[0]
	if wr.Name != consoleRuleName || wr.FailurePolicy != v1alpha1.FailOpen || wr.Observability == nil ||
		wr.Budget != nil || wr.Routing != nil || wr.ModelAccess != nil || wr.Rate != nil || wr.SensitiveData != nil || wr.TokenQuota != nil {
		return Rule{}, errForeign
	}
	r := Rule{Name: d.Metadata.Name, Subject: Subject{Team: d.Spec.Subject.Team, User: d.Spec.Subject.User}}
	o := wr.Observability
	if o.Traces != nil {
		pct := 100
		if o.Traces.SamplePercent != nil {
			pct = *o.Traces.SamplePercent
		}
		r.Traces = &TraceTarget{Exporter: o.Traces.Exporter, SamplePercent: pct}
	}
	if o.Audit != nil {
		r.Audit = &SignalTarget{Exporter: o.Audit.Exporter}
	}
	if o.Content != nil {
		r.Content = &SignalTarget{Exporter: o.Content.Exporter}
	}
	return r, nil
}

// readConsole parses the console policy file into rules sorted by name and
// returns its bytes, the rules revision input, and whether the file existed.
// A missing file is no rules. A read or parse failure is logged once per call
// (one per request) with the path, never the file's content.
func (c *Console) readConsole() ([]Rule, []byte, bool, error) {
	b, err := os.ReadFile(c.opts.PolicyFile)
	if errors.Is(err, fs.ErrNotExist) {
		return []Rule{}, nil, false, nil
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "inferplane: observability console: read console policy file:", err) // a path error: path and errno only
		return nil, nil, false, err
	}
	docs, err := policy.ParseWireDocs(b)
	if err != nil {
		// The parser's text can quote file content, so the log names the
		// path only; the admin's PUT answer carries the parse error.
		fmt.Fprintf(os.Stderr, "inferplane: observability console: console policy file %s does not parse; the console keeps it read-only\n", c.opts.PolicyFile)
		return nil, nil, false, fmt.Errorf("%w: %v", errForeign, err)
	}
	rules := make([]Rule, 0, len(docs))
	for _, d := range docs {
		r, err := ruleFromDoc(d)
		if err != nil {
			return nil, nil, false, err
		}
		rules = append(rules, r)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	return rules, b, true, nil
}
