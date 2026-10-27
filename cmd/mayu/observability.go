package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/server"
	"github.com/inferplane/inferplane/internal/server/requestpolicy"
	"github.com/inferplane/inferplane/internal/tracing"
)

// Governed observability assembly. The local `observability.exporters`
// ceiling becomes an otelexport.Set; GovernancePolicy observability rules (from
// local files or the control plane) choose, per subject, which declared
// exporters receive traces, audit copies and egress content. The policy store
// checks every rule against Set.Allows at apply time, including when the block
// is absent: a nil Set allows nothing, so a distributed rule is refused and
// reported upstream instead of being accepted and silently exporting nothing.

// buildObservability resolves header refs and starts the log pipelines. A nil
// Set (no observability block) is valid.
func buildObservability(cfg *config.Config, m *metrics.Metrics) (*otelexport.Set, error) {
	o := cfg.Observability
	if o == nil {
		return nil, nil
	}
	names := make([]string, 0, len(o.Exporters))
	for name := range o.Exporters {
		names = append(names, name)
	}
	sort.Strings(names)
	exporters := make([]otelexport.Exporter, 0, len(names))
	for _, name := range names {
		e := o.Exporters[name]
		headers := make(map[string]string, len(e.Headers))
		for h, ref := range e.Headers {
			v, err := config.ResolveSecretRef(ref)
			if err != nil {
				return nil, fmt.Errorf("observability exporter %q header %q: %w", name, h, err)
			}
			headers[h] = v
		}
		exporters = append(exporters, otelexport.Exporter{
			Name: name, Endpoint: e.Endpoint, Protocol: e.Protocol, Insecure: e.Insecure, Headers: headers,
			Signals: e.Signals, MaxContentBytes: e.ContentLimit(), QueueBytes: e.QueueLimit(),
		})
	}
	return otelexport.New(o.ServiceName, exporters, m)
}

// auditTargets selects audit-copy exporters for a record's subject.
func auditTargets(pol *policy.Store) func(team, user string) []string {
	return func(team, user string) []string { return pol.Observability(team, user).Audit }
}

// contentExport copies egress content to the subject's content exporter.
func contentExport(set *otelexport.Set, pol *policy.Store) requestpolicy.ContentExport {
	return func(_ context.Context, c requestpolicy.ContentRecord) {
		exporter := pol.Observability(c.Team, c.Account).Content
		if exporter == "" {
			return
		}
		set.EmitContent(exporter, otelexport.Content{RecordID: c.RecordID, Team: c.Team, Ingress: c.Ingress,
			Model: c.Model, Upstream: c.Upstream, TraceID: c.TraceID, Request: c.Request, Response: c.Response})
	}
}

// traceDecider unions the legacy `otel` baseline (when configured) with the
// governed trace targets of the request's principal.
func traceDecider(pol *policy.Store, legacy *tracing.Target) tracing.Decider {
	return func(ctx context.Context) []tracing.Target {
		var out []tracing.Target
		if legacy != nil {
			out = append(out, *legacy)
		}
		p, ok := principal.From(ctx)
		if !ok {
			return out
		}
		for _, t := range pol.Observability(p.Team, p.AccountSubject()).Traces {
			out = append(out, tracing.Target{Exporter: t.Exporter, Ratio: float64(t.SamplePercent) / 100})
		}
		return out
	}
}

// initTracing installs routed tracing over the legacy `otel` exporter and every
// declared exporter that allows traces. It returns nil when neither exists.
func initTracing(ctx context.Context, cfg *config.Config, set *otelexport.Set, pol *policy.Store) (func(context.Context) error, error) {
	exporters := map[string]tracing.ExporterConfig{}
	var legacy *tracing.Target
	service := ""
	if o := cfg.OTel; o != nil {
		exporters[tracing.LegacyExporter] = tracing.ExporterConfig{Endpoint: o.Endpoint, Protocol: o.Protocol, Insecure: o.Insecure}
		t := tracing.LegacyTarget(o.SampleRatio)
		legacy = &t
		service = o.ServiceName
	}
	for _, e := range set.TraceExporters() {
		exporters[e.Name] = tracing.ExporterConfig{Endpoint: e.Endpoint, Protocol: e.Protocol, Insecure: e.Insecure, Headers: e.Headers}
	}
	if cfg.Observability != nil && cfg.Observability.ServiceName != "" {
		service = cfg.Observability.ServiceName
	}
	if len(exporters) == 0 {
		return nil, nil
	}
	return tracing.InitExporters(ctx, service, exporters, traceDecider(pol, legacy))
}

// contentOption wires governed content export when an exporter allows it.
func contentOption(set *otelexport.Set, pol *policy.Store) server.DataMuxOption {
	if !set.Has(otelexport.SignalContent) {
		return server.WithContentExport(nil)
	}
	return server.WithContentExport(contentExport(set, pol))
}
