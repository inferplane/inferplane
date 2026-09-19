---
translation_source: runbooks/claude-code-action.md
translation_source_sha256: e278fc882ffe416e33aa22f48eff95cf120474e06f3c4246bd54bbe4db2feebf
---

# 운영 절차: Claude Code GitHub Action · Amazon Bedrock {#runbook-claude-code-github-action-amazon-bedrock}

[anthropics/claude-code-action](https://github.com/anthropics/claude-code-action)으로 `@claude` 응답과 자동 PR 리뷰를 등록합니다. **GitHub OIDC**를 통해 **Amazon Bedrock**을 사용하며 Anthropic API 키나 장기 AWS 키는 필요하지 않습니다.

## 제공 기능 {#what-this-provides}

- `.github/workflows/claude.yml`: 이슈·PR 댓글·PR 리뷰·새 이슈의 `@claude` 언급에 응답.
- `.github/workflows/claude-code-review.yml`: PR 생성·새 커밋마다 CLAUDE.md 불변 조건에 근거해 리뷰.

## GitHub 호스팅 러너를 사용하는 이유 {#why-github-hosted-runners-not-self-hosted}

공개 저장소의 자체 호스팅 러너에서는 포크 PR의 임의 코드가 러너와 AWS 인스턴스 역할 자격 증명을 훔칠 위험이 있습니다. 호스팅 러너·OIDC는 단기·범위 제한 자격 증명을 사용하고 상시 인프라가 없습니다. 이 워크플로는 포크 PR을 건너뜁니다. 비공개 저장소나 VPC 전용 Bedrock이 필요해 자체 호스팅을 고려하더라도 포크 제한과 일회성 러너를 적용하세요.

## 사전 구성 · 저장소 관리자 {#prerequisites-repo-admin-code-alone-wont-activate-it}

### 1. AWS 계정의 GitHub OIDC 공급자 {#1-aws-github-oidc-provider-once-per-aws-account}

없으면 다음 공급자를 등록합니다.

- URL: `https://token.actions.githubusercontent.com`
- Audience: `sts.amazonaws.com`

### 2. 워크플로가 사용할 IAM 역할 {#2-aws-iam-role-the-workflow-assumes}

역할 ARN을 `AWS_ROLE_TO_ASSUME` 비밀에 넣습니다. 신뢰 정책을 이 저장소로 제한하세요.

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": { "Federated": "arn:aws:iam::<ACCOUNT_ID>:oidc-provider/token.actions.githubusercontent.com" },
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {
      "StringEquals": { "token.actions.githubusercontent.com:aud": "sts.amazonaws.com" },
      "StringLike": { "token.actions.githubusercontent.com:sub": "repo:inferplane/inferplane:*" }
    }
  }]
}
```

권한 정책은 필요한 Bedrock 호출만 허용합니다.

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": ["bedrock:InvokeModel", "bedrock:InvokeModelWithResponseStream"],
    "Resource": "arn:aws:bedrock:*::foundation-model/anthropic.claude-*"
  }]
}
```

리전 간 프로파일이 호출하는 기반 모델도 고려하고 실제 사용할 리전·모델·프로파일에 맞게 권한을 검증·제한하세요.

### 3. Bedrock 모델 접근 활성화 {#3-aws-enable-bedrock-model-access}

`AWS_REGION`의 Bedrock에서 사용할 Claude 모델과 리전 간 프로파일이 접근하는 리전의 권한을 준비합니다.

### 4. GitHub 비밀과 선택적 변수 {#4-github-repo-secret-optional-variables}

- 비밀 `AWS_ROLE_TO_ASSUME`: 2단계 역할 ARN.
- 선택적 `AWS_REGION`: 기본 `us-west-2`.
- 선택적 `CLAUDE_BEDROCK_MODEL`: 기본값은 현재 워크플로를 참고합니다. 허용한 모델과 리전에 맞는 ID를 지정하세요.

### 5. main에 워크플로 병합 {#5-merge-these-workflows-to-main}

해당 이벤트가 사용하는 기본 브랜치 워크플로가 등록되어야 자동 실행이 적용됩니다.

## 검증 {#verify}

- 시험 PR에서 Claude Code Review가 역할을 맡아 Bedrock을 호출하고 리뷰를 게시하는지 확인합니다.
- `@claude summarize this PR` 댓글에 응답하는지 확인합니다.
- 역할 획득 실패는 신뢰 정책 `sub`·`aud`와 ARN을 확인합니다.
- Bedrock AccessDeniedException은 모델 접근·권한·리전을 확인합니다.
- 포크 PR에 리뷰가 없으면 이 워크플로의 의도된 제한입니다.

## 참고 {#notes}

GitHub 토큰은 기본 `${{ github.token }}`과 제한된 permissions를 사용합니다. 리뷰 게시에는 pull-requests 쓰기가 필요합니다. Bedrock 경로에 별도 Claude GitHub App은 필요하지 않습니다. Claude가 푸시한 변경으로 CI를 다시 실행해야 하는 경우에만 사용자 지정 App 토큰을 검토하세요.

비용은 PR 수에 따라 늘어납니다. 범위를 조절하려면 경로 필터를 신중히 적용하되 필수 리뷰 커버리지를 우회하면 안 됩니다. `@claude` 작업은 실제 언급이 있어야 실행됩니다.

## PR #3 리뷰 후 적용한 보호 {#hardening-applied-follow-up-to-the-auto-review-of-pr-3}

- **언급 남용 방지:** `@claude`와 함께 작성자가 OWNER·MEMBER·COLLABORATOR여야 합니다. 임의 공개 사용자가 유료 호출을 실행하지 못하게 합니다.
- **SHA 고정 Actions:** checkout·AWS 자격 증명·Claude Action을 변경 가능한 태그 대신 커밋에 고정합니다. 갱신 시 `gh api repos/<owner>/<action>/commits/<tag> --jq .sha`로 확인합니다.
- **한 줄 claude_args:** 여러 줄 인수 해석으로 allowed-tools가 빠지지 않도록 합니다. CLI는 두 표기 `--allowed-tools`·`--allowedTools`를 받습니다.
- **최소 권한:** 불필요한 actions 읽기를 제거하고 이슈 트리거는 opened만 사용합니다.
- **신뢰 sub 범위:** PR OIDC 주체는 `repo:inferplane/inferplane:pull_request`이므로 main ref만 허용하면 리뷰가 깨집니다. 저장소 범위 `:*` 또는 main·pull_request를 명시적으로 허용합니다.
- diff의 프롬프트 인젝션 위험은 읽기·댓글 전용 도구 제한으로 완화합니다.

## PR #4 후속 검토 {#pr-4-auto-review-follow-up-verified-in-repo-no-workflow-changes}

두 번째 검토는 HIGH 없이 병합 가능으로 판단했습니다. MEDIUM 두 건은 다음과 같이 확인했습니다.

- `claude_args` 인용: 당시 고정된 Action의 `shell-quote` 토큰화가 쉼표·괄호를 포함한 allowed-tools 문자열을 단일 인수로 해석하므로 기존 인용은 정확했습니다.
- SHA 자동 갱신: 주간 GitHub Actions Dependabot이 새 버전 PR을 열며 동일한 Bedrock 리뷰를 받도록 구성했습니다.

후속 정리에서 필요 없는 `actions: read`를 제거하고 포크 제한의 OIDC 보안 근거를 주석에 복원했습니다. SHA 갱신은 위의 주간 Dependabot 구성으로 처리합니다.
