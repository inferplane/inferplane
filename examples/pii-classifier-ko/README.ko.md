# 한국어 PII 분류기 샘플 (옵트인)

이 샘플은 inferplane의 `sensitiveData` 규칙에 학습 기반 한국어 PII 신호를
더합니다. 포함된 레시피로 직접 미세조정한 Laya 체크포인트를 게이트웨이 옆에서
실행하고, `systemone` 분류기가 요청에 개인정보가 있는지 묻습니다. 분류기는
라우팅만 제한합니다. Enforce에서 탐지되면 요청을 내부 모델에만 보내고, 요청
본문은 바꾸지 않습니다.

inferplane은 이 샘플의 가중치와 이미지를 배포하지 않고 레시피만 제공합니다.
체크포인트를 직접 학습해 이미지에 굽고, 둘 다 자체 비공개 저장소와
레지스트리에 보관하세요.

| 파일 | 용도 |
|---|---|
| `../config.pii-classifier-ko.json` | 전체 게이트웨이 설정: `pilot-ko` 팀에 Shadow로 적용한 분류기 |
| `governance.yaml` | 개인정보 규칙: 탐지되거나 검사할 수 없으면 `private-coder`로만 `InternalOnly` |
| `../helm.pii-classifier-ko.yaml` | Helm 값: Laya 사이드카와 같은 분류기 설정 |
| `laya-serve/` | 이미지 레시피: 직접 학습한 체크포인트를 구운 CPU용 Laya 0.4.6, 오프라인, non-root |
| `model-card.md` | 이 레시피로 만든 기준 체크포인트의 모델 카드(배포하지 않음) |
| `training/` | 체크포인트를 학습하는 레시피 |

## 분류기 이미지 빌드

1. [`training/README.md`](training/README.md)로 체크포인트를 학습합니다.
   런처가 출력하는 작업의 모델 아티팩트에 `laya-ko-pii/`가 들어 있습니다.
2. 아티팩트를 받아 이미지에 굽습니다. `laya-serve/weights.txt`에 적힌 파일만
   복사하며, 하나라도 없으면 빌드가 실패합니다.

```bash
aws s3 cp s3://<bucket>/jobs/<job>/output/model.tar.gz .
tar xzf model.tar.gz laya-ko-pii
docker build --build-context localweights=laya-ko-pii \
  -t laya-ko-pii:local examples/pii-classifier-ko/laya-serve
```

쓰기 전에 체크포인트를 자체 독립 평가셋(`training/indep/`)으로 채점하세요.
새로 학습하면 기준 체크포인트의 수치가 그대로 재현되지 않습니다.

## 로컬 실행

```bash
docker run -d --rm --name laya-ko -p 127.0.0.1:8095:8095 -e LAYA_HOST=0.0.0.0 \
  laya-ko-pii:local
INFERPLANE_ADMIN_TOKEN=<admin token> PII_KO_PUBLIC_KEY=<provider key> \
  go run ./cmd/mayu serve --config examples/config.pii-classifier-ko.json
```

컨테이너 포트는 호스트 루프백에만 엽니다. 실행 전에 설정의 예시 공급자를
실제 값으로 바꾸세요.

## Kubernetes

클러스터가 이미지를 받을 수 있는 비공개 레지스트리에 푸시하고(노드
아키텍처가 섞여 있으면 두 아키텍처 모두), 그 digest로 설치합니다.

```bash
docker buildx build --platform linux/amd64,linux/arm64 --push \
  --build-context localweights=laya-ko-pii \
  -t <registry>/laya-ko-pii:<revision> examples/pii-classifier-ko/laya-serve
helm install inferplane charts/inferplane -f examples/helm.pii-classifier-ko.yaml \
  --set piiClassifier.laya.image.repository=<registry>/laya-ko-pii \
  --set piiClassifier.laya.image.digest=<digest>
```

두 값이 모두 없으면 차트는 사이드카를 렌더링하지 않습니다.

사이드카는 게이트웨이 파드 안의 `127.0.0.1`에서만 수신합니다. Service 포트가
없고, 프로브도 컨테이너 안에서 실행합니다.

사이드카에 bearer 토큰을 요구하려면 `piiClassifier.laya.apiKeySecret`에
`LAYA_API_KEY` 키를 담은 Secret을 지정합니다. 차트는 이 값을 사이드카에
`LAYA_API_KEY`로 전달합니다. 게이트웨이 컨테이너는 `secrets.existingSecret`의
모든 키를 환경 변수로 읽으므로(`envFrom`), 같은 `LAYA_API_KEY`를 그 Secret에도
넣거나 `apiKeySecret`에 그 Secret을 지정하고, 분류기 항목에
`api_key_ref: {env: LAYA_API_KEY}`를 추가합니다.

## 프로필

- **CPU(Dockerfile 그대로).** 1,000자 한국어 윈도마다 약 0.5초가 걸리고, 준비까지 약
  7~14초가 걸립니다. 그래서 샘플은 `max_windows: 4`, `timeout_ms: 3000`, Shadow
  모드로 파일럿 팀 하나에만 적용합니다. 캐시되지 않은 윈도가 그보다 많은
  요청은 처음 4개만 읽고 나머지를 `abstain`으로 셉니다. 그래서 Clear가 되지
  않으며, 앞부분 윈도가 캐시된 뒤의 요청이 나머지를 읽습니다.
- **GPU(실제 트래픽용).** amd64에서 CUDA torch로 `laya-serve/Dockerfile`을
  빌드하고, GPU 노드 풀에서 같은 네임스페이스의 ClusterIP Service로
  실행합니다. 게이트웨이 파드만 허용하는 NetworkPolicy를 두고,
  LoadBalancer나 공개 엔드포인트는 쓰지 않습니다. `max_windows: 64`로 두고
  `timeout_ms`는 측정한 p95보다 크게 잡습니다.

## 토큰 예산과 잘림

laya-serve는 윈도마다 약 980토큰(이 레시피로 학습한 체크포인트의 `max_len`
1024에서 질문 몫을 뺀 값)을 읽고, 나머지는 오류 없이 잘라 낸 뒤 응답의 `usage`에 그 사실을
보고합니다. 게이트웨이는 토큰 수를 추정하지 않습니다. 서버가 잘랐다고 보고한
윈도는 읽은 부분에서 탐지되지 않으면 `abstain`이므로 Clear가 되지 않습니다.
2026-10-16에 체크포인트 토크나이저로 잰 1,000자당 토큰 수:

| 텍스트 | 토큰 |
|---|---|
| 영어 산문 | 169 |
| Go 코드 | 366 |
| 이모지 채팅 | 471 |
| 한국어 산문 | 596 |
| base64 | 691 |
| 한자 | 749 |
| sha256 잠금 파일 줄 | 805 |
| JSON 숫자 | 850 |
| UUID 목록 | 934 |
| 전화번호·주민등록번호 목록 | 1,035 |

기본값 `window_runes: 1000`은 일반적인 코딩 텍스트에 맞습니다. 밀도가 높거나
의도적으로 만든 윈도는 예산을 넘을 수 있고(전화번호·주민등록번호 목록은
1,000자에서 이미 넘고, 한국어 산문은 약 1,650자부터 넘습니다), 그러면
fail-closed가 됩니다(Enforce에서는 `onUninspectable`). `LAYA_JEV_STRICT`는 설정하지 마세요. 이
설정은 `usage`에서 잘림 정보를 지우므로, 기본값 `truncation: "reported"`에서는
모든 결과가 `abstain`이 됩니다. strands-decider는 잘림을 보고하지 않으므로,
너무 긴 윈도를 HTTP 422로 거부하는 `--strict-window`로 실행하고
`truncation: "rejected"`를 설정합니다. `rejected`에서는 게이트웨이가 그 주장을
그대로 믿습니다. 입력을 조용히 자르는 서버(예: `--strict-window` 없는
strands-decider)는 앞부분만 보고 판정하므로, 그 판정이 Clear로 캐시될 수
있습니다.

## 근거

아래 수치는 이 레시피로 만든 기준 체크포인트(Laya v3 seed 1, revision
`laya-ko-pii-v3s1`)의 결과이며, 이 체크포인트는 배포하지 않습니다. 직접 학습한
체크포인트는 결과가 다릅니다. 같은 레시피의 다른 두 seed는 F1 0.934와 0.939를
기록했습니다.

- **문서 단위.** 기준 체크포인트는 독립 합성 한국어 평가셋 2,236행에서 F1
  0.953, FPR 0.036, AUROC 0.993입니다. 같은 셋에서 게이트웨이 정규식 탐지기는
  F1 0.478입니다. 자세한 내용은 `model-card.md`를 보세요.
- **코딩 어시스턴트 요청 안에서**(2026-10-16, 기준 체크포인트로 `laya-serve/`에서 빌드한 이미지로 측정):
  감싼 본문 F1 `0.952`, FPR `0.035`, abstain 또는 오류 `1`건.

평가 데이터는 모두 합성입니다. 실제 트래픽에서의 정확도는 Shadow로 측정하기
전까지 알 수 없습니다.

## 롤아웃

1. 파일럿 팀에 Shadow로 적용하고, 감사 기록의 `would_restrict`를 그 팀
   트래픽에서 라벨링한 표본과 비교합니다.
2. Enforce 전에 민감 유형별 게이트를 정해 둡니다: 미탐률(이름과 속성이 다른
   turn에 있는 경우 포함), 일반 코딩 요청의 오탐률과 abstain 비율, p95 추가
   지연과 자원 사용량, 분류기 장애 시의 대체 경로.
3. `inferplane_pii_classifier_windows`와 캐시 적중률을 지켜봅니다. 캐시가 비어
   있으면 긴 첫 요청은 abstain이 되고, 게이트웨이 프로세스마다 캐시가 따로
   채워집니다.
4. 게이트를 충족했을 때만 Enforce로 올리고, `timeout_ms`는 측정한 p95보다
   크게 잡습니다. Enforce에서는 타임아웃이 곧 요청 제한입니다.
5. 다시 학습한 체크포인트는 새 `revision`을 받고 Shadow부터 다시 시작합니다.

## 한계

- 평가는 합성 데이터의 문서 단위 결과입니다.
- 게이트웨이는 긴 요청을 윈도 단위로 읽습니다. 서로 다른 윈도나 turn에 있는
  이름과 속성은 함께 판정되지 않습니다. 판정 캐시는 윈도 전체만 저장하므로
  함께 판정된 내용을 나누거나 합치지 않습니다.
- CPU 프로필은 긴 코딩 세션을 감당하지 못합니다.
- 키별 속도·예산 한도는 분류기 부하를 제한하지 않습니다. 분류는 승인보다 먼저
  라우팅 중에 실행되므로, 한도를 넘은 키도 거부되기 전에 요청마다 최대
  `max_windows`번의 추론을 일으킵니다. 사이드카는 담당하는 팀에 맞춰 크기를
  정하세요. laya-serve는 동시에 `LAYA_MAX_CONCURRENT`개(기본 16) 요청까지 받고
  나머지에는 503을 반환하며, 추론은 한 번에 하나씩 처리합니다. batch 모드는
  게이트웨이 요청마다 사이드카 요청 하나를 보냅니다. 포화되면 Enforce에서는
  fail-closed가 되고, Shadow에서는 지연이 최대 `timeout_ms`까지 늘어납니다.
  요청 간 상한은 후속 작업입니다.
- 분류기 탐지에는 구간 마스킹이 없습니다. Mask 규칙은 대신 `onUninspectable` 동작을 적용합니다(정규식 탐지는 그대로 마스킹합니다).
- 내부 자체 호스팅 분류기만 지원합니다.

이 레시피로 학습한 체크포인트는 Laya(Apache-2.0)와 mmBERT-base(MIT)에서
파생되므로 `NOTICE`를 함께 보관하세요.
