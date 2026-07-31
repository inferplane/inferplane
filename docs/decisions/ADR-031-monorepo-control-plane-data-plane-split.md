# ADR-031: Monorepo split into control plane (inferplaned) and node-local data plane (mayu)

## Status

Accepted (2026-07-30)

## Context

inferplane began as a single central gateway binary. A central gateway sits on
the inference critical path, which contradicts the project's core argument:

- **Streaming latency** — a central hop taxes every SSE chunk and directly
  hurts TTFT (benchmarkable, the easier claim to defend).
- **Fault isolation** — a central outage must not stop every developer.

The repository was renamed `inferplane/mayu` → `inferplane/inferplane` and is
restructured as a monorepo with two binaries.

## Decision

1. **Two binaries, one module.**
   - `cmd/inferplaned` — control plane: policy/routing-rule distribution,
     budget-lease issuance, short-lived credential brokering, telemetry
     aggregation. Inference traffic never passes through it.
   - `cmd/mayu` — node-local data plane (localhost or K8s node-local), the
     existing gateway code moved via `git mv`. `mayu` is a **component name**,
     not a project name — the same position ztunnel/waypoint hold in Istio.
   - `cmd/inferplaned` is scaffolded **now** (health endpoints only) so both
     binaries import `internal/policy` from day one and schema version skew is
     a compile error, not a production surprise.

2. **Hybrid config API (`api/v1alpha1`).** The schema borrows the CRD shape
   (`apiVersion`/`kind`/`metadata`/`spec` — kubectl-friendly, CNCF-idiomatic)
   but delivery is inferplane's own gRPC/HTTP channel: workstation mode has no
   K8s API server, so the types depend on no Kubernetes machinery.

3. **Budget control is a lease pattern.** N proxies each see only their own
   usage, so rule propagation alone cannot enforce a global budget. The
   control plane issues leases ("this much budget for this interval"); the
   data plane enforces locally with zero network round trips inside the
   grant, reports consumption and renews asynchronously.
   **★ Open:** lease issuance unit and renewal cadence defaults — the single
   parameter pair trading overshoot tolerance against control-plane load.
   Config fields exist and are required; no default is hardcoded.

4. **Failure policy is per rule, never global** (`failurePolicy` on every
   rule, required, no default). Control-plane outage → inference continues
   within lease validity (fail-open); only hard budget caps fail closed on
   lease expiry. Global fail-open voids budget control; global fail-closed
   voids the fault-isolation argument.

5. **Version skew is explicit.** A data plane receiving a rule it does not
   support rejects it (`policy.UnsupportedError`) and reports the rejection;
   the control plane exposes the version distribution of connected proxies.
   Silent ignoring is the worst failure mode a governance tool can have.

6. **Local cache never replaces the server-side prompt cache.** Its three
   jobs: identical-request dedupe, cache-affinity routing (pin a session to
   the same region/inference profile to keep the server cache warm — the most
   important), offline spooling. Payloads live only in a `VolatileStore`
   (memory; named for the guarantee, not tmpfs — macOS has neither tmpfs nor
   /dev/shm); lease state / counters / prefix hashes / routing decisions live
   in SQLite and never contain payloads.

7. **Cache affinity vs fallback is an explicit per-rule preference**
   (`onAffinityConflict`): failover cold-starts the server cache and can
   raise cost, so neither side wins by default.

8. **Credentials never rest in the data plane.** The control plane
   authenticates users via OIDC and brokers short-lived credentials (e.g. STS
   AssumeRole), which also anchors per-user attribution at issuance time.
   Standalone (control-plane-less) mode may use local config keys but must
   print a "not recommended for production" warning at runtime.

## Consequences

- Provider isolation (§8), the canonical-schema invariant (§2.2), and the
  verbatim-body cache invariant (§4.4) are unchanged and now live in mayu.
- Existing `internal/router`/`server`/`openai`/`tracing`/`metrics` keep
  serving the static-config path; `internal/proxy`/`cache`/`telemetry` are
  the consolidation targets for the lease-driven path.
- Old release links (v0.1.0/v0.2.0) pointed at tags that were never pushed;
  versioning restarts when the first split release is cut.

## Open questions (★ — decide before implementing)

1. ~~Lease issuance unit and renewal cadence defaults~~ — **decided** in
   ADR-032 (grant 0.1% of limit, renew 10s; policy poll 60s, floor 15s).
2. ~~api/v1alpha1 shape~~ — **decided**: hybrid (CRD-style schema, gRPC/HTTP
   delivery).
3. Whether to track server-cache TTL with a proxy-local timer (providers do
   not return remaining TTL; knowing expiry opens a window to switch to a
   cheaper path, but complexity vs. benefit is unjudged).
