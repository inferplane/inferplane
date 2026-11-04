package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/server"
	"github.com/inferplane/inferplane/internal/server/observabilityapi"
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

// buildObservability resolves header refs, starts the log pipelines and
// installs the runtime limits persisted in observability.console.state_file.
// A nil Set (no observability block) is valid. A state file that does not
// parse fails boot: a pause must not silently lapse on restart.
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
			Signals: e.Signals, DataBoundary: e.DataBoundary, MaxContentBytes: e.ContentLimit(), QueueBytes: e.QueueLimit(),
		})
	}
	set, err := otelexport.New(o.ServiceName, exporters, m)
	if err != nil {
		return nil, err
	}
	if c := o.Console; c != nil && c.StateFile != "" {
		notes, err := observabilityapi.LoadLimits(c.StateFile, set)
		if err != nil {
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // nothing is queued yet: stop the pipelines without waiting
			_ = set.Shutdown(ctx)
			return nil, err
		}
		for _, n := range notes {
			fmt.Fprintln(os.Stderr, "inferplane: observability state file:", n)
		}
	}
	return set, nil
}

// auditTargets selects audit-copy exporters for a record's subject: the
// policy selection after the runtime limits (Set.AuditTargets counts it).
func auditTargets(set *otelexport.Set, pol *policy.Store) func(team, user string) []string {
	return func(team, user string) []string { return set.AuditTargets(pol.Observability(team, user).Audit) }
}

// contentExport copies egress content to the subject's content exporter,
// unless matching rules disagree or a runtime limit withholds it.
func contentExport(set *otelexport.Set, pol *policy.Store) requestpolicy.ContentExport {
	return func(_ context.Context, c requestpolicy.ContentRecord) {
		d := pol.Observability(c.Team, c.Account)
		exporter := set.ContentTarget(d.Content, d.ContentConflict)
		if exporter == "" {
			return
		}
		set.EmitContent(exporter, otelexport.Content{RecordID: c.RecordID, Team: c.Team, Ingress: c.Ingress,
			Model: c.Model, Upstream: c.Upstream, TraceID: c.TraceID, Request: c.Request, Response: c.Response})
	}
}

// traceDecider unions the legacy `otel` baseline (when configured) with the
// governed trace targets of the request's principal, after the runtime limits.
func traceDecider(set *otelexport.Set, pol *policy.Store, legacy *tracing.Target) tracing.Decider {
	return func(ctx context.Context) []tracing.Target {
		var out []tracing.Target
		if legacy != nil {
			out = append(out, *legacy)
		}
		p, ok := principal.From(ctx)
		if !ok {
			return out
		}
		var selected []otelexport.TraceSelection
		for _, t := range pol.Observability(p.Team, p.AccountSubject()).Traces {
			selected = append(selected, otelexport.TraceSelection{Exporter: t.Exporter, SamplePercent: t.SamplePercent})
		}
		for _, t := range set.TraceTargets(selected) {
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
	var observe tracing.Observer
	if set != nil {
		observe = set.ObserveTrace // span counts for the console's traces rows
	}
	return tracing.InitExporters(ctx, service, exporters, traceDecider(set, pol, legacy), observe)
}

// contentOption wires governed content export when an exporter allows it.
func contentOption(set *otelexport.Set, pol *policy.Store) server.DataMuxOption {
	if !set.Has(otelexport.SignalContent) {
		return server.WithContentExport(nil)
	}
	return server.WithContentExport(contentExport(set, pol))
}

// ensureConsolePolicyFile creates observability.console.policy_file empty
// (0600) when it does not exist yet, so the local file channel, which lists it
// in `policies`, can load it on first boot. An existing file is untouched.
func ensureConsolePolicyFile(cfg *config.Config) error {
	if cfg.Observability == nil || cfg.Observability.Console == nil || cfg.Observability.Console.PolicyFile == "" {
		return nil
	}
	f, err := os.OpenFile(cfg.Observability.Console.PolicyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create console policy file: %w", err)
	}
	return f.Close()
}

// observabilityConsole builds the full-admin observability console API. It is
// mounted even without an observability block, so the view can say what is
// missing instead of failing with 404. keys supplies the verified-identity
// organization so change records carry digest identity when it is configured;
// the audit writer's instance id names the answering process in the status.
func observabilityConsole(cfg *config.Config, set *otelexport.Set, pol *policy.Store, aud *audit.Writer, keys keystore.Store) *observabilityapi.Console {
	opts := observabilityapi.Options{Set: set, PolicySource: observabilityapi.SourceNone, Emit: aud.Append, AuditReady: aud.Ready}
	if aud != nil {
		opts.Instance = aud.Instance() // the same id every audit record of this process carries
	}
	opts.Organization = func(ctx context.Context) (string, error) {
		c, err := principal.IdentityConfig(ctx, keys)
		return c.Organization, err
	}
	switch {
	case cfg.ControlPlane != nil:
		opts.PolicySource = observabilityapi.SourceControlPlane
	case len(cfg.Policies) > 0:
		opts.PolicySource = observabilityapi.SourceFiles
	}
	if pol != nil {
		opts.Policies = pol // a nil *policy.Store must stay a nil interface
	}
	if o := cfg.Observability; o != nil && o.Console != nil {
		opts.StateFile, opts.PolicyFile, opts.ContentRules = o.Console.StateFile, o.Console.PolicyFile, o.Console.ContentRules
	}
	if o := cfg.OTel; o != nil {
		protocol := o.Protocol
		if protocol == "" {
			protocol = "http"
		}
		opts.Legacy = &observabilityapi.LegacyExporter{Endpoint: o.Endpoint, Protocol: protocol, TLS: !o.Insecure,
			SampleRatio: tracing.LegacyTarget(o.SampleRatio).Ratio}
	}
	return observabilityapi.New(opts)
}
