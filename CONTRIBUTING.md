# Contributing

- All commits MUST be signed off (`git commit -s`) — [DCO](https://developercertificate.org/).
  CI rejects unsigned commits.
- Provider PRs touch only `providers/<name>/`, the blank-import line in
  `cmd/mayu/main.go`, and provider docs — zero core diff.
- Run `go test ./...` before submitting.
- Architecture: `docs/architecture.md`. Historical design rationale:
  `docs/specs/2026-06-10-inferplane-gateway-design.md`.
