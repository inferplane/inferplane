# Model-aware Codex launcher

`scripts/inferplane-codex` is a Python 3.9+ standard-library helper for the
local inferplane gateway. It reads `INFERPLANE_API_KEY` from its inherited
environment and authenticates `GET http://127.0.0.1:8081/v1/models`. It never
loads `mayu.env`, reads upstream credentials, or falls back to a direct provider.

**Integration status:** catalog loading, model-specific context, argument
precedence, neutral bridge effort, native preferences, and request routing have
been checked with Codex 0.154.0 and a loopback fake server. Deployment requires
the gateway's neutral `reasoning.effort="none"` handling and the metadata
contract below. These checks do not establish live backend support.

## Launching

With the gateway key already exported privately:

```bash
scripts/inferplane-codex
scripts/inferplane-codex -m global.xai.grok-4.6
scripts/inferplane-codex -m openai.gpt-6-astra  # explicit native Responses session
printf '%s\n' 'Explain the current diff' | scripts/inferplane-codex exec -
scripts/inferplane-codex exec --sandbox read-only -- 'A prompt beginning with --'
```

Use a model ID actually returned for your virtual key. The default is
`global.openai.gpt-6-astra`, the existing Astra model through the portable
Converse bridge. This starts new sessions with neutral reasoning and a
stateless conversation format suitable for switching between bridged models.
`openai.gpt-6-astra` remains an explicit native Responses option with its
native reasoning preferences and capabilities.

If the portable default is unavailable, the launcher stops rather than silently
selecting native Astra. Select another accessible ID explicitly. `-m`,
`--model`, attached/equal forms, and `-c model=...` are checked
against the authenticated list. An explicit model flag takes precedence over
`-c model=...`. The default deliberately overrides a model in user/profile
config, keeping the launcher default independent of direct Codex's model choice.
For `-c model=...`, the launcher accepts a plain ID, a JSON-compatible
double-quoted string, or a single-quoted literal string. It does not parse
the full TOML grammar or trailing inline comments; use `-m MODEL` for an
unambiguous model selection.

All caller arguments and stdin are preserved. Provider/catalog overrides are
appended **after the subcommand and caller options**, immediately before the
first `--` if present. This avoids the Codex behavior where a subcommand's `-c`
list supersedes root options. Approval, sandbox, and profile settings are not
overwritten. For a bridged initial model, the helper appends
`model_reasoning_effort="none"`, including after an explicit non-neutral caller
override. Native initial models keep the user's reasoning preferences.
Direct-provider switches such as `--oss`, `--local-provider`, and `--remote`
are refused.

The helper sets the `inferplane` provider to the local `/v1` URL, Responses
wire API, `env_key="INFERPLANE_API_KEY"`, `requires_openai_auth=false`, and
`supports_websockets=false`. Hosted web search is disabled, as in the existing
shell function. Missing credentials, inaccessible/malformed model metadata,
unknown, Responses-unsupported, explicitly tool-ineligible or unmapped native
selected IDs stop the launch. Invalid native binding metadata and failures
reading the installed catalog also stop the launch. A valid native binding
whose slug is absent from that catalog is skipped with a warning so other
eligible models remain usable.
Discovery ignores proxy settings, refuses redirects, has a five-second timeout
and a 4 MiB response limit, and never includes response bodies in errors.
For Codex child processes, including bundled-catalog inspection, the helper
merges existing `NO_PROXY` and `no_proxy` exclusions and adds `127.0.0.1`,
`localhost`, and `::1` to both variables. This uses a copied child environment;
the parent environment and all HTTP/HTTPS/ALL proxy settings are unchanged.
Other destinations retain their configured proxies and existing exclusions.

## Model catalog and context

The helper writes a temporary `model_catalog_json` file with mode `0600` in a
`0700` directory. It keeps the file alive while Codex runs, then removes it on
normal exit, CLI startup failure, or termination. SIGHUP and SIGTERM are
forwarded. Terminal Ctrl-C reaches Codex through their shared foreground
process group; the wrapper avoids sending a second interrupt. Use SIGTERM
when terminating the wrapper programmatically. SIGKILL cannot run cleanup.
The catalog contains model metadata and instructions, never keys.

Each eligible gateway ID appears in Codex's `/model` picker. Both `context_window` and
`max_context_window` use that ID's declared bound: `context_window`, otherwise
`max_model_len`, or the smaller value when both are present. Codex 0.154.0 clamps
an inherited global `model_context_window` to the entry's `max_context_window`,
including when it resolves another model. The helper adds no global
context-window override.

For an explicitly selected portable Responses model, gateway context admission
uses the existing ingress estimate, `max(1, request_bytes / 4)`, plus the requested
output budget. This is an estimate, not the backend's tokenizer. The sensitivity
inspector's larger byte-based ceiling also accounts for recursively decoded JSON;
it must not be mistaken for this model's actual input-token count. Automatic
alternatives, strict budget targets and InternalOnly targets keep conservative
capacity checks. Inspection coverage, capabilities, pricing and budget reservation
are still enforced.

This is verified against the installed Codex 0.154.0 app server using an
isolated config with `model_context_window=1048576`. `config/read` still returns
that raw global value, while the runtime `thread/tokenUsage/updated` event
reports **419,430 usable tokens for Grok**: its 524,288-token catalog cap with
the portable entry's 80% usable-context setting. The fixed global value does
not win over that per-model cap. `codex debug models` alone returns raw catalog
metadata and is not evidence of an effective runtime limit.

A second test starts on the portable Astra default, then switches to Grok in
the same thread. With a 1,000,000-token Astra test declaration, the runtime
usable window changes from 800,000 to 419,430 despite the unchanged global
setting. The second request replays the first user and assistant turns without
opaque reasoning items or a `previous_response_id`. These are fake-provider
client checks, not claims about every backend or existing native history.

No user-config removal is required for the launcher's cap enforcement. For a
separate direct-Codex cleanup, the installed 0.154.0 bundled Astra metadata
reports `context_window=272000`, `max_context_window=872000`, and 95% usable
context. Those are bundled defaults; gateway aliases use the gateway-declared
bounds instead. Removing the obsolete global setting to restore direct
Codex's bundled default is a separate integration action; this helper never
edits `~/.codex/config.toml`.

If neither bound is declared, the helper warns and uses **16,384 tokens**.
This is a conservative operational fallback, not an assertion about the model's
actual capacity; a smaller real model still needs accurate gateway metadata.
Portable entries reserve 20% context headroom and compact at 80%; native
entries retain bundled headroom with an 80% compaction threshold. Codex may
further clamp compaction, or apply a caller's explicit compaction setting.
Invalid bounds, duplicates, and malformed entries fail closed.

The catalog is a startup snapshot. Newly added models/metadata require
relaunching; the gateway remains responsible for request-time authorization
and routing. A picker entry is not proof of Responses/tool compatibility.
Switching from native to bridged models can leave unsupported opaque history;
start a new session when the bridge refuses that history.

## Responses and tool capability filtering

An entry with `responses_mode="unsupported"` is skipped without failing the
rest of discovery, even if it declares `"tools"`. This means its provider chain
cannot serve native Responses or the canonical bridge. Exclusion happens
before capability processing, context fallback, or native template lookup.

For other entries, tool filtering uses the gateway's explicit `capabilities`
metadata:

| Metadata | Codex eligibility |
| --- | --- |
| Field absent | Included for compatibility with older servers; tool support is not attested. |
| String array containing `"tools"` | Included, subject to the other metadata checks. |
| String array without `"tools"`, including `[]` | Excluded from the catalog and from explicit `-m` selection. |
| `null`, a non-array value, or non-string array members | Malformed metadata; launch fails closed. |

An excluded default does not silently select another model. Filtering applies
before native template lookup or context fallback for excluded entries. It is
local to the launcher: these models remain in gateway discovery; plain-text
API access still depends on the gateway's authorization and backend availability.
The deployment can withhold `"tools"` from declarations for text-only models
or account-blocked routes without a launcher code change. No model names or
provider brands affect filtering, and no prompt-based tool emulation is added.
Other capability strings do not
enable extra Codex features; Responses mode and trusted native metadata still
determine the catalog profile.

## Metadata needed from the gateway

Extend each `/v1/models` entry with these bounded fields, derived from declared
route capabilities rather than model-name or provider-brand heuristics:

| Field | Values | Meaning |
| --- | --- | --- |
| `responses_mode` | `"native"`, `"bridge"`, or `"unsupported"` | Native Responses, the stateless bridge, or a chain containing a provider without either support. Unsupported entries are skipped. A native declaration must hold for every eligible route/fallback. |
| `codex_model` | Exact bundled Codex slug derived from the public model name, e.g. `"gpt-6-astra"` | Required for an eligible native route; selects trusted local metadata. Omit for bridged/unsupported routes. Never expose a private upstream deployment ID here. |
| `capabilities` | Model declaration's array of verified capability strings | Include `"tools"` only when declared and supported. An explicit empty array excludes the model from Codex; omission preserves legacy eligibility. Do not serialize `null`. |
| `context_window` / `max_model_len` | Positive integer | Actual configured model context bound. |

Example:

```json
{
  "object": "list",
  "data": [
    {
      "id": "global.openai.gpt-6-astra",
      "context_window": 1000000,
      "responses_mode": "bridge",
      "capabilities": ["tools"]
    },
    {
      "id": "openai.gpt-6-astra",
      "context_window": 1000000,
      "responses_mode": "native",
      "codex_model": "gpt-6-astra",
      "capabilities": ["tools", "vision", "reasoning"]
    },
    {
      "id": "bedrock.grok",
      "context_window": 524288,
      "responses_mode": "bridge",
      "capabilities": ["tools"]
    },
    {
      "id": "other.model",
      "responses_mode": "unsupported",
      "capabilities": ["tools"]
    }
  ]
}
```

Without `responses_mode`, the helper uses portable metadata for compatibility
with existing metadata, including the neutral effort override. Native routes
must declare their mode to retain native preferences. This compatibility
behavior does not remove the requirement for bridge-side neutral-effort
support. The helper does **not** infer native support from `openai.*` or any
other name. Invalid or contradictory capability declarations are
rejected. Native metadata comes from `codex debug models --bundled`, which
skips refresh and does not read user configuration; the helper replaces its
slug and context limits with the gateway values. Only native models with a
matching installed template are included; valid but unmatched bindings are
skipped with a warning.

The gateway derives `codex_model` only from the **public** model name, stripping
an `openai.` or `openai/` namespace. It never derives this field from a private
upstream deployment ID. The current deployment's native public model maps
`openai.gpt-6-astra` to the exact bundled slug `gpt-6-astra`.

Native aliases whose normalized public names lack a bundled entry are not yet
selectable. The launcher emits the warning
`skipping a native model: codex_model is absent from the installed Codex catalog`
and omits the alias, while valid bridge models and known native mappings
continue to work. Explicitly selecting that alias still fails with the
`selected model is unavailable to Codex` error identifying an unmapped native
model as a possible cause. The launcher does not select a replacement, use a
private upstream name, or guess a native template.

This availability exception applies only to a valid mapping with no installed
match. Missing or invalid `codex_model` values, invalid capability/context
metadata, and catalog read/parse failures remain fatal. Warnings do not echo
untrusted model or template values.

Portable entries advertise text, direct tools, a single default/supported
reasoning level of `"none"`, no reasoning-summary parameter, no verbosity,
no Responses Lite, and no experimental context state. They do not clone
Astra's instructions or infer capabilities from a brand.

## Codex 0.154.0 reasoning handling

Actual requests captured against the fake server establish:

| Initial route | Synthetic user setting | Captured `reasoning.effort` |
| --- | --- | --- |
| Bridge | No override | `"none"` |
| Bridge | Inherited `model_reasoning_effort="xhigh"` | `"none"` |
| Bridge | Explicit `-c model_reasoning_effort="high"` | `"none"` |
| Native | Inherited `model_reasoning_effort="xhigh"` | `"xhigh"` |

An empty `supported_reasoning_levels` list and
`supports_reasoning_summary_parameter=false` **do not unset an inherited
effort**. Codex's request builder takes the configured effort ahead of the
catalog default and does not filter ordinary values against supported levels.
The CLI has no TOML-null/unset override for this value. Therefore the helper
explicitly sets the neutral sentinel for the initial bridged model and gives
every bridge catalog entry `"none"` as its only supported/default level.
The bridge must interpret `"none"` as requesting no reasoning feature while
continuing to refuse non-neutral efforts and actual opaque history.

Native initial models get no effort override. Their catalog entries retain
the bundled native reasoning choices. The bridge-only startup override does
not edit the user's persistent config, approval policy, or sandbox settings.
The `/model` picker is supplied the same per-model reasoning choices and
context bounds; the helper does not erase or translate existing session history.

Codex also unconditionally requests `include=["reasoning.encrypted_content"]`
in ordinary Responses requests. Catalog flags prevent enabling Responses Lite;
they cannot remove this include value or erase opaque items from old history.

This is neutral effort, not omission of the wire field. No opaque reasoning
history is silently discarded, and no extra reasoning capability is inferred
from a model's name or a general `"reasoning"` capability declaration.

## Offline verification

```bash
# Standard library only; fake HTTP model list and recording CLI.
python3 -B -m unittest discover -s tests/launchers -p 'test_inferplane_codex.py' -v

# Optional real installed CLI; all requests still go to loopback fakes.
# Creates an isolated synthetic CODEX_HOME, with no real auth or user config.
INFERPLANE_TEST_CODEX="$(command -v codex)" \
  python3 -B -m unittest discover -s tests/launchers -p 'test_inferplane_codex.py' -v

# Included automatically in the existing harness.
bash tests/run-all.sh inferplane-codex
```

Installed-client tests assert the bridge/native effort distinction, effective
context under an inherited fixed global window, and a portable-Astra-to-Grok
switch in the same thread with history replay.
They do not establish live backend compatibility. Tests also cover catalog
permissions/lifecycle, context fallback, auth failures, explicit capability
filtering and old-server eligibility,
redirect/proxy isolation, CLI config precedence, explicit `--`, stdin, native
metadata lookup, exit status and termination signals.

The schema and behavior were inspected from the installed CLI's
`codex debug models --bundled` output and the upstream `rust-v0.154.0` sources:
`protocol/src/openai_models.rs`, `protocol/src/openai_models/reasoning_effort.rs`,
`models-manager/src/model_info.rs`, `core/src/client.rs`, and
`utils/cli/src/config_override.rs`. Official configuration documentation:
<https://developers.openai.com/codex/config-reference/> and
<https://developers.openai.com/codex/config-advanced/>.

## Suggested shell integration

Deploy the matching gateway metadata and neutral-effort handling, then install
the reviewed script as `~/.local/bin/inferplane-codex`. Keep the existing
private environment-loading prefix in `icodex`, replacing its hardcoded Codex
configuration/argument array with this final command:

```bash
icodex() (
    if [[ ! -r "$HOME/inferplane-local/mayu.env" ]]; then
        printf '%s\n' 'icodex: cannot read ~/inferplane-local/mayu.env' >&2
        return 1
    fi
    . "$HOME/inferplane-local/mayu.env" || return
    if [[ -z "${INFERPLANE_VKEY:-}" ]]; then
        printf '%s\n' 'icodex: INFERPLANE_VKEY is not configured' >&2
        return 1
    fi
    export INFERPLANE_API_KEY="$INFERPLANE_VKEY"
    command "$HOME/.local/bin/inferplane-codex" "$@"
)
```

This task does not modify `.bashrc`, install the helper, or access `mayu.env`.
