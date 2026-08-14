# LLM Gateway: Customer Pain Points, Needs, and Must-Have Analysis

> **Written**: 2026-06-26 | **Sources**: internal Slack conversations, web research, knowledge graph
>
> For an analysis based on 59 actually-filed support tickets, see [docs/customer-issue-analysis.md](docs/customer-issue-analysis.md).

---

## 1. Pain Points Customers Are Experiencing

### 🔴 Complaints about third-party LLM gateways (LiteLLM, etc.)

| Complaint | Detail | Source |
|-----------|-----------|------|
| **Prompt-caching bug** | LiteLLM misidentifies the model ID and breaks prompt caching → cost spikes unexpectedly. A known bug where a Sonnet call gets answered by Opus | #claude-interest-korea (Woo Hyung Choi, 6/25) |
| **Invisible cost spikes** | A customer hitting the caching bug without knowing it sees cost keep climbing with no way to diagnose the cause | #claude-interest-korea (Woo Hyung Choi, 6/25) |
| **Missing logging** | Prompt logging is off by default; requires a separate config setting (`store_prompts_in_spend_logs: true`) | #claude-interest-korea (Jinsung Huh, 6/11) |
| **Guardrail bypass** | A vulnerability where security policies such as Bedrock Guardrails can be bypassed when going through LiteLLM | #claude-interest-korea (Jinsung Huh, 6/25) |
| **Access-control bypass** | Incomplete access-control features such as RBAC limit how well security policy can actually be enforced | #claude-interest-korea (Jinsung Huh, 6/25) |
| **Thin support staffing** | LiteLLM (Berri AI): the CEO reviews PRs, and there are **two customer engineers worldwide**. Customers see this and conclude "this isn't going to hold up" | #claude-interest-korea (Woo Hyung Choi, 6/25) |
| **No response on sales/contracting** | A Japanese customer (SCSK): after requesting a LiteLLM private offer, **10 days with no response** (mik@berri.ai, varoon@berri.ai, and sales@berri.ai all unresponsive) → jeopardized their July 1 launch plan | #jp-aws-marketplace-general (Yutaka Iwasaki, 6/24) |
| **C-level pressure** | One customer's stance: "if the LiteLLM problem isn't fixed this week, we're ripping it out entirely" — a VP got personally involved, and the CEO started tracking it directly | #claude-code-workshop-tf (Myeongsu Jeon, 6/24) |
| **Missing Bedrock-native features** | "The biggest problem is that LiteLLM's features aren't native to Bedrock. I did get asked how we could recommend such an unfinished service, but..." | #claude-code-workshop-tf (Myeongsu Jeon, 6/24) |
| **Alternatives like Bifrost are also immature** | Testing Bifrost (Maxim AI) → found it lacking in management features → customers end up back on LiteLLM anyway (no real alternative) | #claude-interest-korea (Jinsung Huh, 6/25) |

### 🟡 Common complaints when no LLM gateway exists at all (industry-wide)

| Category | Complaint |
|----------|-----------|
| **Cost opacity** | No way to attribute cost by team/feature/customer. In multi-tenant SaaS, no per-tenant tracking correlates with an average **340% cost overrun** |
| **Shadow AI** | 75%+ of employees use AI tools without approval; zero visibility for the security team |
| **Provider lock-in** | Auth schemes, API formats, and rate limits all differ, so switching providers means rewriting code across the board |
| **No failure handling** | No automatic failover on a provider outage → service goes down mid-demo |
| **API key sprawl** | Each team manages its own API keys independently → cost visibility is lost and security-incident risk rises |
| **Duplicate-call cost** | Repeated identical prompts get billed every time with no caching |
| **No observability** | No visibility into what data flows where |
| **Rate-limit conflicts** | Provider TPM/RPM limits collide with internal per-tenant quotas → double-throttling |

---

## 2. Why an LLM Gateway Is Needed

### Requirements specific to Korean customers

1. **Unifying multiple AI coding tools**: "Let developers use whichever tool they want (Claude Code or Codex), but **centralize cost and logging only**" — a need shared by most enterprise customers (#claude-interest-korea, Jinsung Huh, 6/11)

2. **NCT (National Core Technology) compliance**: customers handling NCT in Korea (mostly manufacturers) must process all AI inference **inside Korea only** — a region-locked gateway is mandatory (#claude-interest-korea, Byong-Wu Chong, 6/15)

3. **Security in mixed 1P (Anthropic Direct) + Bedrock environments**: even when using Claude Code via Anthropic 1P, an LLM gateway is placed in front for guardrails and prompt logging (#claude-interest-korea, Jinsung Huh, 6/25)

4. **Reprising the "service mesh" pattern**: the cross-cutting concerns a service mesh solved in the microservices era (auth, retries, observability, policy) are now an LLM gateway's job in the LLM era — **solve it once vs. every team rediscovering it independently**

### Industry-wide need

| Need | Description |
|--------|------|
| **Cost control** | Track token usage per team/project/feature and alert on budget |
| **Security/compliance** | PII filtering, prompt auditing, data-exfiltration prevention |
| **Operational reliability** | Automatic failover, rate limiting, retry/backoff |
| **Multi-provider strategy** | Avoiding lock-in, A/B testing, routing to the best model |
| **Developer productivity** | A single API; zero code changes on a model swap |
| **Governance** | Central visibility into who uses which model, how much |

---

## 3. Must-Have Features

### 🏗️ Core architecture

```
┌─────────────────────────────────────────────────────────┐
│  Applications (Claude Code, Codex, Custom Apps, Agents) │
└──────────────────────────┬──────────────────────────────┘
                           │
                    ┌──────▼──────┐
                    │ LLM Gateway │
                    └──────┬──────┘
                           │
        ┌──────────────────┼──────────────────┐
        ▼                  ▼                  ▼
  ┌──────────┐     ┌──────────┐     ┌──────────┐
  │ Bedrock  │     │ 1P APIs  │     │ Self-    │
  │ (Claude, │     │(Anthropic│     │ hosted   │
  │  GPT)    │     │ OpenAI)  │     │ Models   │
  └──────────┘     └──────────┘     └──────────┘
```

### ✅ Required features (Tier 1 — Must Have)

| # | Feature | Detail | Customer evidence |
|---|------|------|-----------|
| 1 | **Unified API** | Support both the Anthropic Messages API and the OpenAI Chat Completions API; switch providers with no code change | Universal across customers |
| 2 | **Cost attribution** | Real-time dashboard of token usage/cost per team/user/project | "centralize cost and logging only" |
| 3 | **Prompt logging** | Record every input/output prompt, for audit/debugging/compliance | Jinsung, Nambong — "prompt logging should also be fully possible, right?" |
| 4 | **Access control (RBAC)** | Per-user/team model access rights, daily/monthly usage caps | Enterprise security requirement |
| 5 | **Rate limiting / quota** | Per-user, per-team token quotas coordinated with provider rate limits | Prevents "cost explosions" |
| 6 | **Automatic failover** | Automatic fallback to another model/region on a provider outage | Operational reliability |
| 7 | **Guardrails** | PII filtering, harmful-content blocking, Bedrock Guardrails integration | Fixes the "guardrail bypass" bug |
| 8 | **Auth/SSO** | OIDC/JWT/SAML, IAM integration, API-key management | Enterprise security |
| 9 | **Region locking** | Restrict traffic to a specific region (NCT, GDPR, etc.) | "all AI inference inside Korea only" |
| 10 | **Observability** | Latency, error-rate, and token-usage metrics, integrated with CloudWatch/Datadog | Operational visibility |

### 🟡 High priority (Tier 2 — Should Have)

| # | Feature | Detail |
|---|------|------|
| 11 | **Semantic caching** | Cache similar prompts to cut cost; apply the correct per-provider TTL (avoiding LiteLLM's caching bug) |
| 12 | **Model routing** | Automatically pick the right model by request type/cost/quality |
| 13 | **Load balancing** | Spread traffic across multiple API keys/endpoints |
| 14 | **Audit trail** | A tamper-proof record of who called which model and when |
| 15 | **Prompt/response conversion** | Automatic API-format conversion (Anthropic ↔ OpenAI compatibility) |

### 🔵 Differentiators (Tier 3 — Nice to Have)

| # | Feature | Detail |
|---|------|------|
| 16 | **A/B testing** | Support quality-comparison experiments across models |
| 17 | **Budget alerts** | Automatic notification/block past a spend threshold |
| 18 | **Self-hosted model integration** | The same interface for self-hosted models (vLLM, TGI, etc.) |
| 19 | **MCP/tool-use support** | Integration with MCP tools such as AgentCore WebSearch (SigV4, PrivateLink) |
| 20 | **Multi-cloud support** | Route across AWS + Azure + GCP simultaneously |

---

## 4. Competitive Landscape

| Solution | Characteristics | Limitations |
|--------|------|------|
| **LiteLLM** (Berri AI) | Most widely used. Free OSS tier, Enterprise ~200-300M KRW/year | Two support staff, many bugs, caching issues, unresponsive |
| **Portkey.ai** | Used by Coupang and others | Limited management features |
| **Kong Gateway** | Added AI features. Adoption starting at Toss-affiliated companies | Heavyweight, not an LLM-gateway specialist |
| **Cloudflare AI Gateway** | Has a large-scale deal with Anthropic | No on-premises option |
| **Bifrost (Maxim AI)** | A lightweight alternative | Startup-stage, thin management features |
| **Azure APIM GenAI** | Microsoft's native GenAI gateway | Azure lock-in |
| **Google Apigee AI** | GCP-native | GCP lock-in |
| **AWS (Bedrock)** | No dedicated LLM gateway service yet | **"It would be great if AWS shipped one managed LLM gateway"** (Jinsung, 6/25) |

---

## 5. Key Insights and SA Implications

### 💡 Key Takeaways

1. **"AWS should claim the LLM gateway market first"** — other CSPs (Azure APIM, Google Apigee) already offer one; AWS is the only gap (#claude-interest-korea, Jinsung Huh)

2. **"It also doesn't feel like there's a reason for a CSP to build this gateway"** — a counter-view also exists: since the three major clouds effectively already provide an LLM gateway, the view is that a specialized outside vendor should build it instead (Woo Hyung Choi)

3. **"Since Bedrock can't cover everything, people are hastily putting a proxy in front"** — today's structure is third-party tools filling Bedrock's feature gaps

4. **LiteLLM dependency risk has become real** — a situation running from missing support to bugs to C-level pressure; the lack of an alternative is the biggest problem

5. **An open-source LLM gateway proposal** (Junseok Oh) — an ingress-adapter architecture supporting both the Anthropic Messages API and the OpenAI Chat Completions API

---

## 6. Related Resources

- [Single LLM gateway architecture blog post](https://aws.amazon.com/ko/blogs/tech/single-llm-gw-arch/) — Jinsung Huh, dohkim (published 6/26)
- [NCT GenAI Gateway sample](https://github.com/aws-samples/sample-nct-genai-gateway) — Byong-Wu Chong
- [LLM Gateway (Open Source) project](kg://entity:06c2dc8905c0) — proposed by Junseok Oh
- [Hana Bank LiteLLM deployment](kg://entity:24a5f8471754) — Hana Bank's LiteLLM adoption project
