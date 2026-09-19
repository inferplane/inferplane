---
translation_source: getting-started/quickstart.md
translation_source_sha256: db979a3236ef0fb29bf0251ff91c6e85e1bfe0a95323f864489c91f393b501c4
---

# 첫 요청 보내기 {#make-your-first-request}

로컬 게이트웨이를 실행하고 가상 키를 발급한 뒤 Anthropic Messages 요청을 보냅니다. 이 평가 구성은 SQLite, 루프백 리스너, 로컬 감사 파일을 사용하며 공유 집행을 배포하지 않습니다.

## 준비 사항 {#before-you-start}

Git, `go.mod`에 지정된 Go 도구 체인, curl, OpenSSL, 예제 모델 접근 권한이 있는 Anthropic API 키가 필요합니다. 실제 요청에는 공급자의 정상 요금이 부과됩니다. 다른 공급자는 [자체 호스팅 예제](../../examples/config.selfhosted.json)를 수정하거나 [클라이언트·공급자 가이드](clients.md)를 참고하세요.

## 1. 빌드하고 비공개 상태 디렉터리 준비 {#1-build-and-prepare-private-state}

Bash 터미널에서 저장소 루트를 기준으로 실행합니다.

```bash
git clone https://github.com/inferplane/inferplane.git
cd inferplane
CGO_ENABLED=0 go build -trimpath -o bin/mayu ./cmd/mayu
umask 077
mkdir -p .local/inferplane
```

[빠른 시작 설정](../../examples/config.quickstart.json)은 현재 디렉터리 기준 `.local/inferplane/`에 키와 감사 파일을 저장합니다. 두 리스너 모두 `127.0.0.1`에 바인딩됩니다.

## 2. 서버 자격 증명 설정 {#2-supply-server-credentials}

```bash
read -rsp 'Anthropic API key: ' ANTHROPIC_API_KEY
echo
export ANTHROPIC_API_KEY
INFERPLANE_ADMIN_TOKEN="$(openssl rand -hex 32)"
export INFERPLANE_ADMIN_TOKEN

bin/mayu pricing check --config examples/config.quickstart.json
```

설정에는 비밀 값의 참조만 들어갑니다. 공급자 키는 서버 터미널에만 보관하세요. `pricing check`가 통과해야 계속 진행할 수 있습니다. 내장 가격표는 내부 회계용 추정치이며 현재 공급자 견적이 아닙니다. 금전 한도 평가 전에 계정의 모델 접근 권한과 적용 요금을 확인하세요.

## 3. 클라이언트 키 발급 및 게이트웨이 실행 {#3-issue-a-client-key-and-start-the-gateway}

```bash
bin/mayu keys create \
  --config examples/config.quickstart.json \
  --team demo --models claude-sonnet-4-6

bin/mayu serve --config examples/config.quickstart.json
```

첫 번째 출력 줄인 가상 키를 비밀 관리 도구에 보관하세요. 평문은 한 번만 표시됩니다. 다음 줄의 `key_id`는 메타데이터이며 인증에 사용할 수 없습니다. `--config`를 사용하면 키 발급과 게이트웨이가 같은 데이터베이스를 사용합니다.

## 4. 다른 터미널에서 확인 {#4-verify-from-another-terminal}

```bash
curl --fail-with-body http://127.0.0.1:9090/readyz

read -rsp 'Virtual key: ' INFERPLANE_API_KEY
echo
export INFERPLANE_API_KEY

curl --fail-with-body http://127.0.0.1:8080/v1/messages \
  -H "x-api-key: $INFERPLANE_API_KEY" \
  -H 'anthropic-version: 2023-06-01' \
  -H 'content-type: application/json' \
  -d '{
    "model": "claude-sonnet-4-6",
    "max_tokens": 64,
    "messages": [{"role": "user", "content": "Reply with: gateway connected"}]
  }'
```

준비 상태 응답은 HTTP 200이어야 하며, Messages 응답에는 텍스트와 사용량이 포함됩니다. 401이면 가상 키 또는 데이터베이스가 다른지 확인하세요. 업스트림 접근 오류이면 서버의 공급자 자격 증명과 모델 권한을 확인합니다.

클라이언트 터미널에서 Claude Code를 실행하려면 다음과 같이 설정합니다.

```bash
ANTHROPIC_BASE_URL=http://127.0.0.1:8080 \
ANTHROPIC_API_KEY="$INFERPLANE_API_KEY" \
claude --model claude-sonnet-4-6
```

## 5. 기록 확인과 종료 {#5-inspect-and-stop}

관리 콘솔 주소는 `http://127.0.0.1:9090/admin/ui/`입니다. 인증이 필요한 작업에는 서버의 관리 토큰을 사용합니다. 저장소 루트의 다른 터미널에서 다음 명령을 실행하세요.

```bash
bin/mayu report --file .local/inferplane/audit.jsonl --by team,model
bin/mayu audit verify --file .local/inferplane/audit.jsonl
```

Ctrl-C로 서버를 종료합니다. 키 기록과 감사 증거를 보존하려면 상태 파일을 유지하세요. 로컬 금전·요청률·토큰 카운터는 메모리에 있으므로 재시작해도 예산 상태가 영속적으로 이어지는 것은 아닙니다.

이어서 [배포 프로파일](deployment-profiles.md), [보안 경계](../operations/security.md), [라우팅·정책](../policy-routing.md)을 확인하세요. 인증, TLS, 네트워크 제어를 먼저 정하지 않은 채 바인딩 주소만 변경해 빠른 시작 리스너를 외부에 노출하지 마세요.
