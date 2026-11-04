# Governed Observability Export Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let GovernancePolicy decide, per team/user, whether mayu exports traces, audit-record copies and egress content over OTLP, and to which exporter — while the exporters themselves (endpoint, credentials, TLS, allowed signals) stay pinned in mayu's local config, and the control plane keeps receiving only its existing dedicated non-OTel channels.

**Architecture:** mayu config gains an `observability.exporters` ceiling. A new `observability` rule kind names exporters per signal; the data plane rejects (and reports upstream) any rule naming an exporter or signal outside its ceiling, so a compromised control plane can never add a destination. Traces use one always-recording TracerProvider whose routing span processor forwards each span to the exporters its request's decision selects (sampling is a gateway-side dice roll, so a client `traceparent` cannot suppress or force export). Audit copies and content go out as OTLP log records through a bounded, non-blocking queue per exporter; the hash-chained WAL stays the evidence of record.

**Tech Stack:** Go 1.26, `go.opentelemetry.io/otel` SDK v1.45 (traces), `go.opentelemetry.io/proto/otlp` v1.11 + `google.golang.org/protobuf` + `google.golang.org/grpc` (already in the module graph; OTLP logs are built from the proto types — no new module downloads), Prometheus client for drop/sent counters.

**Spec:** this plan carries the design (conversation of 2026-10-27, summarized in "Decisions" below). An ADR recording the reversal of ADR-011's "traces are the only OTLP signal" stance is proposed as `ADR-056` but is not created by this plan.

## Decisions

1. **Two channels, different guarantees.** Governance data (consumption reports, leases, authority, `/v1alpha1/usage` windows) stays on the existing dedicated channels to inferplaned with store-then-ack semantics. Observability (traces, audit copies, content) goes mayu → OTLP collector directly; inferplaned is not on that path.
2. **Local ceiling, central choice.** Exporter endpoint, protocol, TLS, header credentials (`env:`/`file:` refs), allowed signals, data boundary and queue limits are local config only. A policy may only *select among* declared exporters for signals each exporter allows. This is a permission-boundary model: policy can narrow, never widen.
3. **Observability rules are best-effort.** `failurePolicy` must be `FailOpen`; export failures never fail a request. Required evidence remains the local audit chain (WAL + required sinks + optional S3 anchor).
4. **Rejection semantics.** On CP distribution, a document naming an undeclared exporter/signal is rejected per document (existing `ApplyWire` posture) and reported in `Rejections`; on the local file channel the load fails whole (existing posture). Operators keep observability rules in their own documents so a rejection never takes budgets down with it (documented).
5. **Merge semantics across matching policies.** Traces: union of exporters, sample percent = max per exporter. Audit: union of exporters. Content: exported only when every matching rule that names content names the same single exporter; any disagreement disables content for that request (privacy-first).
6. **Content export is the egress body.** It is the same bytes body capture (ADR-018) stores: after routing, masking and reinspection, only for requests that reached a provider. Content-capable exporters must declare `data_boundary: internal` and use TLS unless the endpoint is loopback.
7. **Sampling ignores the client.** The SDK sampler records every span; the routing processor decides export with a gateway-side random draw per target. Incoming `traceparent` still joins the client's trace id for correlation.
8. **Legacy `otel` block keeps working** as a local baseline exporter named `otel` (traces only, at `sample_ratio`), unioned with governed targets. The name `otel` is reserved inside `observability.exporters`.

## Global Constraints

- `CGO_ENABLED=0`; every dependency pure Go; no new module beyond those already in `go.sum`.
- Secrets only via `env:`/`file:` refs (`config.SecretRef`); header values never appear in logs, errors, metrics or audit.
- `/metrics` labels: exporter name (config-bounded), signal (fixed set), outcome (fixed set). No team, user, key id.
- Audit canonical bytes for existing records must be byte-identical (hash chain compatibility).
- Request path must never block on export: enqueue is non-blocking and bounded by bytes.
- Builds/tests under `/home/atomoh/.tmp` (`TMPDIR=/home/atomoh/.tmp`, `GOTMPDIR=/home/atomoh/.tmp/gotmp`); never pipe test output to head/tail; redirect to a log and grep.
- Commits signed off (`git commit -s`) with the session's Co-Authored-By trailer.
- Do not hand-edit `AGENTS.md`; do not create the ADR file.

## Review Focus

1. **Collector down or slow for a long time** → request latency unchanged, audit writer loop never blocks, memory bounded by `queue_bytes`; drops counted. Test: Task 5 `TestQueueDropsWhenFullWithoutBlocking`, Task 7 `TestAuditSinkNeverBlocksWriter`.
2. **Client-controlled `traceparent`** (sampled flag `00`/`01`, chosen trace ids) → export decision unchanged. Test: Task 6 `TestClientTraceparentDoesNotControlExport`.
3. **CP rule naming an undeclared exporter, or content on a traces-only exporter** → that document rejected with a reason, other documents still apply, nothing exported for it. Test: Task 1 `TestApplyWireRejectsObservabilityOutsideCeiling`.
4. **Multi-MiB request bodies with content export on** → oversize records dropped and counted, no unbounded buffering. Test: Task 5 `TestContentOversizeDropped`.
5. **Shutdown with queued records** → bounded flush, no hang past the context deadline. Test: Task 5 `TestShutdownHonorsDeadline`.

---

## File Structure

| File | Responsibility |
|---|---|
| `api/v1alpha1/types.go` | Wire `ObservabilityRule`, `TraceExport`, `SignalExport`; `Rule.Observability` |
| `internal/policy/observability.go` (new) | Internal form, conversion/validation, signal constants, `ObservabilityDecision`, merge |
| `internal/policy/policy.go` | `Rule.Observability`, one-kind check, dispatch to converter |
| `internal/policy/store.go` | `SetObservabilityCeiling`, ceiling check in `checkEnforceable`, `Store.Observability` |
| `deploy/crd/inferplane.dev_governancepolicies.yaml` | Schema + CEL for the new kind |
| `internal/controlplane/ui/static/app.js` | Open observability rules in the raw editor (never misread as another kind) |
| `internal/config/observability.go` (new) | `ObservabilityConfig`, `ObservabilityExporter`, validation |
| `internal/config/config.go` | `Config.Observability`, call validation |
| `internal/metrics/metrics.go` | `inferplane_otel_export_records_total{exporter,signal,outcome}` |
| `internal/otelexport/` (new) | Declared exporter set (`Allows`), OTLP logs clients (HTTP protobuf, gRPC), bounded queue + batch worker, audit sink, content emit |
| `internal/tracing/tracing.go`, `internal/tracing/route.go` (new) | `NewExporter` with headers/TLS, routing processor, `InitRouted`, legacy `Init` on top |
| `internal/audit/sink.go`, `writer.go`, `record.go` | Optional `RecordSink`; `PrincipalRef.Account` (`json:"-"`) |
| `internal/principal/principal.go` | Fill `Account` in `AuditRef` |
| `internal/server/requestpolicy/content.go` (new) | `ContentRecord`, `ContentExport` hook type |
| `internal/server/server.go` + 4 ingress handlers | `WithContentExport` option, setters, call next to body capture |
| `cmd/mayu/observability.go` (new), `gateway.go` | Assembly: build set, ceiling, trace routing, audit sink, content hook, shutdown |
| `cmd/mayu/observability_acceptance_test.go` (new) | End-to-end with a fake OTLP collector |
| Docs | `internal/CLAUDE.md`, `cmd/CLAUDE.md`, `docs/reference/agent-llm.md` (observability section), `examples/` |

---

### Task 1: Policy schema, validation, ceiling check and decision

**Files:**
- Modify: `api/v1alpha1/types.go` (Rule + new types)
- Create: `internal/policy/observability.go`
- Modify: `internal/policy/policy.go` (Rule field, kinds count/message, switch case)
- Modify: `internal/policy/store.go` (ceiling field/setter, `checkEnforceable` param, `Observability`)
- Test: `internal/policy/observability_test.go`

**Interfaces:**
- Produces:
  - `policy.SignalTraces = "traces"`, `policy.SignalAudit = "audit"`, `policy.SignalContent = "content"`
  - `type policy.Observability struct { Traces *TraceExport; Audit, Content string }`
  - `type policy.TraceExport struct { Exporter string; SamplePercent int }` (1..100, never 0)
  - `type policy.ObservabilityDecision struct { Traces []TraceExport; Audit []string; Content string }`
  - `func (s *Store) SetObservabilityCeiling(f func(exporter, signal string) error)`
  - `func (s *Store) Observability(team, user string) ObservabilityDecision`

Wire shape:

```go
// ObservabilityRule selects governed OTLP export for the subject's requests.
// Exporters are names from the data plane's local observability ceiling; a
// policy can only choose among them (ADR-056 proposal). Export is best-effort,
// so the rule requires FailOpen.
type ObservabilityRule struct {
	Traces  *TraceExport  `json:"traces,omitempty"`
	Audit   *SignalExport `json:"audit,omitempty"`
	Content *SignalExport `json:"content,omitempty"`
}

// TraceExport exports the subject's request spans. SamplePercent is 1..100;
// omitted means 100.
type TraceExport struct {
	Exporter      string `json:"exporter"`
	SamplePercent *int   `json:"samplePercent,omitempty"`
}

// SignalExport exports one log signal (audit copies or egress content).
type SignalExport struct {
	Exporter string `json:"exporter"`
}
```

- [ ] **Step 1: Write failing tests** — conversion accepts a full rule; rejects FailClosed, empty rule, empty/invalid exporter name (`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`), samplePercent 0/101; one-kind rule still enforced; `ApplyWire` with a ceiling that allows only `corp/traces` rejects a doc naming `corp` for content and a doc naming `other`, accepts a budget doc in the same set, reports rule names; merge: two team/user matches union traces with max percent, union audit, content agreement exported, disagreement disabled; no observability rules → zero decision.

```go
func TestApplyWireRejectsObservabilityOutsideCeiling(t *testing.T) {
	s := NewEmptyStore()
	s.SetObservabilityCeiling(func(exporter, signal string) error {
		if exporter == "corp" && signal == SignalTraces {
			return nil
		}
		return fmt.Errorf("exporter %q does not allow %s", exporter, signal)
	})
	docs := []v1alpha1.GovernancePolicy{
		obsDoc("content-on-traces-only", "eng", "", &v1alpha1.ObservabilityRule{Content: &v1alpha1.SignalExport{Exporter: "corp"}}),
		obsDoc("unknown-exporter", "eng", "", &v1alpha1.ObservabilityRule{Traces: &v1alpha1.TraceExport{Exporter: "other"}}),
		obsDoc("ok", "eng", "", &v1alpha1.ObservabilityRule{Traces: &v1alpha1.TraceExport{Exporter: "corp"}}),
		budgetDoc("money", "eng"),
	}
	rej := s.ApplyWire(docs)
	if len(rej) != 2 || rej[0].Policy != "content-on-traces-only" || rej[1].Policy != "unknown-exporter" {
		t.Fatalf("rejections = %+v", rej)
	}
	if _, ok := s.TeamLimits("eng"); !ok {
		t.Fatal("budget document in the same distribution must still apply")
	}
	got := s.Observability("eng", "")
	if len(got.Traces) != 1 || got.Traces[0] != (TraceExport{Exporter: "corp", SamplePercent: 100}) || got.Content != "" {
		t.Fatalf("decision = %+v", got)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/policy/ -run 'Observability' -count=1 > $LOG 2>&1; grep -E '^(ok|FAIL|---)' $LOG` → FAIL (undefined).
- [ ] **Step 3: Implement** wire types; `observabilityFromV1Alpha1`; add `wr.Observability != nil` to the kinds list and message; `checkEnforceable(p, routedAndPriced, ceiling, shared)` checks each named `(exporter, signal)` when `ceiling != nil`; `snapshot.observability bool` precomputed so `Observability` returns fast when no rule exists; merge in sorted, deterministic order.
- [ ] **Step 4: Run** policy tests (whole package, `-race`) → PASS.
- [ ] **Step 5: Commit** `feat(policy): observability rule kind with local exporter ceiling`.

### Task 2: CRD and console

**Files:**
- Modify: `deploy/crd/inferplane.dev_governancepolicies.yaml`
- Modify: `internal/controlplane/ui/static/app.js`
- Test: native CRD validation; existing `internal/policy/crd_test.go`; controlplane UI tests.

- [ ] **Step 1:** Add `observability` object schema (traces.exporter string pattern, samplePercent integer 1..100; audit/content exporter); extend the exactly-one CEL list and message; add `!has(self.observability) || self.failurePolicy == 'FailOpen'`.
- [ ] **Step 2: Run** `go -C deploy/crd/validation run -mod=readonly . ../inferplane.dev_governancepolicies.yaml` → exit 0; `go test ./internal/policy/ -run CRD` → PASS.
- [ ] **Step 3:** In `openRule`, route rules carrying `observability` to `openEditor(view)` (raw document editor) so the structured form never rewrites them as another kind; make the rule summary label show `observability`.
- [ ] **Step 4: Run** `go test ./internal/controlplane/... -count=1` → PASS.
- [ ] **Step 5: Commit** `feat(crd): observability rule schema and console raw-edit fallback`.

### Task 3: Local exporter ceiling config

**Files:**
- Create: `internal/config/observability.go`, `internal/config/observability_test.go`
- Modify: `internal/config/config.go` (`Config.Observability`, validation call)

**Interfaces:**
- Produces: `type config.ObservabilityConfig struct { ServiceName string; Exporters map[string]ObservabilityExporter }`; `type config.ObservabilityExporter struct { Endpoint, Protocol string; Insecure bool; Headers map[string]*SecretRef; Signals []string; DataBoundary string; MaxContentBytes, QueueBytes int64 }`; `const config.DefaultMaxContentBytes = 2 << 20`, `config.DefaultExportQueueBytes = 64 << 20`.

- [ ] **Step 1: Failing tests:** valid two-exporter config; rejects: empty exporters map, bad name, reserved `otel`, endpoint with scheme/path or missing port, protocol `udp`, empty/duplicate/unknown signal, content without `data_boundary: internal`, content with `insecure: true` on a non-loopback host, header ref with both/neither env and file, header name with spaces, max_content_bytes > 16 MiB, queue_bytes < 1 MiB or > 1 GiB, queue_bytes smaller than max_content_bytes.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement** `validateObservability(*ObservabilityConfig) error` and call it from the existing validation path next to `validateOTel`.
- [ ] **Step 4: Run** `go test ./internal/config/ -count=1` → PASS.
- [ ] **Step 5: Commit** `feat(config): observability exporter ceiling`.

### Task 4: Export metrics

**Files:** Modify `internal/metrics/metrics.go`; test in `internal/metrics/metrics_test.go`.

- [ ] Add `otelExport *prometheus.CounterVec` named `inferplane_otel_export_records_total` labels `exporter,signal,outcome`; register; nil-safe `func (m *Metrics) ObserveOTelExport(exporter, signal, outcome string, n int)` (n ≤ 0 is a no-op). Test via `testutil.ToFloat64`. Commit `feat(metrics): otel export record counter`.

### Task 5: `internal/otelexport` — exporter set and OTLP logs pipeline

**Files:**
- Create: `internal/otelexport/set.go` (declared exporters, `Allows`, `Has`, `Shutdown`)
- Create: `internal/otelexport/logs.go` (record building, queue, worker, retry)
- Create: `internal/otelexport/client.go` (HTTP protobuf and gRPC clients)
- Create: `internal/otelexport/audit.go` (audit `RecordSink`), `content.go` (`EmitContent`)
- Test: `internal/otelexport/*_test.go`

**Interfaces:**
- Consumes: `metrics.Metrics.ObserveOTelExport`; `audit.Record`, `audit.RecordSink` (Task 7 adds the interface; Task 5 implements against it, so implement Task 7's audit change first if executing out of order).
- Produces:
  - `type otelexport.Exporter struct { Name, Endpoint, Protocol string; Insecure bool; Headers map[string]string; Signals []string; MaxContentBytes, QueueBytes int64 }`
  - `func otelexport.New(serviceName string, exporters []Exporter, m *metrics.Metrics) (*Set, error)`
  - `func (s *Set) Allows(exporter, signal string) error` (nil-safe)
  - `func (s *Set) Has(signal string) bool`
  - `func (s *Set) TraceExporters() []Exporter`
  - `func (s *Set) AuditSink(decide func(team, user string) []string) audit.Sink`
  - `type otelexport.Content struct { RecordID, Team, Ingress, Model, Upstream, TraceID string; Request, Response []byte }`
  - `func (s *Set) EmitContent(exporter string, c Content)`
  - `func (s *Set) Shutdown(ctx context.Context) error`

Log record shape (audit): body = canonical JSON string; attributes `inferplane.audit.event`, `inferplane.audit.id`, `inferplane.audit.instance`, `inferplane.audit.hash` (sha256 of the canonical bytes, i.e. the next record's `prev_hash`), `inferplane.audit.prev_hash`, `inferplane.team`; `event_name` `inferplane.audit`; trace id set when the record has one. Content: body kvlist `{request, response}` strings; attributes `inferplane.audit.id` (joins the audit record), `inferplane.team`, `inferplane.ingress`, `gen_ai.request.model`, `gen_ai.response.model`; `event_name` `inferplane.content`.

Queue: per exporter, channel capacity 8192 items plus an atomic byte budget `QueueBytes`; `enqueue` never blocks — over budget or full ⇒ `dropped_queue_full`. Worker batches ≤ 512 records, ≤ 3.5 MiB, or 1 s; export timeout 10 s; retry HTTP 429/502/503/504 and gRPC Unavailable/ResourceExhausted/DeadlineExceeded/Aborted up to 3 attempts (1 s, 2 s backoff, interrupted by shutdown); other failures ⇒ `dropped_export_error`. Content over `MaxContentBytes` ⇒ `dropped_oversize`. Failure log lines name exporter + status only, at most once a minute per exporter.

- [ ] **Step 1: Failing tests** with an `httptest` collector decoding `ExportLogsServiceRequest`: audit record arrives with body/attributes/hash; content arrives with request/response; headers sent; 503 then 200 retried once; 400 dropped and counted; `TestQueueDropsWhenFullWithoutBlocking` (blocked collector, enqueue returns in < 50 ms and drop counter increments); `TestContentOversizeDropped`; `TestShutdownHonorsDeadline` (collector never answers, `Shutdown` with 200 ms deadline returns within 1 s); gRPC client against an in-process `LogsServiceServer` on 127.0.0.1; `Allows` matrix; nil `*Set` `Allows` returns an error.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/otelexport/ -race -count=1` → PASS.
- [ ] **Step 5: Commit** `feat(otelexport): bounded OTLP log export for audit copies and content`.

### Task 6: Trace routing and client-independent sampling

**Files:**
- Modify: `internal/tracing/tracing.go` (`NewExporter`, legacy `Init` delegates to `InitRouted`)
- Create: `internal/tracing/route.go` (routing span processor)
- Test: `internal/tracing/route_test.go`; existing `tracing_test.go` stays green.

**Interfaces:**
- Produces:
  - `type tracing.ExporterConfig struct { Endpoint, Protocol string; Insecure bool; Headers map[string]string }`
  - `func tracing.NewExporter(ctx context.Context, c ExporterConfig) (sdktrace.SpanExporter, error)`
  - `type tracing.Target struct { Exporter string; SamplePercent int }`
  - `type tracing.Decider func(ctx context.Context) []Target`
  - `func tracing.InitRouted(serviceName string, exporters map[string]sdktrace.SpanExporter, decide Decider) func(context.Context) error`

- [ ] **Step 1: Failing tests** with in-memory exporters (`tracetest.NewInMemoryExporter`): decider returning `{a,100}` exports to `a` only; `{a,100},{b,100}` exports to both; unknown exporter ignored; `{a,0}`-equivalent (never chosen) not exported; `TestClientTraceparentDoesNotControlExport` — Extract a `-00` traceparent then Start: span still recorded and exported to `a`; a `-01` traceparent with decider `[]` exports nothing; trace id of the exported span equals the client's.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement** `routingProcessor` (OnStart: draw `rand.IntN(100) < pct` per target, store chosen exporter names keyed by span id; OnEnd: `LoadAndDelete` and forward to each target's batch processor; ForceFlush/Shutdown fan out) and `sdktrace.AlwaysSample()`.
- [ ] **Step 4: Run** `go test ./internal/tracing/ -race -count=1` → PASS.
- [ ] **Step 5: Commit** `fix(tracing): route spans per governed decision; ignore client sampled flag`.

### Task 7: Audit record sink and account attribution

**Files:**
- Modify: `internal/audit/sink.go` (`RecordSink`), `internal/audit/writer.go` (dispatch), `internal/audit/record.go` (`PrincipalRef.Account string \`json:"-"\``)
- Modify: `internal/principal/principal.go` (`AuditRef` sets `Account: p.AccountSubject()`)
- Test: `internal/audit/sink_test.go`, `internal/principal` test

- [ ] **Step 1: Failing tests:** writer calls `WriteRecord` with the record (Account visible) and identical canonical bytes for a `RecordSink`; canonical bytes of a record with Account set equal those without (no `account` key); `TestAuditSinkNeverBlocksWriter` in otelexport (blocked collector, 10k appends complete promptly).
- [ ] **Step 2–4:** implement, run `go test ./internal/audit/ ./internal/principal/ ./internal/otelexport/ -race -count=1` → PASS.
- [ ] **Step 5: Commit** `feat(audit): optional record sinks receive the in-memory record`.

### Task 8: Content export hook in ingress handlers

**Files:**
- Create: `internal/server/requestpolicy/content.go`
- Modify: `internal/server/server.go` (`WithContentExport` DataMux option; set on msgs, chat, resp, invoke, stream, converse)
- Modify: `internal/server/anthropicapi/messages.go`, `openaiapi/chat.go`, `bedrockapi/invoke.go`, `responsesapi/handler.go` + `observe.go` (setter + call next to each `bodies.Capture`)
- Test: `internal/server/anthropicapi/content_export_test.go`

**Interfaces:**
- Produces: `type requestpolicy.ContentRecord struct { RecordID, Team, Account, Ingress, Model, Upstream, TraceID string; Request, Response []byte }`; `type requestpolicy.ContentExport func(ctx context.Context, c ContentRecord)`; `func server.WithContentExport(f requestpolicy.ContentExport) DataMuxOption`.

- [ ] **Step 1: Failing test:** Messages handler with a masking-free mock provider; export hook captures one record with the egress body, team, account, model/upstream and the same record id as the audit record; a privacy-denied request produces no export.
- [ ] **Step 2–4:** implement at all seven capture sites with the same status condition as body capture; run `go test ./internal/server/... -race -count=1` → PASS.
- [ ] **Step 5: Commit** `feat(server): governed egress content export hook`.

### Task 9: Gateway assembly and acceptance

**Files:**
- Create: `cmd/mayu/observability.go` (build set from config, resolve header refs, decider closures, trace exporters)
- Modify: `cmd/mayu/gateway.go` (install ceiling before policy revalidation, append audit sink, `WithContentExport`, tracing init, shutdown ordering: audit writer close → set shutdown → tracer shutdown)
- Test: `cmd/mayu/observability_acceptance_test.go`

- [ ] **Step 1: Failing acceptance test:** config with exporters `corp` (traces, audit) and `vault` (content, internal) pointing at one fake collector per exporter; local policy file with an observability rule for team `engineering`; one `/v1/messages` call; after `gateway` shutdown both collectors received the expected traces/logs; a second boot with a policy naming `missing` fails with a message naming the exporter; a config with only legacy `otel` still exports spans.
- [ ] **Step 2–4:** implement; run `go test ./cmd/mayu/ -run Observability -race -count=1` then the full suite.
- [ ] **Step 5: Commit** `feat(mayu): assemble governed observability export`.

### Task 10: Docs and examples

**Files:** `internal/CLAUDE.md` (tracing/otelexport bullets), `cmd/CLAUDE.md` (assembly rule), `docs/reference/agent-llm.md` or the observability reference that documents ADR-011, `examples/config.json` (commented-out equivalent is not possible in JSON: add `examples/observability/config-snippet.json` and `examples/policies/observability.yaml`), `docs/policy-*.md` rule list.

- [ ] Update text to say traces, audit copies and content are OTLP signals selected by policy within the local ceiling; Prometheus stays the only metrics path; audit chain remains the evidence of record.
- [ ] Run `bash tests/run-all.sh`, `go vet ./...`, `gofmt -l .`, `go test ./... -race`.
- [ ] Commit `docs: governed observability export`.

## Self-Review Notes

- Spec coverage: decisions 1–8 map to Tasks 1 (4, 5), 3 (2, 6, 8), 5 (1, 3, 6), 6 (7, 8), 7–8 (6), 9 (all assembly).
- Types: `TraceExport` exists twice by design (wire `v1alpha1.TraceExport` with `*int`, internal `policy.TraceExport` with `int`); `tracing.Target` mirrors `policy.TraceExport` so `tracing` stays a leaf.
- Out of scope: OTLP metrics push (Prometheus remains the single registry), per-team session policies, CP-side visibility of DP ceilings.
