---
translation_source: reference/security.md
translation_source_sha256: 06a8a8cf9c1d196902c226fede41c6cd7987d9038c6c49fa62a8d8e8395bb443
---

# 보안 {#security}

### 1. 개요 {#1-overview}

가상 키 인증, 팀 RBAC, 키·비밀 분리, 인라인 비밀 거부, 선택적 자체 TLS, 비밀을 누출하지 않는 메트릭이 공통 보안입니다. CLAUDE.md의 필수 불변 조건입니다.

### 2. 구성 요소 {#2-components}

| 구성 | 경로 | 역할 |
| --- | --- | --- |
| 데이터 인증 | `internal/server/auth.go` | KeyAuth가 가상 키를 Principal로 해석 |
| 관리 인증 | `internal/server/adminauth.go` | 하나의 Bearer에서 정적 비상 토큰·OIDC를 완전 분기. 401·403 구분과 거부 감사 |
| OIDC 검증 | `internal/adminauth/` | 형태 판정, 그룹→팀, 알고리즘 고정, aud/azp, ±60초 오차, JWKS 부정 캐시 |
| 콘솔 SSO | `internal/server/adminui/static/app.js` | 브라우저 Code+PKCE, 게이트웨이는 리소스 서버. sessionStorage에는 verifier/state/nonce만 일시 저장, ID 토큰은 메모리만. 오류보다 state를 먼저 확인하고 textContent·replaceState·nonce 검사를 적용. 발견 엔드포인트 HTTPS 필수 |
| 콘솔 CSP | `adminui.go`, gateway.go의 ssoConnectSrc | login_origins가 있으면 self·issuer origin·허용 로그인 origin만 connect-src에 추가. script/style은 self. 없으면 기존 CSP·숨긴 SSO 유지 |
| CLI 로그인 | `cmd/mayu/login.go`, `internal/server/authapi/` | 콘솔과 별도의 client_id·검증기, 루프백 PKCE. HTTPS 또는 루프백, 발견 주소는 issuer 동일 출처·하위 도메인. issuer/client_id TOFU 고정, reset 없는 변경 거부. refresh token 캐시 없음. 만료·owner는 서버 결정, 주체별 발급 제한 |
| 설정 조회 | `internal/server/configapi/` | 비밀 값을 담을 수 없는 토폴로지 표현. 참조 이름·IAM 모드만 표시 |
| RBAC | `internal/keystore/keystore.go` | 팀·모델의 Principal.Allows |
| 교차 모델 폴백 재검사 | `internal/router/router.go` | 초기 허용 검사 뒤 추가된 폴백도 FilterModelAllowed로 다시 확인. 미설정 요청 이름만 허용된 키는 거부하며 다른 모델로 조용히 전환하지 않음 |
| 키 해시 | `internal/keystore/sqlite.go` | SHA-256 저장·평문 1회 표시. 선언형 키도 key_ref 사용, 인라인 금지 |
| TLS 검증 | `internal/server/tls.go` | 인증서·키 한쪽만 설정하면 거부 |
| 비밀 참조 | `internal/config/config.go` | env/file/secret 참조, 인라인 api_key 거부 |
| 자격 증명 중개 | `controlplane/broker.go`, `proxy/credentials.go`, `bedrock/client.go` | 최대 1시간 STS 세션. 읽을 수 없는 호스트 환경에서 노드 Bedrock IAM을 제거할 수 있음. 개발자 소유 호스트에서는 우회 방지가 아님. 하트비트와 다른 전용 토큰, JWT bearer는 검증 없이 403. 소스 누락·최초 조회 실패·알 수 없는 인증 모드·원격 평문 HTTP는 거부. 기본 AWS 체인으로 대체하지 않음 |
| 메트릭 보호 | `internal/metrics/metrics.go` | key_id·비밀 라벨 없음, _rejected로 카디널리티 제한 |

중개 경로는 자격 증명 필드를 로그에 남기지 않습니다. 조회의 non-2xx·디코딩 오류에는 상태와 고정 문자열만 사용합니다. JSON 오류가 시간 값 등을 노출할 수 있어 그대로 감싸지 않습니다. STS 실패도 고정 502 본문입니다. v1 dataplane ID는 호출자 주장 값이므로 실제 장비 증거가 아니며 세션에는 전체 bedrock:Invoke 권한이 있습니다. 네트워크 조건 SourceIp·SourceVpce는 운영자가 역할에 적용해야 합니다.

### 3. 주요 결정 {#3-key-decisions}

- 클라이언트 키를 업스트림에 전달하거나 공급자 키를 클라이언트에 노출하지 않습니다.
- 메트릭은 비인증이지만 비밀·key_id가 없고 라벨 수가 제한됩니다.
- 해석 전 403·404에는 센티널 모델 라벨을 사용해 공격자 입력으로 시계열이 늘어나지 않게 합니다.
- 미설정 요청 모델만 허용된 키는 RBAC에서 403이며 폴백으로 대신 처리하지 않습니다. 나중 추가한 모든 폴백도 재검사합니다.
- 중개 실패는 노드 AWS 신원으로 대체하지 않고 자격 증명 엔드포인트에는 OIDC 분기가 없습니다. 두 조건을 동작 변경에 실패하는 테스트로 고정합니다.

### 4. 코드 위치 {#4-code-pointers}

- `internal/server/auth.go`: 가상 키와 빈 키 우회 방지.
- `internal/config/config.go`: 비밀 참조 해석·인라인 거부.
- `anthropicapi/messages.go`·`openaiapi/chat.go`: 403·404의 _rejected 라벨.

### 5. 관련 문서 {#5-cross-references}

관련 모듈은 keystore·audit·metrics입니다. ADR-004, ADR-026, ADR-028, ADR-029, ADR-040과 [운영 절차](../runbooks/), [CLI 로그인](../runbooks/cli-login.md), [보안 정책](../../SECURITY.md)을 참고하세요.

### 민감 정보 목적지 제한 · ADR-043 {#sensitive-data-destination-restrictions-adr-043}

InternalOnly는 승인된 정규 모델과 매 시도의 명시적 internal 공급자를 모두 요구합니다. 누락·알 수 없는 경계는 신뢰하지 않습니다. 일치 규칙의 교집합을 사용하고 Block이 우선합니다. 안전한 체인이 없으면 마스킹·승인·공급자 호출 전에 거부합니다. Shadow도 개인정보를 집행하며 컨텍스트 선호 실패는 이미 안전한 경로를 유지합니다. 원래 모델 RBAC가 먼저 통과해야 하고 새 대안에는 가격·컨텍스트·기능·수신 프로토콜 검사가 필요합니다.

로컬 탐지 범위는 유한한 이메일·전화·Luhn 카드·SSN·IPv4·주민등록번호이며 보편적인 개인정보 탐지가 아닙니다. 불투명·알 수 없는 내용은 검사 불가이고 파싱·취소 실패는 민감 정책에서 거부합니다. 기존 선택적 마스킹은 별개로 모든 검사 대상을 다루지는 않습니다. 공급자 라벨은 운영자 선언이지 실제 엔드포인트 검증이 아닙니다. 새 감사·헤더·메트릭에 프롬프트·탐지 값을 넣지 않고 라우팅 메트릭은 팀·모드·사유만 사용합니다.

거부된 분산 개인정보 세대는 유효 복구 전까지 해당 주체를 차단합니다. 로컬 시작 정책도 토폴로지 검사 후 서비스합니다. 새 규칙 전에 모든 바이너리·CRD를 올리고 첫 CP 동기화 전 보호에는 require_sync, 정책 나이에는 max_policy_age를 사용합니다. 계산 API는 준비되지 않거나 오래되거나 거부된 상태에서 로컬/200입니다. [한계·배포](../policy-routing.md)를 참고하세요.

ADR-044의 유한한 완전 Mask는 외부 체인 반환 전에 수행하며 InternalOnly와 함께여도 필요합니다. 안전하지 않은 구조·숫자 변경, 남은 탐지, 불완전 변환은 거부합니다. 내부 모델 라벨만으로 Responses 원격 도구의 외부 호출을 허용할 수 없습니다. 엄격 예산은 거부된 정책 갱신을 차단하고 이후 컨텍스트·어피니티·폴백을 모두 필터링합니다. 세션 힌트는 범위가 지정된 HMAC 입력일 뿐 Principal이나 감사 차원이 아닙니다.
