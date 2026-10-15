# Admin workspace design and validation

The control-plane and gateway consoles share a visual system and connect inspection to operational actions. Key issuance, spend inspection and model registration follow the basic tasks described in LiteLLM's documentation. Policy distribution and budget-driven model substitution have their own inferplane workflows.

## Views and tasks

| Task | Entry point and completion | Validation |
| --- | --- | --- |
| Connect a coding tool | mayu overview → model registration → key issuance → client setup | Gateway browser QA and registration/routing tests |
| Find or issue a virtual key | Search keys; enter team, model and budget; show plaintext once | Browser issuance/search and monetary-form tests |
| Edit models and providers | Search → edit → save, preserving existing metadata | PUT/routing Go tests, Node and browser tests |
| Inspect spend | Settlement analytics with an explicit UTC range; distinguish unavailable data | Unsafe int64, small amounts and analytics-outage cases |
| Investigate operational issues | CP Needs attention → filtered nodes → rejection detail → related policy | Event-handler and browser navigation tests |
| Set a spend limit | Daily/monthly budget form → change summary → publish | Budget Node tests and browser QA |
| Set model substitution | Guided rule → budget in the same policy → threshold/source/target | Go policy validation and browser publication |
| Configure access, throughput and sensitive-input protection | Task-specific form → complete document review → publish | Node adversarial cases and strict Go decoding/validation |
| Inspect publication | Success message's Inspect distribution → node observations | Browser QA |
| Navigate | Ctrl/⌘ K, search pages and fetched records, keyboard selection, Esc | Browser QA for both consoles |
| Adapt the layout | System fonts, light/dark themes and scrolling tables on narrow screens | All views at 390/768/1440px |

The control-plane console is at `/ui/`; the gateway console is at `/admin/ui/`. CP search covers fetched team usage, policies and recent nodes. Gateway search covers fetched keys, teams, providers and models. Newly issued plaintext keys and client configuration examples are excluded from search. Neither search sends a separate query to the server.

## Design principles

Search exposes tasks without requiring users to remember menus. Empty states show a next action; operational warnings link to evidence. Policy edits show changed fields alongside the complete document and allow returning to the draft. After publication, operators inspect observations from the next heartbeat.

Budgets and settled spend retain integer units. A configured limit and remaining admission credit are different quantities; telemetry cannot establish available credit. The UI explains the difference between substitutions that may return to the original model and strict targets that can refuse a request. Sensitive-input controls distinguish finite detection, inspection coverage and opaque-input refusal.

Existing servers determine limit, key and policy authority. The UI does not claim unimplemented organization roles or user pools. Matching generations and `/healthz` are not proof of fleet HA or inference-admission readiness.

## Reproduce

```bash
go test ./internal/controlplane/ui ./internal/server/adminui ./internal/server/configapi -race
node internal/controlplane/ui/testdata/workspace.cjs
node internal/server/adminui/testdata/routing_forms.cjs --money
python3 internal/controlplane/ui/testdata/browser_qa.py --screenshots /tmp/inferplane-cp-ux
python3 internal/server/adminui/testdata/browser_qa.py --screenshots /tmp/inferplane-gateway-ux
```

Browser QA requires installed Playwright and Chromium. It uses synthetic data and local fake APIs, without production services, real keys, AWS or a real IdP. Run repository builds, race tests, vet, formatting and harness checks as required by AGENTS.md.

Claims of faster completion or fewer errors than LiteLLM require a user study with the same tasks, participant conditions and data. Current validation establishes the listed workflows' implementation, behavior and visual quality; it does not establish complete feature parity or measured competitive superiority.
