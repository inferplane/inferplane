# Changelog

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **`inferplane login` / `token` / `logout`** (ADR-028): OIDC login for humans — trades an IdP session for an automatically-renewing, short-lived gateway virtual key instead of a hand-copied, never-expiring one. New opt-in data-plane endpoints `GET /v1/auth/config`, `POST /v1/auth/key`, `DELETE /v1/auth/key`; a second, distinct OIDC client from the admin console's; no IdP refresh token is ever cached on disk. CI/service-account provisioning (`inferplane keys create`, declarative `virtual_keys`) is unchanged.
- **Model-level fallback** (ADR-029): a hardcoded client requesting a not-yet-configured model (e.g. a new Claude release) is now served a configured model instead of 404ing, via config `model_fallbacks` or — with no config at all — a default same-family heuristic (`claude-opus-5` → the highest configured `claude-opus-*` version below it). A configured model whose upstream rejects it as unknown (404, or Bedrock `ValidationException`) also crosses to its fallback model within the existing priority-fallback chain. Substitution is fail-closed on RBAC (a key allowed only the requested name is denied, never silently downgraded) and advertised via `x-inferplane-model-fallback`.

### Fixed
- **Cost settlement was wrong in five independent ways, producing 0 µUSD on real traffic** (ADR-030). Streaming requests billed **output tokens only** — input and prompt-cache counts arrive on `message_start`, which the settlement path never read (measured: 5 µUSD where 52 was correct, a 10.4x under-bill, and Claude Code traffic is effectively all streaming). Cache writes always billed at the cheaper 5-minute tier, leaving the 1-hour tier (2x input) unreachable. A config declaring only `input_per_mtok`/`output_per_mtok` billed **all cache tokens at zero** — cache rates are now derived from the input rate (0.1x / 1.25x / 2x, verified against both Anthropic's and Bedrock's published tables). Bedrock cross-region prefixes (`global.`/`us.`/`apac.`) missed the rate table entirely, and Bedrock Converse dropped cache tokens while InvokeModel kept them. The OpenAI-compatible ingress billed cached prompt tokens at the full input rate (over-billing). A stream that broke mid-flight billed nothing for tokens already delivered.
- `pricing.on_missing: "block"` was dead config — it behaved identically to `allow`, so unpriced traffic an operator believed was refused was served free. It is now enforced at boot and at runtime, and an unrecognized value is a load error instead of silently meaning `allow`.
- A non-admin OIDC identity issuing a key via `POST /admin/keys` could set `owner` to any value, letting a teammate attribute a key to someone else; the server now always overrides `owner` to the caller's own verified subject.

### Changed
- **BREAKING (only when `pricing.on_missing` is `"block"`):** the gateway now refuses to boot if any configured route has no pricing rate, naming the routes. With the default `allow` it logs them loudly and continues. Migration: declare the missing rates under `pricing.overrides` (two numbers per model — cache rates derive), or set `on_missing` to `"allow"`.
- `pricing.version` labels the rate table and lands in every audit record's `cost.pricing_version`, which was previously the hardcoded string `"bundled"` even for fully-overridden tables — a disputed invoice can now be pinned to the rates that produced it.

## [0.2.0] - 2026-06-14

### Added
- **Free OIDC SSO for the admin plane** (ADR-004): the gateway validates IdP ID tokens (Dex/Keycloak/Okta) against the issuer's JWKS and maps the `groups` claim to teams; the static admin token remains as break-glass. Resource-server-only — no redirect/session/cookie, no CSP change.
- **Config hot-reload** (ADR-006): `SIGHUP` re-reads config and atomically swaps the provider/model/pricing topology with no restart; governance counters, keystore, and audit chain persist; a bad config rolls back.
- **Provider visibility** (ADR-005): read-only `GET /admin/config` and a console **Providers** tab show wired providers, endpoints, auth modes, and model routing — never a secret value.
- **Console operator dashboard** (ADR-002): token-gated SPA with Overview, Virtual keys, Providers, Governance, and Quickstart tabs; data-free static assets behind a strict CSP.
- **Governance views + one-click audit verify** (ADR-003 #2): per-team quota-utilization gauge and cumulative budget spend, plus `GET /admin/audit/verify` (per-sink hash-chain check, complete-prefix tolerant of a live writer).
- **Chargeback report** (ADR-007): `inferplane report` aggregates settled µUSD by team (or resolved model) from the audit log to CSV — exact integer-micros money, no float drift.
- **Per-team admin authorization + admin-action audit** (ADR-004): OIDC team-members issue/revoke keys only for their teams; every admin mutation and denial is an audit event.

### Changed
- Admin key management, config view, and audit verify are unified behind a single `AdminAuth` accepting static tokens or OIDC ID tokens on one bearer header.

## [0.1.0]

### Added
- Anthropic Messages ingress (`/v1/messages`, `/v1/messages/count_tokens`, `/v1/models`) with verbatim, cache-safe body forwarding.
- OpenAI Chat Completions ingress (`/v1/chat/completions`, `/v1/models`) with canonical-schema conversion.
- Virtual keys (`ik_...`) with team RBAC and per-key allowed-model lists; SHA-256 hashed at rest, shown once.
- Two-phase governance: per-team rate limits (TPM/RPM), daily token quotas, and monthly USD budgets with `block`/`warn` policies.
- Integer-microUSD pricing with round-half-even and TTL-tiered prompt-cache rates; `on_missing: allow` for self-hosted chargeback.
- Tamper-evident audit log: per-instance SHA-256 hash chain, disk WAL (`buffer_then_block`), and the `inferplane audit verify` command.
- Providers: Anthropic direct, Amazon Bedrock (Claude via InvokeModel, others via Converse), and any OpenAI-compatible endpoint, with priority fallback and per-provider circuit breakers.
- Prometheus `/metrics` on the admin plane using OpenTelemetry GenAI semantic conventions, plus a 9-panel Grafana dashboard.
- Optional self-terminated TLS on the data plane for non-Kubernetes deployments.
- Packaging: multi-stage `CGO_ENABLED=0` static Docker image (distroless/nonroot) and a Helm chart (ConfigMap config, IRSA ServiceAccount, `existingSecret` reference).

### Security
- Config rejects inline secrets; credentials are referenced only via `env:`/`file:`/`secret:`.
- The gateway never forwards the client key upstream and never exposes its upstream keys to clients.
- `/metrics` carries no secret or `key_id`, and bounds label cardinality with a `_rejected` sentinel on pre-resolution 403/404 paths.
- `count_tokens` always returns 200 to avoid crashing Claude Code.

[0.2.0]: https://github.com/inferplane/inferplane/releases/tag/v0.2.0
[0.1.0]: https://github.com/inferplane/inferplane/releases/tag/v0.1.0

---

<a id="korean"></a>

# 한국어

이 프로젝트의 모든 주요 변경 사항은 이 파일에 기록됩니다.
이 문서는 [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)를 기반으로 하며,
[Semantic Versioning](https://semver.org/spec/v2.0.0.html)을 따릅니다.

## [Unreleased]

### 추가됨
- **`inferplane login` / `token` / `logout`** (ADR-028): 사람을 위한 OIDC 로그인 — 손으로 복사한 만료 없는 키 대신, IdP 세션을 자동 갱신되는 단기 가상 키로 교환. 신규 옵트인 데이터 플레인 엔드포인트 `GET /v1/auth/config`, `POST /v1/auth/key`, `DELETE /v1/auth/key`; 관리 콘솔과 별개의 OIDC 클라이언트; IdP refresh token은 절대 디스크에 캐시하지 않음. CI/서비스 계정 발급(`inferplane keys create`, 선언적 `virtual_keys`)은 그대로.
- **모델 단위 폴백** (ADR-029): 아직 설정되지 않은 모델(예: 새로 나온 Claude 버전)을 하드코딩된 클라이언트가 요청해도 404 대신 설정된 다른 모델로 서빙합니다 — config `model_fallbacks`로 명시하거나, 아무 설정 없이도 기본 동일 패밀리 휴리스틱(`claude-opus-5` → 그 아래로 설정된 가장 높은 `claude-opus-*` 버전)이 적용됩니다. 설정된 모델이라도 업스트림이 미등록으로 거부(404, 또는 Bedrock `ValidationException`)하면 기존 우선순위 폴백 체인 내에서 폴백 모델로 전환됩니다. 치환은 RBAC에 대해 fail-closed(요청한 이름만 허용된 키는 거부되며 절대 조용히 다운그레이드되지 않음)이며 `x-inferplane-model-fallback`으로 알립니다.

### 수정됨
- **비용 정산이 서로 독립적인 5가지 이유로 틀려 실트래픽에서 0 µUSD가 나오던 문제** (ADR-030). 스트리밍 요청이 **output 토큰만** 과금 — input·프롬프트 캐시 카운트는 `message_start`에 실리는데 정산부가 한 번도 읽지 않았습니다(실측: 52가 정답인데 5 µUSD, 10.4배 저과금. Claude Code 트래픽은 거의 전부 스트리밍). 캐시 쓰기가 항상 싼 5분 티어로 과금되어 1시간 티어(input의 2배)가 도달 불가능. `input_per_mtok`/`output_per_mtok`만 선언한 config는 **모든 캐시 토큰이 0원** — 이제 캐시 요율을 input에서 파생합니다(0.1×/1.25×/2×, Anthropic·Bedrock 공개 표 양쪽에서 검증). Bedrock 리전 프리픽스(`global.`/`us.`/`apac.`)가 요율표를 완전히 빗나갔고, Bedrock Converse는 캐시 토큰을 누락(InvokeModel은 보존). OpenAI 호환 인그레스는 캐시된 프롬프트 토큰을 풀 요율로 과금(과과금). 중간에 끊긴 스트림은 이미 전달된 토큰을 과금하지 않았습니다.
- `pricing.on_missing: "block"`이 죽은 설정이던 문제 — `allow`와 동일하게 동작해서, 오퍼레이터가 거부된다고 믿은 미과금 트래픽이 무료로 서빙됐습니다. 이제 부팅 시점과 런타임 모두에서 집행되며, 인식할 수 없는 값은 조용히 `allow`가 되는 대신 로드 에러입니다.
- `POST /admin/keys`로 키를 발급하는 비관리자 OIDC 신원이 `owner`를 임의 값으로 지정할 수 있어 팀원이 키를 남의 이름으로 귀속시킬 수 있던 문제 — 서버가 항상 `owner`를 호출자 본인의 검증된 subject로 덮어씀.

### 변경됨
- **브레이킹 (`pricing.on_missing`이 `"block"`인 경우에만):** 요율이 없는 라우트가 하나라도 있으면 게이트웨이가 부팅을 거부하고 해당 라우트를 나열합니다. 기본값 `allow`에서는 크게 로그만 남기고 계속합니다. 마이그레이션: 누락된 요율을 `pricing.overrides`에 선언(모델당 숫자 2개 — 캐시는 파생)하거나 `on_missing`을 `"allow"`로 설정하세요.
- `pricing.version`이 요율표에 이름을 붙이고 모든 감사 레코드의 `cost.pricing_version`에 실립니다. 이전에는 전부 override된 표에서도 `"bundled"` 하드코딩이었습니다 — 이제 분쟁이 생긴 청구를 그 금액을 만든 요율에 고정할 수 있습니다.

## [0.2.0] - 2026-06-14

### 추가됨
- **관리 플레인 무료 OIDC SSO** (ADR-004): IdP(Dex/Keycloak/Okta) ID 토큰을 issuer JWKS로 검증하고 `groups` 클레임을 팀에 매핑; 정적 관리자 토큰은 break-glass로 유지. 리소스 서버 전용 — 리다이렉트/세션/쿠키·CSP 변경 없음.
- **Config hot-reload** (ADR-006): `SIGHUP`으로 config를 재로드하고 프로바이더/모델/pricing 토폴로지를 무중단 원자 교체; 거버넌스 카운터·키스토어·감사 체인 유지; 잘못된 config는 롤백.
- **프로바이더 가시성** (ADR-005): 읽기 전용 `GET /admin/config`와 콘솔 **Providers** 탭이 연결된 프로바이더·엔드포인트·인증 모드·모델 라우팅을 표시 (시크릿 값은 절대 미표시).
- **콘솔 운영자 대시보드** (ADR-002): Overview/Virtual keys/Providers/Governance/Quickstart 탭의 토큰 게이트 SPA; strict CSP 뒤의 데이터 없는 정적 자산.
- **거버넌스 뷰 + 원클릭 audit verify** (ADR-003 #2): 팀별 쿼터 이용률 게이지·누적 예산 지출, `GET /admin/audit/verify`(sink별 해시체인 검증, 라이브 writer의 부분 라인 허용).
- **차지백 리포트** (ADR-007): `inferplane report`가 감사 로그에서 settled µUSD를 팀(또는 resolved 모델)별 CSV로 집계 — 정수 µUSD 정확 금액, float 오차 없음.
- **팀별 관리 권한 + 관리 행위 감사** (ADR-004): OIDC 팀 멤버는 자기 팀 키만 발급/폐기; 모든 관리 변경·거부가 감사 이벤트.

## [0.1.0]

### Added
- 캐시 안전 본문 verbatim 전달을 갖춘 Anthropic Messages 인그레스(`/v1/messages`, `/v1/messages/count_tokens`, `/v1/models`) 추가.
- canonical schema 변환을 갖춘 OpenAI Chat Completions 인그레스(`/v1/chat/completions`, `/v1/models`) 추가.
- 팀 RBAC와 키별 허용 모델 목록을 갖춘 가상 키(`ik_...`) 추가; 저장 시 SHA-256 해시, 1회 표시.
- 2단계 거버넌스 추가: 팀별 rate limit(TPM/RPM), 일일 토큰 쿼터, 월별 USD 예산과 `block`/`warn` 정책.
- round-half-even과 TTL 계층 프롬프트 캐시 단가를 갖춘 정수 microUSD pricing 추가; 자체 호스팅 차지백용 `on_missing: allow`.
- 변조 감지 감사 로그 추가: 인스턴스별 SHA-256 해시 체인, 디스크 WAL(`buffer_then_block`), `inferplane audit verify` 명령.
- 공급자 추가: Anthropic 직접, Amazon Bedrock(Claude는 InvokeModel, 그 외 Converse), 모든 OpenAI 호환 엔드포인트, 우선순위 폴백과 공급자별 서킷 브레이커.
- OpenTelemetry GenAI 시맨틱 컨벤션을 사용하는 관리 플레인 Prometheus `/metrics`와 9패널 Grafana 대시보드 추가.
- 비-Kubernetes 배포를 위한 데이터 플레인 자체 종단 TLS(선택) 추가.
- 패키징: 멀티스테이지 `CGO_ENABLED=0` 정적 Docker 이미지(distroless/nonroot)와 Helm 차트(ConfigMap config, IRSA ServiceAccount, `existingSecret` 참조) 추가.

### Security
- config가 인라인 시크릿을 거부; 자격 증명은 `env:`/`file:`/`secret:`로만 참조.
- 게이트웨이는 클라이언트 키를 상위로 전달하지 않고, 상위 키를 클라이언트에 노출하지 않음.
- `/metrics`는 시크릿·`key_id`를 담지 않으며, 사전 해석 403/404 경로에서 `_rejected` 센티넬로 레이블 카디널리티 제한.
- `count_tokens`는 Claude Code 크래시 방지를 위해 항상 200 반환.

[0.2.0]: https://github.com/inferplane/inferplane/releases/tag/v0.2.0
[0.1.0]: https://github.com/inferplane/inferplane/releases/tag/v0.1.0
