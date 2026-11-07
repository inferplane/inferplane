# Performance hardening

Approved direction: the administrator controls optional masking (default off);
reuse request parsing; release local audit bytes only after collector acceptance;
improve the durable money journal. Implementation choices are delegated.

## Decisions

1. Provider HTTP defaults bound response-header waits to 120 seconds, without
   imposing a total lifetime on streaming responses. Explicit injected clients
   retain their transport and timeout semantics.
2. Responses has a request-local parsed document. Canonical observation, stored
   reference refusal, conversion validation and strict-tool checks share it.
   Masking invalidates it. Native forwarding still uses the original bytes.
3. Optional masking gets an explicit administrator setting, off when absent.
   Existing explicitly installed plugins and required GovernancePolicy privacy
   remain binding; an optional switch cannot silently override policy.
4. Add an explicit required OTLP audit sink. It exports exact canonical records
   and acknowledges only complete collector acceptance, including checking OTLP
   partial-success responses. The WAL is the local spool; successful batches are
   truncated only when no earlier undelivered records remain. No automatic replay
   of ambiguous partial fan-out, and no deletion of existing file-sink archives.
   Operators select this sink instead of permanent local file retention.
   Group commit amortizes fsync over queued records without acknowledging a
   durable operation before sync. Required file sinks also support batch sync.
5. Replace whole-journal serialization on every monetary transition with
   transactionally updated entity rows. Keep FULL SQLite durability, owner
   fencing, exact integer accounting, bounded receipts and restart burning.
   A cached snapshot may accelerate decoding but never substitutes for a
   successful database transaction. Migration is atomic and older readers must
   refuse the new journal version.

## Qualification

Offline regression tests cover masking authorization/defaults, exact bytes and
duplicate JSON keys, streaming header timeouts, collector failure/partial
acceptance, audit retention, journal rollback/fencing/restart and uncertainty.
Repeat the saved synthetic benchmark conditions where applicable; report
throughput and allocations separately from request latency. Production OTLP,
model quality and fleet HA are outside this local qualification.
