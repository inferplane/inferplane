# 한국어 PII 분류기 샘플 (옵트인)

이 샘플은 inferplane의 `sensitiveData` 규칙에 학습 기반 한국어 PII 신호를
더합니다. 미세조정한 Laya 체크포인트를 게이트웨이 옆에서 실행하고,
`systemone` 분류기가 요청에 개인정보가 있는지 묻습니다. 분류기는 라우팅만
제한합니다. Enforce에서 탐지되면 요청을 내부 모델에만 보내고, 요청 본문은
바꾸지 않습니다.

| 파일 | 용도 |
|---|---|
| `../config.pii-classifier-ko.json` | 전체 게이트웨이 설정: `pilot-ko` 팀에 Shadow로 적용한 분류기 |
| `governance.yaml` | 개인정보 규칙: 탐지되거나 검사할 수 없으면 `private-coder`로만 `InternalOnly` |
| `../helm.pii-classifier-ko.yaml` | Helm 값: Laya 사이드카와 같은 분류기 설정 |
| `laya-serve/` | 이미지: 체크포인트를 구운 CPU용 Laya 0.4.6, 오프라인, non-root |
| `model-card.md` | 가중치와 함께 공개하는 모델 카드 |
| `training/` | 체크포인트를 만든 학습 레시피 |

## 로컬 실행

```bash
docker run -d --rm --name laya-ko -p 127.0.0.1:8095:8095 -e LAYA_HOST=0.0.0.0 \
  ghcr.io/inferplane/laya-ko-pii@<digest>
INFERPLANE_ADMIN_TOKEN=<admin token> PII_KO_PUBLIC_KEY=<provider key> \
  go run ./cmd/mayu serve --config examples/config.pii-classifier-ko.json
```

컨테이너 포트는 호스트 루프백에만 엽니다. 실행 전에 설정의 예시 공급자를
실제 값으로 바꾸세요.

## Kubernetes

```bash
helm install inferplane charts/inferplane -f examples/helm.pii-classifier-ko.yaml \
  --set piiClassifier.laya.image.digest=<digest>
```

사이드카는 게이트웨이 파드 안의 `127.0.0.1`에서만 수신합니다. Service 포트가
없고, 프로브도 컨테이너 안에서 실행합니다.

## 프로필

- **CPU(기본 제공).** 1,000자 한국어 윈도마다 약 0.5초가 걸리고, 준비까지 약
  7~14초가 걸립니다. 그래서 샘플은 `max_windows: 4`, `timeout_ms: 3000`, Shadow
  모드로 파일럿 팀 하나에만 적용합니다. 캐시되지 않은 윈도가 그보다 많은
  요청은 처음 4개만 읽고 나머지를 `abstain`으로 셉니다. 그래서 Clear가 되지
  않으며, 앞부분 윈도가 캐시된 뒤의 요청이 나머지를 읽습니다.
- **GPU(실제 트래픽용).** amd64에서 CUDA torch로 `laya-serve/Dockerfile`을
  빌드하고, GPU 노드 풀에서 같은 네임스페이스의 ClusterIP Service로
  실행합니다. 게이트웨이 파드만 허용하는 NetworkPolicy를 두고,
  LoadBalancer나 공개 엔드포인트는 쓰지 않습니다. `max_windows: 64`로 두고
  `timeout_ms`는 측정한 p95보다 크게 잡습니다.

## 근거

- **문서 단위.** 독립 합성 한국어 평가셋 2,236행에서 F1 0.953, FPR 0.036,
  AUROC 0.993입니다. 같은 셋에서 게이트웨이 정규식 탐지기는 F1 0.478입니다.
  자세한 내용은 `model-card.md`를 보세요.
- **코딩 어시스턴트 요청 안에서**(2026-10-16, `SHA256SUMS`의 가중치로 `laya-serve/`에서 빌드한 이미지로 측정):
  감싼 본문 F1 `0.951`, FPR `0.032`, abstain 또는 오류 `1`건.

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
- 분류기 탐지에는 구간 마스킹이 없습니다. Mask 규칙은 대신 `onUninspectable` 동작을 적용합니다(정규식 탐지는 그대로 마스킹합니다).
- 내부 자체 호스팅 분류기만 지원합니다.

가중치: Hugging Face의 `<namespace>/laya-ko-pii`, revision `<revision>` (Apache-2.0).
