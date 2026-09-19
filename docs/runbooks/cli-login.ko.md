---
translation_source: runbooks/cli-login.md
translation_source_sha256: 76c81894558e0efe5b79ed8fb01f656c8d8ddd2178c64d9dbcb3f5755c9fd922
---

# 운영 절차: mayu login · CLI OIDC 로그인 {#runbook-mayu-login-cli-oidc-login-adr-028}

개발자가 회사 IdP로 인증하고 수명이 짧고 갱신 가능한 게이트웨이 가상 키를 받습니다. `ik_...`를 직접 복사할 필요가 없습니다. CI·서비스 계정은 기존 `mayu keys create` 또는 선언형 `virtual_keys`(ADR-023)를 계속 사용합니다.

## 운영자 활성화 {#enable-it-operator}

```json
{
  "server": {
    "admin_auth": {
      "oidc": {
        "issuer": "https://idp.example.com",
        "client_id": "inferplane-console",
        "cli_login": {
          "enabled": true,
          "client_id": "inferplane-cli",
          "key_ttl": "8h"
        }
      }
    }
  }
}
```

`cli_login.client_id`는 콘솔 `client_id`와 **달라야** 합니다(ADR-028). `key_ttl`은 설정 로드 시 15분~24시간 범위로 제한하며 기본값은 8시간입니다.

### 두 번째 IdP 앱 클라이언트 등록 {#register-a-second-idp-app-client}

CLI는 콘솔과 별개인 OAuth **공개 클라이언트**이며 비밀이 없습니다.

- **리다이렉트 URI:** `http://127.0.0.1/callback`. 일부 IdP는 등록 URI가 필요합니다. CLI가 임시 포트를 요청에 넣으므로 포트 없는 루프백 또는 IdP가 요구하는 정확한 URI를 등록하세요. Cognito 주의 사항은 아래와 같습니다.
- **허용 방식:** Authorization Code, PKCE 필수, 클라이언트 비밀 없음.
- **범위:** `openid`만 사용. CLI는 refresh token을 요청·저장하지 않으므로 `offline_access`를 사용하지 않습니다.
- **그룹 클레임:** RBAC에 필요하면 설정합니다. Cognito는 범위와 무관하게 `cognito:groups`를 내보냅니다. Okta·Keycloak은 별도 범위가 필요할 수 있으므로 이 클라이언트에도 허용하세요.

### 배포 시 영향 {#deployment-implication}

`GET /v1/auth/config`와 `POST /v1/auth/key`는 Claude Code가 사용하는 **데이터 영역** 포트에 있습니다. 콘솔 SSO와 달리 개발자가 관리 포트 `:9090`에 접근할 필요가 없습니다.

### 영속성 요구 {#persistence-requirement}

**`persistence.enabled: false` 상태에서 `oidc.cli_login`을 활성화하지 마세요.** 로컬 저장소의 Pod 재시작은 키 저장소·감사 WAL을 지웁니다. `key_ttl`마다 교체되면 발급 주체 기록이 빠르게 사라집니다. 일회용 개발 환경이 아니면 영속 볼륨과 내구성 있는 감사 저장 대상(`file`, `s3anchor`, ADR-012)을 먼저 구성하세요.

### Cognito 주의 사항 {#cognito-specific-note}

Cognito는 임의 포트의 `http://127.0.0.1:<any-port>/callback`을 허용하지 않고 정확한 URI 매칭을 요구합니다. 다음 중 하나를 사용하세요.

- 고정 CLI 포트를 등록하고 항상 `mayu login --port <그 포트>`로 실행합니다.
- 브라우저 흐름 대신 `mayu login --id-token-command`에 신뢰할 수 있는 도구를 지정해 유효한 IdP **ID 토큰**만 표준 출력에 내보냅니다. AWS STS `get-caller-identity`는 계정·ARN 메타데이터를 반환하며 ID 토큰이 아니므로 그 명령만으로 대체할 수 없습니다.

## 개발자 사용 {#use-it-developer}

```bash
mayu login --gateway https://gateway.example.com
```

- 허용 팀이 하나면 자동 선택합니다.
- 여러 팀이면 `--team <name>`을 지정합니다.
- 브라우저가 열립니다. `--no-browser` 또는 X11 없는 SSH에서는 URL을 표준 오류로 표시합니다. 로그인 후 키를 발급하고 다음과 같은 안내를 표시합니다.

```
logged in as team alpha; key ik_1a2b3c4d5e6f expires 2026-07-28T20:00:00Z

Claude Code — add to ~/.claude/settings.json (use an ABSOLUTE path to the binary):
  { "apiKeyHelper": "/usr/local/bin/mayu token",
    "env": { "ANTHROPIC_BASE_URL": "https://gateway.example.com", "CLAUDE_CODE_API_KEY_HELPER_TTL_MS": "3600000" } }
OpenCode / scripts:  eval "$(mayu token --export)"
```

`apiKeyHelper` 블록을 `~/.claude/settings.json`에 넣으세요. 바이너리는 **절대 경로**를 사용합니다. 주기적으로 다시 실행되는 이름만 지정하면 PATH 변조 위험이 있습니다.

**도우미 TTL은 키 TTL보다 충분히 짧아야 합니다.** 예시는 8시간 키에 `CLAUDE_CODE_API_KEY_HELPER_TTL_MS=3600000`(1시간)을 사용합니다. 서버 키 TTL을 줄이면 도우미 TTL도 줄이세요. 그렇지 않으면 만료 키가 캐시되어 401이 발생할 수 있습니다. 401이면 즉시 도우미를 다시 호출해 회복하지만 불필요한 오류가 생깁니다.

### mayu token {#mayu-token}

키 수명이 5분 넘게 남으면 네트워크 호출 없이 캐시 키를 출력합니다. `apiKeyHelper`에는 키만 전달합니다. 실제 터미널에서는 기본으로 `key_id`·만료만 표시하며 비밀 원문은 `--raw`, 환경 변수 내보내기는 `--export`를 사용합니다. 후자는 `ANTHROPIC_BASE_URL`·`ANTHROPIC_AUTH_TOKEN`을 내보냅니다.

만료 후 `login`에 `--id-token-command`를 사용했다면 도구를 다시 실행해 브라우저 없이 키를 재발급합니다. 그렇지 않으면 `session expired; run: mayu login`으로 실패합니다. IdP refresh token을 캐시하지 않으므로 키 TTL마다 대화형 재로그인이 필요할 수 있습니다.

### mayu logout {#mayu-logout}

캐시 키 폐기를 최선의 노력으로 요청하고 오프라인이어도 로컬 자격 증명 파일은 항상 지웁니다. 이미 로그아웃한 상태에서도 안전하게 실행할 수 있습니다.

## 문제 해결 {#troubleshooting}

| 증상 | 원인·대응 |
| --- | --- |
| `gateway has no CLI-discoverable login config` | `oidc.cli_login.enabled` 누락·비활성화. `--issuer`·`--client-id`를 직접 지정하거나 운영자가 활성화 |
| `entitled to multiple teams; specify --team` | 여러 팀에 매핑됨. `mayu login --team <name>` 사용 |
| `your identity maps to no gateway team` | 일치하는 `group_mappings`·`admin_groups` 없음. 관리자에게 추가 요청. 기본 팀을 자동 허용하지 않음 |
| `OIDC identity changed (issuer/client_id); pass --reset` | 해당 URL의 이전 신원과 TOFU 불일치. 의도한 변경일 때만 `--reset`, 아니면 접속 대상 변조 가능성을 확인 |
| 브라우저 콜백 시간 초과 | 3분 내 IdP 흐름 미완료. 브라우저 또는 출력 URL 확인 |
| 브라우저 자동 실행 안 됨 | SSH·WSL·헤드리스에서 정상일 수 있음. 표준 오류 URL을 수동으로 열거나 `--no-browser` |
| Claude Code 세션 중 401 | 도우미를 다시 호출하므로 교체·만료 키는 다음 요청에서 회복 |
| `GET /admin/keys`의 행 증가 | 키 TTL·발급 제한에 따라 개발자당 하루 약 1~3개 예상. 행 수와 무관하게 만료 시 조회 거부. `keys prune`은 미구현 후속 과제 |
