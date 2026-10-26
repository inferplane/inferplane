# Context-window validation, 2026-10-02

For the latest measured model capacity, latency and cost, see
[model performance](../performance/models.md). This page retains the October 2
client-profile validation and its configuration-specific limits.

These synthetic text probes ran through the authenticated local mayu gateway,
with one configured target per public model. They do not qualify arbitrary
tools/history, model quality, another deployment/region, or an exact maximum.
No prompt, virtual key, account identifier or session identifier is recorded in
the [machine-readable evidence](../validation/context-window-probes-2026-10-02.json).

| Public model | Declared bound | Observed accepted input | Result |
| --- | ---: | ---: | --- |
| `openai.gpt-6.1-sol` | 1,050,000 | 600,026 | About 1,048,000 synthetic units returned an unclassified upstream 400; maximum unresolved. |
| `qwen.qwen3-coder-next` | 256,000 | 254,029 | Accepted; client usable window may increase from 204,800 to 243,200. |
| `global.xai.grok-4.7` | 500,000 | 430,037 | Accepted; client usable window may increase from 400,000 to 425,000. Maximum unresolved. |

Sol 6.1 also rejected `reasoning.effort: "none"` on a short request. The same
short request succeeded without that native option. Native probes therefore
leave effort unset; bridge probes use the existing neutral sentinel. The long
native request still returned 400 without the effort option. The gateway hides
the upstream error body, so neither error is promoted to an exact context limit.
Sol 6.1's existing client bound/headroom remains unchanged. Its operational
native declaration differs from the separate Converse example/worktree.

Installed Codex 0.159.3 runtime tests against loopback fakes confirmed model
switching resolves Grok to 425,000 usable tokens and Qwen to 243,200, even with
an inherited `model_context_window=1048576`. The local installed launcher and
`icodex` shell integration were updated after validation; a new shell/client
loads the operator profile. The gateway and global Codex config were not changed.

The total campaign ceiling was $30. Configured-price estimates for successful
usage and conservative reservations for unknown failures are kept separately
in the artifact; they are not AWS bill reconciliation. Failed calls are not
assumed free. Remaining budget was insufficient for the next near-window
Astra/Sol 6 probe, so those models remain unqualified rather than guessed.

To use the reviewed Qwen/Grok client adjustments:

```bash
export INFERPLANE_CODEX_CONTEXT_PROFILES="$PWD/examples/codex/context-profiles.json"
icodex -m qwen.qwen3-coder-next
```

The launcher reads the profile at startup, keeps the gateway's declared cap,
and changes only usable/compaction percentages. For Qwen these are 95%/90%;
for Grok they are 85%/82%. A changed gateway bound refuses the stale profile.
Restart the client after installing a profile. Explicit caller compaction
settings can still affect runtime compaction.

The reusable probe prints a plan unless `--execute` is supplied:

```bash
python3 scripts/inferplane-context-probe \
  --config /path/to/gateway-config.json --model qwen.qwen3-coder-next \
  --units 254000 --max-spend-usd 30 --ledger /path/to/campaign.json
```

For execution, privately export `INFERPLANE_API_KEY` and append `--execute`.
Use the **same ledger** for every model/call in a campaign. The command locks
the ledger across calls, persists the worst input/cache-price reservation before
sending, never retries, bypasses ambient proxies, and refuses redirects. Prices
come from local overrides and must conservatively cover the deployment's actual
tiers; hosted tools/search are not invoked. No provider-secret references are
resolved. Unknown failures keep their full reservation. A success is recorded
as a qualified observation only when the public model remains unchanged and no
model-substitution header is present. This is operator evidence, not financial
authority or a proof against an adversarial/stale backend declaration.
