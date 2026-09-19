---
translation_source: onboarding.md
translation_source_sha256: 0a5e7e18e653819bcb97b39573d6b24d3f1e04312d9d82cb65acc7d747bb0b87
---

# 개발자 온보딩 {#developer-onboarding}

## 빠른 시작 {#quick-start}

### 1. 준비 사항 {#1-prerequisites}

- [ ] `go.mod`에 맞는 Go 도구 체인 설치, `go version` 확인.
- [ ] 저장소 접근 권한.
- [ ] 선택적으로 컨테이너용 Docker, 클러스터용 Helm·kubectl.
- [ ] 로컬 실제 호출용 업스트림 자격 증명(예: `ANTHROPIC_API_KEY`).

### 2. 설정 {#2-setup}

```bash
# Clone and fetch dependencies
git clone https://github.com/inferplane/inferplane.git
cd inferplane
go mod download

# Build the static binary
CGO_ENABLED=0 go build -trimpath -o bin/mayu ./cmd/mayu
```

### 3. 검증 {#3-verify}

```bash
go test ./... -race      # full suite, race detector
go vet ./...             # static checks
gofmt -l .               # must print nothing

```

실행은 [첫 요청 가이드](getting-started/quickstart.md)를 따르세요. 쓰기 가능한 상태를 준비하고 비밀 값을 셸 이력에 넣지 않으며 키 발급·서버에 같은 설정을 사용합니다. DB 통합 시험은 `INFERPLANE_TEST_PG_DSN`·`KEYSTORE_TEST_POSTGRES_DSN` 모두 같은 일회용 서비스를 가리켜야 합니다. DB 스킵은 검증 통과가 아닙니다.

## 프로젝트 개요 {#project-overview}

- `CLAUDE.md`: 프로젝트 맥락과 규칙.
- [아키텍처](architecture.md): 현재 구조.
- [초기 설계](specs/2026-06-10-inferplane-gateway-design.md): v0.1의 역사적 배경.
- [구현 레퍼런스](reference/INDEX.md): 계층별 세부 사항.
- [설계 결정](decisions/): ADR 원문.

## 개발 흐름 {#development-workflow}

- 브랜치 접두사: `feat/`, `fix/`, `docs/`, `refactor/`, `chore/`.
- Conventional Commits와 **DCO 서명**(`git commit -s`)을 사용합니다.
- 공급자 PR은 `providers/<name>/`, `cmd/mayu/main.go`의 blank import, 공급자 문서만 수정합니다. 핵심 내부 구현을 바꾸지 않습니다.
- 제출 전 `go test ./... -race`, `bash tests/run-all.sh`를 실행합니다.

## 핵심 개념 {#key-concepts}

- **가상 키:** `ik_...` 클라이언트 자격 증명. SHA-256 저장, 한 번만 표시.
- **정규 스키마:** 프로토콜 변환용 Anthropic 상위 집합.
- **원문 전달:** 같은 프로토콜의 본문을 바이트 그대로 전달해 캐시 안전성 유지.
- **두 단계 집행:** 청구 전 PreCheck, 후속 Settle.
- **변조 탐지 감사:** 인스턴스별 SHA-256 체인, 오프라인 검증.

## 문제 해결 {#troubleshooting}

- 401: 잘못되거나 폐기된 키, 관리 API의 관리 토큰 누락 여부 확인.
- count_tokens: 항상 200 계약을 유지해야 합니다. non-200은 Claude Code를 중단시킬 수 있습니다.
- 캐시 적중 감소: 변환 중 cache_control 손상 여부와 동일 프로토콜 원문 전달 확인.
- 감사 체인 단절: 재시작 전체를 하나로 이어 검증하지 말고 인스턴스별 구간 확인.

## 참고 자료 {#resources}

- [아키텍처](architecture.md)
- [역사적 설계](specs/2026-06-10-inferplane-gateway-design.md)
- [Grafana 대시보드](../deploy/grafana/inferplane.json)
- [Helm 차트](../charts/inferplane/)
