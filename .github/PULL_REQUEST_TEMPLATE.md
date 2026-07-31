## What & why

<!-- What does this PR change and what problem does it solve?
     Link the issue/ADR it implements, if any. -->

## How was it tested

<!-- `go test ./... -race` output, new tests added, manual verification. -->

## Checklist

- [ ] Every commit is DCO signed off (`git commit -s`) — see [DCO](../DCO)
- [ ] `go build ./... && go test ./... -race && go vet ./...` pass
- [ ] `gofmt -l .` reports nothing
- [ ] No secrets, keys, or inline credentials anywhere (config uses `env:` / `file:` / `secret:` refs only)
- [ ] Provider-only PRs touch only `providers/<name>/` and provider docs (design §8)
- [ ] Docs updated where behavior changed (README, `docs/`, ADR if a decision was made)
