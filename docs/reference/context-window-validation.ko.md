---
translation_source: reference/context-window-validation.md
translation_source_sha256: fb4febc542898e527cb81a873fddaf068e103bc3a994a2a2ccc4d97a4a944e5e
---

# 컨텍스트 윈도우 실호출 검증, 2026-10-02

최신 모델별 용량·지연·비용 실측은 [모델별 성능](../performance/models.md)에서
확인하세요. 이 문서는 10월 2일 클라이언트 프로파일 검증과 해당 설정의
한계를 보존합니다.

공개 모델마다 하나의 설정된 타깃을 가진 로컬 mayu 게이트웨이에 인증된 합성 텍스트 요청을 보냈습니다. 임의 도구·이력, 모델 품질, 다른 배포·리전 또는 정확한 최대값을 검증한 것은 아닙니다. [기계 판독 증거](../validation/context-window-probes-2026-10-02.json)에는 프롬프트·가상 키·계정·세션 식별자를 기록하지 않습니다.

| 공개 모델 | 선언값 | 성공한 실제 입력 토큰 | 결과 |
| --- | ---: | ---: | --- |
| `openai.gpt-6.1-sol` | 1,050,000 | 600,026 | 약 1,048,000 합성 단위는 분류되지 않은 upstream 400. 최대값 미확정. |
| `qwen.qwen3-coder-next` | 256,000 | 254,029 | 성공. 클라이언트 사용 가능 범위 204,800→243,200 조정 가능. |
| `global.xai.grok-4.7` | 500,000 | 430,037 | 성공. 클라이언트 사용 가능 범위 400,000→425,000 조정 가능. 최대값 미확정. |

Sol 6.1은 짧은 요청에서도 `reasoning.effort: "none"`을 거절했고, 이 옵션을 빼면 짧은 요청이 성공했습니다. 따라서 native 검사에는 effort를 지정하지 않고 브리지만 기존 none 값을 사용합니다. 장문 요청은 옵션을 빼도 400이었습니다. 게이트웨이가 upstream 오류 본문을 숨기므로 어느 400도 정확한 최대값의 증거로 승격하지 않습니다. Sol 6.1의 기존 클라이언트 상한·여유는 그대로이며, 운영 native 선언은 별도 작업 트리의 Converse 예제와 다릅니다.

설치된 Codex 0.159.3을 루프백 가짜 서버로 검증해, 상속된 `model_context_window=1048576`이 있어도 모델 전환 시 Grok 425,000·Qwen 243,200 사용 가능 토큰으로 바뀌는 것을 확인했습니다. 검증 후 로컬 설치 런처와 `icodex` 셸 연동을 갱신했으며 새 셸·클라이언트부터 운영자 프로필을 읽습니다. 게이트웨이와 전역 Codex 설정은 바꾸지 않았습니다.

전체 캠페인 한도는 $30이었습니다. 성공 사용량의 설정 단가 추정과 실패·미확인 호출의 보수적 예약액은 증거 파일에 따로 기록하며 AWS 청구서 대조값이 아닙니다. 실패를 무료로 가정하지 않습니다. 다음 Astra·Sol 6의 한도 근처 호출에 필요한 예약액이 남은 한도를 넘어서므로 두 모델은 미검증으로 남깁니다.

검토된 Qwen·Grok 조정을 사용하려면:

```bash
export INFERPLANE_CODEX_CONTEXT_PROFILES="$PWD/examples/codex/context-profiles.json"
icodex -m qwen.qwen3-coder-next
```

실행 도구는 시작할 때 프로필을 읽고 게이트웨이 선언 상한을 유지한 채 사용·압축 비율만 바꿉니다. Qwen은 95%/90%, Grok은 85%/82%입니다. 게이트웨이 선언값이 바뀌면 오래된 프로필을 거부합니다. 설치 후 클라이언트를 재시작하세요. 명시적인 호출자 압축 설정은 여전히 런타임 압축 시점에 영향을 줄 수 있습니다.

검사 도구는 `--execute`가 없으면 계획만 출력합니다.

```bash
python3 scripts/inferplane-context-probe \
  --config /path/to/gateway-config.json --model qwen.qwen3-coder-next \
  --units 254000 --max-spend-usd 30 --ledger /path/to/campaign.json
```

실행하려면 `INFERPLANE_API_KEY`를 비공개로 내보내고 `--execute`를 붙입니다. 같은 캠페인의 모든 모델·호출은 **동일한 ledger**를 사용해야 합니다. 도구는 호출 동안 ledger를 잠그고, 가장 높은 입력·캐시 단가의 예약액을 전송 전에 저장하며, 재시도·환경 프록시·리다이렉트를 사용하지 않습니다. 로컬 가격 override는 실제 배포의 가격 구간을 보수적으로 포함해야 합니다. 검색·호스팅 도구는 호출하지 않으며 provider 비밀 참조를 해석하지 않습니다. 미확인 실패는 전체 예약액을 유지합니다. 공개 모델이 같고 모델 대체 헤더가 없을 때만 성공을 검증 관측으로 기록합니다. 운영자 증거이며 금융 권위나 악의적·오래된 backend 선언에 대한 증명은 아닙니다.
