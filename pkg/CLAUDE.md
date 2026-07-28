# pkg Module

## Role
Public, importable packages with no dependency on `internal/`. Safe for external
consumers and providers to import.

## Key Packages
- `schema/` — the canonical message schema (Anthropic-superset). `usage.go` (ADR-030) — `MergeUsage` FOLDS a usage frame into a running one (latest non-nil per field; Anthropic refines counts across frames rather than adding, so summing would double-bill) and `Usage.CacheWriteTiers` resolves cache-creation tokens into the 5m/1h tiers pricing bills separately (the TTL split wins over the flat total, NEVER summed). Both exist because the streaming settlement path used to overwrite instead of fold — dropping every input and cache count — and the TTL mapping was open-coded at six call sites that all dropped the 1h tier. `blocks.go` (ContentBlock with `*string` streaming fields + `CacheControl`), `message.go`, `request.go`, `response.go`, `chunk.go`, `extra.go` (unknown-field preservation, case-collision rejection, semantic equality), `model_info.go`, `sse.go` (`WriteAnthropicSSE`), `roundtrip_test.go` (golden fixtures).
- `ulid/` — monotonic ULID (Crockford base32, crypto/rand, big-endian carry) for audit record IDs.

## Rules
- **Canonical schema invariant:** same-protocol round-trip is lossless. Pipeline-interpreted fields are typed; everything else is preserved via `Extra map[string]json.RawMessage`.
- Streaming-frame string fields are `*string` so empty values (`"text":""`) survive a round-trip.
- `extra.go` must reject case-variant key collisions (e.g. `Model` vs `model`) to prevent duplicate-key smuggling.
- No imports from `internal/` — keep `pkg/` consumable by providers and external code.
- Changes here ripple across every provider and ingress; cover with golden fixtures in `roundtrip_test.go`.
