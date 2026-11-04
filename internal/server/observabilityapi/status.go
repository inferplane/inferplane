package observabilityapi

import (
	"errors"
	"net/http"
	"sort"

	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/policy"
)

// editState says whether one editor is usable and, when it is, which
// revision an apply must name. Reason is why not: not_configured,
// no_state_file, no_policy_file, control_plane, foreign_documents,
// read_failed.
type editState struct {
	Editable bool   `json:"editable"`
	Reason   string `json:"reason,omitempty"`
	Revision string `json:"revision,omitempty"`
}

type rulesState struct {
	editState
	ContentRules bool   `json:"content_rules"`
	Console      []Rule `json:"console"`
}

type legacyStatus struct {
	LegacyExporter
	// Stats is absent without an observability block: nothing counts the
	// legacy exporter then, and a zero row would misreport live traffic.
	Stats *otelexport.SignalStats `json:"stats,omitempty"`
}

// Selection is one observability rule the gateway enforces, from any source.
type Selection struct {
	Policy  string   `json:"policy"`
	Rule    string   `json:"rule"`
	Source  string   `json:"source"` // console | files | control_plane
	Subject Subject  `json:"subject"`
	Targets []Target `json:"targets"`
}

// Target is one signal → exporter selection and what the limits do with it.
type Target struct {
	Signal           string `json:"signal"`
	Exporter         string `json:"exporter"`
	SamplePercent    int    `json:"sample_percent,omitempty"`
	EffectivePercent int    `json:"effective_percent,omitempty"`
	Exported         bool   `json:"exported"`
}

type statusResponse struct {
	Configured    bool                        `json:"configured"`
	Instance      string                      `json:"instance,omitempty"` // the answering process (Options.Instance)
	PolicySource  string                      `json:"policy_source"`
	ServiceName   string                      `json:"service_name,omitempty"`
	CountersSince string                      `json:"counters_since,omitempty"`
	Limits        editState                   `json:"limits"`
	Rules         rulesState                  `json:"rules"`
	Exporters     []otelexport.ExporterStatus `json:"exporters"`
	LegacyOTel    *legacyStatus               `json:"legacy_otel,omitempty"`
	Decisions     []otelexport.DecisionStats  `json:"decisions"`
	Selections    []Selection                 `json:"selections"`
}

func (c *Console) status(w http.ResponseWriter, _ *http.Request) {
	st := c.opts.Set.Status()
	resp := statusResponse{
		Configured: c.opts.Set != nil, Instance: c.opts.Instance, PolicySource: c.source(),
		ServiceName: st.ServiceName, CountersSince: st.CountersSince,
		Limits: c.limitsState(), Rules: c.rulesState(),
		Exporters: st.Exporters, Decisions: st.Decisions,
	}
	if l := c.opts.Legacy; l != nil {
		resp.LegacyOTel = &legacyStatus{LegacyExporter: *l}
		if c.opts.Set != nil {
			stats := c.opts.Set.SignalStats(legacyExporter, otelexport.SignalTraces)
			resp.LegacyOTel.Stats = &stats
		}
	}
	console := map[string]bool{}
	for _, r := range resp.Rules.Console {
		console[r.Name] = true
	}
	resp.Selections = c.selections(c.opts.Set.Limits(), console)
	writeJSON(w, http.StatusOK, resp)
}

func (c *Console) limitsState() editState {
	switch {
	case c.opts.Set == nil:
		return editState{Reason: "not_configured"}
	case c.opts.StateFile == "":
		return editState{Reason: "no_state_file", Revision: otelexport.Revision(otelexport.EncodeLimits(c.opts.Set.Limits()))}
	}
	return editState{Editable: true, Revision: otelexport.Revision(otelexport.EncodeLimits(c.opts.Set.Limits()))}
}

// rulesReadOnly is why console rules cannot be edited here, or "".
func (c *Console) rulesReadOnly() string {
	switch {
	case c.opts.Set == nil:
		return "not_configured"
	case c.source() == SourceControlPlane:
		return "control_plane"
	case c.opts.PolicyFile == "" || c.opts.Policies == nil:
		return "no_policy_file"
	}
	return ""
}

func (c *Console) rulesState() rulesState {
	out := rulesState{ContentRules: c.opts.ContentRules, Console: []Rule{}}
	if reason := c.rulesReadOnly(); reason != "" {
		out.Reason = reason
		return out
	}
	rules, b, _, err := c.readConsole()
	switch {
	case errors.Is(err, errForeign):
		out.Reason = "foreign_documents"
	case err != nil:
		out.Reason = "read_failed"
	default:
		out.Editable, out.Revision, out.Console = true, otelexport.Revision(b), rules
	}
	return out
}

// selections lists every observability rule in the policy store under lim.
// Documents named in console come from the console file.
func (c *Console) selections(lim otelexport.Limits, console map[string]bool) []Selection {
	out := []Selection{}
	if c.opts.Policies == nil {
		return out
	}
	for _, p := range c.opts.Policies.Policies() {
		source := c.source()
		if console[p.Name] {
			source = sourceConsole
		}
		for _, r := range p.Rules {
			if r.Observability == nil {
				continue
			}
			out = append(out, Selection{Policy: p.Name, Rule: r.Name, Source: source,
				Subject: Subject{Team: p.Subject.Team, User: p.Subject.User}, Targets: c.targets(lim, r.Observability)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Policy != out[j].Policy {
			return out[i].Policy < out[j].Policy
		}
		return out[i].Rule < out[j].Rule
	})
	return out
}

// targets reports each selection of one rule under lim, in signal order.
func (c *Console) targets(lim otelexport.Limits, o *policy.Observability) []Target {
	set := c.opts.Set
	var out []Target
	if o.Traces != nil {
		out = append(out, Target{Signal: policy.SignalTraces, Exporter: o.Traces.Exporter, SamplePercent: o.Traces.SamplePercent,
			EffectivePercent: set.SamplePercent(lim, o.Traces.Exporter, o.Traces.SamplePercent),
			Exported:         set.Exporting(lim, o.Traces.Exporter, policy.SignalTraces)})
	}
	if o.Audit != "" {
		out = append(out, Target{Signal: policy.SignalAudit, Exporter: o.Audit, Exported: set.Exporting(lim, o.Audit, policy.SignalAudit)})
	}
	if o.Content != "" {
		out = append(out, Target{Signal: policy.SignalContent, Exporter: o.Content, Exported: set.Exporting(lim, o.Content, policy.SignalContent)})
	}
	return out
}
