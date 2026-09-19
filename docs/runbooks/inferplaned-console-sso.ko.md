---
translation_source: runbooks/inferplaned-console-sso.md
translation_source_sha256: 7c42801345f31461fb9c0d3bf4f8d7fe6a912b379cb2b523f3e0f9e3acff3b04
---

# 운영 절차: inferplaned 콘솔 SSO {#runbook-inferplaned-console-sso-adr-037}

공유 `INFERPLANED_TOKEN`을 붙여 넣는 대신 회사 IdP로 사용량 콘솔 `/ui/`에 로그인합니다. 기존 정적 토큰은 mayu 사용량 전송·정책 하트비트에 그대로 사용됩니다. SSO는 추가 인증 경로입니다.

## 운영자 활성화 {#enable-it-operator}

inferplaned는 설정 파일 없이 컨테이너 환경 변수로 구성합니다.

```bash
INFERPLANED_OIDC_ISSUER=https://cognito-idp.ap-northeast-2.amazonaws.com/ap-northeast-2_XXXXXXXXX
INFERPLANED_OIDC_CLIENT_ID=<cognito app client id>
INFERPLANED_OIDC_GROUPS_CLAIM=cognito:groups
INFERPLANED_OIDC_ALLOWED_GROUPS=platform-admins,finance-readonly
INFERPLANED_OIDC_LOGIN_ORIGINS=https://<your-cognito-domain>.auth.ap-northeast-2.amazoncognito.com
```

| 변수 | 필수 여부 | 의미 |
| --- | --- | --- |
| `INFERPLANED_OIDC_ISSUER` | OIDC 변수 설정 시 | 절대 HTTPS URL, 풀 ID 뒤 추가 경로·쿼리·프래그먼트·userinfo 없음 |
| `INFERPLANED_OIDC_CLIENT_ID` | OIDC 변수 설정 시 | 비밀 없는 공개 앱 클라이언트 ID, 예상 토큰 `aud` |
| `INFERPLANED_OIDC_GROUPS_CLAIM` | 기본 `groups` | **Cognito는 `cognito:groups`로 설정** |
| `INFERPLANED_OIDC_ALLOWED_GROUPS` | OIDC 구성 시 필수 | 쉼표 구분. 하나라도 속하면 전체 콘솔 접근. 아직 팀별 범위 없음 |
| `INFERPLANED_OIDC_LOGIN_ORIGINS` | 선택적 | 브라우저가 FETCH하는 절대 HTTPS origin 목록. 콘솔 자신의 주소는 `'self'`가 처리. Cognito에서는 issuer와 다른 **Hosted UI 도메인**을 지정하며 issuer는 CSP에 자동 추가. 비어 있으면 API OIDC 검증만 하고 SSO 버튼·CSP 확장은 비활성화 |

LOGIN_ORIGINS에 콘솔 주소를 넣으면 리다이렉트·IdP 로그인은 되더라도 토큰 교환이 CSP에 막혀 수동 토큰 화면으로 돌아갈 수 있습니다. 환경 변수 변경에는 재시작이 필요하며 GovernancePolicy처럼 핫 리로드하지 않습니다.

### Cognito 앱 클라이언트 등록 {#register-a-cognito-app-client}

mayu 콘솔 SSO(ADR-026)와 같은 형태지만 두 콘솔을 모두 쓴다면 **별도 앱 클라이언트**로 등록합니다.

- **유형:** 공개 클라이언트, `GenerateSecret=false`. 브라우저 PKCE에서 JS에 넣은 비밀은 비밀이 아닙니다.
- **OAuth 흐름:** Authorization Code와 PKCE.
- **범위:** `openid`만 사용.
- **콜백:** 정확히 `https://<your-console-host>/ui/`. 와일드카드 없이 일치해야 합니다.
- **토큰 엔드포인트 CORS:** 브라우저 코드 교환이 가능하도록 구성합니다.
- **사용자 풀 가입:** 회사의 초대·관리자 생성 정책이면 `AllowAdminCreateUserOnly: true`(`selfSignUpEnabled: false`)를 적용합니다. 콘솔 클라이언트와는 별도의 풀 설정입니다.
- **Hosted UI 도메인:** `https://<domain>.auth.<region>.amazoncognito.com`을 LOGIN_ORIGINS에 추가합니다. `aws cognito-idp describe-user-pool --user-pool-id <pool> --query UserPool.Domain`으로 확인할 수 있습니다. SPA의 인증 리다이렉트와 토큰 `fetch()`가 접근하는 호스트이며 콘솔 주소가 아닙니다.

### 그룹 클레임 주의 사항 {#the-groups-claim-footgun-read-this-before-you-deploy}

**Cognito는 요청 범위와 무관하게 ID 토큰에 `cognito:groups`를 넣습니다.** 기본 `groups`를 유지하면 토큰 검증은 성공하지만 미들웨어가 없는 클레임을 찾아 후속 API가 `identity maps to no team`으로 403을 반환합니다.

```bash
INFERPLANED_OIDC_GROUPS_CLAIM=cognito:groups
```

`GET /ui/auth/config`와 IdP 리다이렉트는 정상인데 모든 데이터 호출이 403이면 이 값을 먼저 확인하세요.

### 시작 시 보호 검사 {#boot-time-guardrails}

- 다른 OIDC 변수가 있는데 허용 그룹이 없으면 시작을 거부합니다. 아무도 권한을 얻을 수 없는 구성을 런타임 오류로 미루지 않습니다.
- 점으로 구분된 base64url 세 부분의 **JWT 형태** `INFERPLANED_TOKEN`도 거부합니다. OIDC 검증기로 분기되어 정적 토큰으로 인증할 수 없으므로 일반 임의 문자열을 사용하세요.
- OIDC만 설정하고 정적 토큰을 생략하는 **SSO 전용 배포**도 비루프백 인증 요구를 충족합니다. 공유 정적 토큰을 완전히 제거할 수 있습니다.

## 브라우저 사용 {#use-it-operator-browser}

1. `https://<console-host>/ui/`를 엽니다.
2. OIDC와 브라우저 로그인 origin이 설정되어 있으면 수동 토큰 아래 SSO 버튼이 표시됩니다.
3. 버튼을 눌러 Cognito Hosted UI에서 로그인하면 `/ui/`로 돌아와 콘솔이 열립니다.
4. `ip_sso_verifier`·`ip_sso_state`·`ip_sso_nonce`는 왕복 중에만 sessionStorage에 저장하고 성공·실패 후 즉시 지웁니다. ID 토큰은 페이지 메모리에만 두며 디스크·localStorage·쿠키에 저장하지 않습니다.
5. 허용 그룹에서 자신을 모두 제거하고 다시 로그인해 403 거부를 확인합니다. 기본 팀을 자동 부여하면 안 됩니다.

수동 토큰 입력 기능은 남습니다. SSO 경로 자체가 설정된 정적 토큰을 비활성화하지는 않습니다.

## 머신 경로 확인 {#verify-the-machine-path-is-unaffected}

```bash
curl -sf -H "Authorization: Bearer $INFERPLANED_TOKEN" \
  https://<control-plane>/v1alpha1/usage?group_by=team
```

정적 토큰이 구성된 배포에서 OIDC 유무와 관계없이 동일하게 동작해야 합니다. mayu의 사용량 전송·정책 동기화는 비-JWT 정적 토큰을 사용하므로 OIDC 검증기로 가지 않습니다.

## 문제 해결 {#troubleshooting}

| 증상 | 원인·대응 |
| --- | --- |
| 로그인 후 모든 요청 403 | Cognito에서 GROUPS_CLAIM이 `groups`인지 확인하고 `cognito:groups`로 변경 |
| 허용 그룹 필수 오류로 시작 거부 | 다른 OIDC 설정 전에 하나 이상의 그룹 지정 |
| 정적 토큰 JWT 형태 오류 | ID 토큰 대신 일반 임의 비밀 문자열 사용 |
| SSO 버튼 없음 | OIDC·LOGIN_ORIGINS 설정 확인. OIDC 미구성 시 `/ui/auth/config`는 404 |
| 로그인 후 수동 토큰 화면 복귀 | LOGIN_ORIGINS에 Cognito Hosted UI 도메인 누락 여부와 브라우저 CSP `connect-src` 오류 확인 |
| `/ui/auth/config` 404 | OIDC 미구성 시 의도된 동작. 필수 환경 변수 설정 후 재시작 |
| 토큰 교환 CORS 오류 | IdP 토큰 엔드포인트·앱 클라이언트 설정 확인 |
| 반복 리다이렉트·콜백 거부 | 등록 URL과 콘솔 `/ui/`의 호스트·포트·끝 슬래시가 정확히 일치하는지 확인 |
