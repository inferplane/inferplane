# Contributing

- All commits MUST be signed off (`git commit -s`) — [DCO](https://developercertificate.org/).
  Automated DCO enforcement is not yet configured; reviewers must check sign-off.
- Provider PRs touch only `providers/<name>/`, the blank-import line in
  `cmd/mayu/main.go`, and provider docs — zero core diff.
- Run both static builds, `go test ./... -race`, `go vet ./...`, `gofmt -l .`,
  and `bash tests/run-all.sh` before submitting.
- Architecture: `docs/architecture.md`. Historical design rationale:
  `docs/specs/2026-06-10-inferplane-gateway-design.md`.
