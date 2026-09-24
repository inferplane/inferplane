# Bedrock coding models: Kimi K3, Fable 5.1 and Opus 5.5

These opt-in examples extend existing model routing; they do not introduce a
second Responses ingress or replace the main branch's policy/identity pipeline.
Merge the provider, model, allowed-model and price entries into your configuration.
Do not replace a running production configuration with an example.

| Example | Public model | Upstream profile | API | Context |
| --- | --- | --- | --- | --- |
| [Kimi K3](../../examples/config.bedrock-kimi-k3.json) | `kimi-k3` | `global.moonshotai.kimi-k3` | Converse | 1,000,000 |
| [Fable 5.1](../../examples/config.bedrock-fable-5-1.json) | `global.anthropic.claude-fable-5-1` | Same as public ID | InvokeModel | 1,000,000 |
| [Opus 5.5](../../examples/config.bedrock-opus-5-5.json) | `global.anthropic.claude-opus-5-5` | Same as public ID | InvokeModel | 1,000,000 |

All examples use Seoul as source region, declare `tools`, and block missing
pricing. Global profiles may process outside Korea; an endpoint region is not
data residency. The SDK default credential chain can use the EC2 instance role;
no sample-account profile or extra AssumeRole is required. Remove unintended
credential/profile overrides if the instance role is the intended identity.

## Pricing and capability boundaries

Standard Global USD per million tokens, checked 2026-09-20 (Opus 5.5: 2026-09-23):

| Model | Input | Output | Cache read | Cache write |
| --- | --- | --- | --- | --- |
| Kimi K3 | 3 | 15 | 0.30 | 3.75 |
| Fable 5.1 | 10 | 50 | 0.25 | 12.50 (5m), 20 (1h) |
| Opus 5.5 | 4 | 20 | 0.20 | 5 (5m), 8 (1h) |

Prices are keyed to the exact global profile; US CRIS must have its own prices.
K3 untiered cache writes use the legacy 5m accounting bucket, not a claim of a
5m TTL. Its example sets both write buckets to the published rate. Converse
does not implement K3's explicit Chat/Responses cache controls in this adapter.

The 1M windows are documented model capacities, not million-token load-test
results. Fable's published maximum output is 128K; do not confuse that with a
client/gateway output budget. The installed launchers may use smaller output or
compaction budgets. K3's model card does not specify a maximum output count.

Fable 5.1 uses always-on adaptive thinking. Omit unsupported sampling parameters;
the existing Fable legacy-thinking adapter also matches `fable-5-1`. AWS requires
`aws_review` retention opt-in; these examples do not change that account setting.
Obtain explicit organizational approval before enabling AWS review. Refusals are
HTTP 200 with a refusal stop reason, not successful execution of a requested task.

Opus 5.5 cache reads are 0.05x input, so its example declares every cache rate
instead of relying on the derived 0.1x default. Thinking cannot be disabled:
`thinking.type: disabled` and `enabled` with `budget_tokens` both return 400. The
legacy-thinking adapter matches `opus-5` (Opus 5 and 5.5) and rewrites only the
`budget_tokens` form; `disabled` passes through and fails. Default effort is
`medium`, one level below Opus 5. Forced `tool_choice` (`any`/`tool`) returns 400,
so an OpenAI or Responses client sending `tool_choice: required` is rejected
upstream; the gateway does not weaken it to `auto`. No client acceptance has
been recorded for Opus 5.5.

## Client setup and acceptance status

Use the main branch's [client setup](../getting-started/clients.md) and
[Codex launcher](../codex-launcher.md). All client requests must go through mayu;
AWS credentials stay on the data plane. Claude Code can use the native Bedrock
mode pointed at mayu's Bedrock ingress rather than the AWS endpoint. Changing
client mode does not itself prove compatibility.

Recorded local acceptance on 2026-09-20 used a deployed-source build plus the
foreign-thinking output hotfix. These are bounded file-read tool round trips,
not qualification of all plugins, arbitrary tools or long coding sessions:

| Client | Fable 5 | Fable 5.1 | Kimi K3 |
| --- | --- | --- | --- |
| OpenCode, Responses launcher | Passed | Passed | Passed |
| Codex, Responses launcher | Passed | Passed | Passed |
| Claude Code, Anthropic Messages mode | Passed after retries | Failed | Not run after prior failure |

Claude Code's Fable 5.1 request failed with Bedrock
`messages.1.output_config: Extra inputs are not permitted`; fallback to Fable 5
also failed on that field. Fable 5 initially encountered a `safeguards` rejection.
Native Bedrock-mode retesting remains pending. Do not strip safety-related fields
blindly or claim that all three clients are fully verified.

A later long Codex/Fable 5.1 session repeatedly exhausted the bridge's 4096-token
output budget with no visible progress. Handling empty/incomplete responses,
reasoning/output budgets and bounded retry behavior remains open. The thinking
hotfix prevents unsupported-block errors; it does not resolve this later issue.

## Sources

- [Kimi K3 model card](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-moonshot-ai-kimi-k3.html)
- [Fable 5.1 model card](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-fable-5-1.html)
- [AWS FoundationModels Seoul prices](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonBedrockFoundationModels/current/ap-northeast-2/index.json)
