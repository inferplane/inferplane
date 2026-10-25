# Native Converse and administrator comparison

The node-local **mayu** gateway accepts native
`POST /model/{configured-model}/converse` and `/converse-stream`.
Use a virtual key with Bearer or `x-api-key` authentication. Client AWS SigV4
identity is not accepted as a replacement for that key. The gateway signs its
upstream request using the configured provider credentials.

Converse works only with eligible Bedrock Runtime targets. An upstream model
must itself support the requested API and content. Mantle targets and custom
SDK endpoint overrides are excluded. AWS regional, FIPS and dualstack endpoints
are selected from the SDK resolver; native requests do not follow redirects or
perform hidden transport retries.

JSON and native eventstream frame bytes are preserved. Mandatory guardrail
insertion and verified privacy masking remain policy exceptions. Invalid or
conflicting guardrails, unsupported pricing modifiers and ambiguous structures
are refused. The stream must include messageStop and valid trailing usage;
interruption emits an AWS exception and retains financial uncertainty.
This does not promise support for every native field on every upstream model.

## Run a comparison

1. Open mayu's `/admin/ui/` and sign in with full administrator authority.
2. Open **Model comparison / 모델 비교**, select two configured models and their
   APIs, then enter the same prompt, output limit and repetition count.
3. Supply a virtual key whose model, team and budget permissions cover the test.
   Administrator authority does not replace virtual-key admission.
4. Run the comparison. Calls are sequential in A/B then B/A order; one to five
   rounds means two to ten attempted requests before any stop condition.
5. Inspect each result's usage evidence and cost. Export the in-memory JSON when
   needed; it excludes credentials, prompt text and answer text.

The UI clears the key field when starting and does not persist the key. Cancel
stops pending work; an already submitted upstream call may still be billable.
Results from cancellation or network loss have unknown cost. Reloading or locking
the console clears the in-memory view. A separate server-side benchmark budget
is not created automatically: use the existing virtual-key/team hard controls.

## Read the measurements

| Field | Meaning and limit |
|---|---|
| Inference HTTP status | Separate from the administrator bridge HTTP status |
| Response success | Structurally completed text/tool response; not a quality or correctness grade |
| Response failure | Includes HTTP/network errors, refusal, filtering, truncation, empty or malformed responses |
| E2E duration | Internal data-handler elapsed time; includes upstream wait and completion, excludes browser round-trip |
| E2E output tokens/s | Reported output tokens divided by that duration; not steady-state decode throughput |
| TTFT | Not observed in this non-streaming UI; `null` |
| Cost | Configured price × observed usage, integer microUSD with existing half-even settlement |
| Accounting known | One attempt, one complete settlement, sufficient usage evidence and a configured price; not invoice reconciliation |
| Multiple attempts | Final settlement is displayed as incomplete evidence; earlier failed calls are not assumed free |
| Cache | Unknown write TTL or unqualified OpenAI cached input semantics retain uncertainty |

The initial UI sends text-only prompts and caps output at 4,096 tokens. It is
appropriate for paired smoke comparisons, not a context-window saturation or
concurrency load generator. Native API clients can submit other supported content
subject to the same routing and policy checks.

The general analytics dashboard uses its aggregate schema; the additional
uncertainty/attempt evidence described here belongs to the comparison result.
See [performance results](performance/index.md) for measured gateway latency,
cache costs and live UI/SDK compatibility, and [model performance](performance/models.md)
for context capacity, generation throughput and task failures.

The API contract is documented in the [HTTP reference](api-reference.md);
the design and governance boundaries are recorded in
[ADR-052](decisions/ADR-052-native-converse-governed-evaluation.md).
