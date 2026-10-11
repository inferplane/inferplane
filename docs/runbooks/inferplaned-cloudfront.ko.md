---
translation_source: runbooks/inferplaned-cloudfront.md
translation_source_sha256: 68db4d33730a9255ac263eb684242471e2e30417b6b2e743ef8bf94eaedba502
---

# CloudFront로 control plane 대시보드 배포 {#deploy-the-control-plane-dashboard-through-cloudfront}

## 정적 자산과 금융 상태 분리 {#separate-static-assets-from-financial-state}

선택적인 CloudFront 배포는 `internal/controlplane/ui/static/`의 **데이터 없는
셸만** 게시합니다. inferplaned·mayu·DB·정책 저장소·금융 권한을 재빌드하거나
재시작하지 않습니다. 메모리 전용 CP의 최근 사용량을 단순 화면 교체 때문에 잃지
않도록 하는 방식입니다. CP의 내장 콘솔은 그대로 존재하며 CDN은 새 정적 릴리스를,
API는 기존 CP 이미지를 사용할 수 있습니다.

암호화·버전 관리되는 비공개 S3 REST 버킷을 SigV4 OAC로 기존 distribution만
읽을 수 있게 하고 공개 접근은 차단합니다. 정적 경로는 `/ui/`, `/ui/index.html`,
`/ui/app.js`, `/ui/style.css`만 정확히 일치시킵니다. **`/ui/*` 전체를 S3로
보내지 마세요.** `/ui/auth/config`는 서비스 origin의 공개·비밀 없는 SSO 설정
엔드포인트에 남아야 합니다. `/v1alpha1/*`·추론·관리 경로의 origin·Authorization
전달·쿼리 처리는 그대로 유지합니다. CloudFront Function은 `/`와 `/ui`만 `/ui/`로
리다이렉트하며 나머지 요청은 변경하지 않습니다.

정적 캐시는 끄고 `Cache-Control: no-store`를 사용합니다. Cognito 로그인 origin을
포함한 CP의 정확한 CSP와 nosniff·no-referrer·frame 차단을 유지합니다. 게시 대상은
셸뿐입니다. 설정·환경 파일·자격 증명·정책 문서·사용량을 업로드하지 마세요.
새 컴퓨팅 인스턴스나 distribution은 필요하지 않지만 S3 저장·요청과 CloudFront
Function 요청에는 일반적인 소액 AWS 비용이 발생합니다.

## 준비와 검토 {#prepare-and-review}

지원 리소스는 `deploy/cloudfront/dashboard-resources.yaml`, 보안 규칙은
`deploy/cloudfront/dashboard-security.guard`에 있습니다.
`prepare-cloudfront-dashboard.py`는 후보·롤백 템플릿을 준비하며 **배포하지 않습니다**.
원본 `get-template` 응답이나 JSON 템플릿을 받으며 기존 논리 ID `Distribution`이
필요합니다. YAML TemplateBody를 읽을 때는 작성 도구 의존성으로 PyYAML이 필요합니다.

1. 기존 템플릿·파라미터 이름·distribution 설정·ECS 배포와 task 상태를 비공개
   디렉터리(umask 077)에 보관합니다. 기존 `/ui/` 응답의 정확한 CSP를 수집하고
   토큰·DSN을 로그에 남기지 않습니다.
2. 지원 템플릿을 cfn-lint·cfn-guard로 검증하고 change set과 사전검증 이벤트를
   생성·검토한 뒤 실행합니다.
3. 셸 파일 3개의 해시를 계산합니다. 변경하지 않는 `releases/<content-hash>/`
   접두사 아래 정확한 object 키 4개를 올리고 올바른 Content-Type과 no-store를
   지정합니다. `ui/` object 자체가 index.html입니다. 공개 website hosting이
   아니므로 S3가 자동 index document를 제공하지 않습니다.
4. **원본** 템플릿과 **실제 운영** distribution으로 주 스택 후보를 준비합니다.
   기존 도메인·ACM 인증서·비공개 VPC origin을 보존합니다. 편집하지 않은 scalar
   GetAtt 표현을 정규화하지 마세요. 표현 변환만으로 ECS task-definition 교체가
   발생할 수 있습니다. 기존 스택 파라미터는 모두 이전 값을 사용합니다. 도구는
   알 수 없는 origin ID나 기존 edge 함수를 몰래 교체하지 않고 거부합니다.
5. 후보와 롤백을 lint하고 주 change set에서 **Distribution만 replacement 없이
   수정되는지** 확인합니다. ECS service·TaskDef·IAM·네트워크·상태 리소스가
   나타나면 중단하고 후보를 고칩니다.
6. ETag로 동시 변경을 확인하고 검토한 change set을 실행합니다. 스택
   UPDATE_COMPLETE와 distribution Deployed까지 기다린 뒤 `/`, `/ui`, `/ui/*`를
   무효화합니다.

```bash
python3 scripts/prepare-cloudfront-dashboard.py \
  --template "$PRIVATE_DIR/stack-template.before.json" \
  --distribution-config "$PRIVATE_DIR/cloudfront.before.json" \
  --support-outputs "$PRIVATE_DIR/support.outputs.json" \
  --release-prefix "$RELEASE_PREFIX" \
  --output "$PRIVATE_DIR/template.dashboard.json"
python3 -m unittest discover -s tests/deployment -v
cfn-lint --regions ap-northeast-2 -t deploy/cloudfront/dashboard-resources.yaml
```

## 검증과 롤백 {#verify-and-roll-back}

배포 파일 해시와 릴리스 manifest, 루트 리다이렉트, 정확한 CSP·no-store, 직접 S3
비인증 접근 차단, bearer 없는 보호 API의 401, 설정된 자격 증명으로 읽기의 200,
실제 브라우저 탐색·모바일·Disconnect·Cognito authorization 리다이렉트를 확인합니다.
IdP 리다이렉트 확인은 사람의 SSO 로그인 완료나 쓰기 권한 승인 증거가 아닙니다.
실제 금융 정책을 변경하여 쓰기를 시험하지 마세요. ECS task ID와 service revision도
변하지 않았는지 확인합니다.

도구는 후보 옆에 `.rollback.json`과 `.rollback.parameters.json`을 만듭니다.
도메인·TLS·VPC origin을 포함해 **실제 운영 기준 상태**로 복구하며, 이 수동 변경을
빠뜨린 옛 템플릿으로 되돌리지 않습니다. 롤백도 Distribution만 수정되는
CloudFormation change set으로 검토·실행하고 같은 UI 경로를 무효화합니다.
지원 자산은 유지·비공개 상태로 남으며 연결 중인 버킷·함수를 삭제하지 마세요.

다음 릴리스는 새 해시 접두사에 올린 뒤 현재 템플릿의 `DashboardReleasePrefix`
파라미터만 바꿉니다. 나머지 파라미터는 보존하고 change set을 다시 검토합니다.
준비 도구는 최초 연결만 담당하며 기존 edge association의 자동 교체를 하지 않습니다.
SSO origin이 바뀌면 지원 스택의 CSP 파라미터도 함께 검토하세요.

화면 사용법과 지원 범위는 [대시보드 운영 문서](inferplaned-dashboard.md)를 참고하세요.
