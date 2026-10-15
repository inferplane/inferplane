---
translation_source: reference/pricing.md
translation_source_sha256: 66f7cf338c51925ad7e94c2625a8847923d40c9ba28e3e88f799d5f9576f380c
---

# 응답 단위 컨텍스트 요금

mayu는 설치된 토폴로지의 가격 스냅샷으로 공급자 시도마다 정수 microUSD를 정산합니다. Codex reported_cost를 요구하거나 만들지 않습니다. 설정 단가 추정이며 세금·개별 할인·호스팅 도구 요금·미설정 서비스 구간은 포함하지 않습니다.

## 설정

최상위는 short 단가입니다. `short_context_limit`과 `long_context`를 함께 선언하면 응답 전체에 적용하는 구간 요금을 사용합니다.

```json
{
  "pricing": {
    "on_missing": "block",
    "version": "aws-gpt-standard-2026-10-02",
    "overrides": {
      "bedrock-responses-east": {
        "openai.gpt-6.1-sol": {
          "input_per_mtok": 2.2,
          "output_per_mtok": 11,
          "cache_read_per_mtok": 0.11,
          "cache_write_5m_per_mtok": 2.75,
          "cache_write_1h_per_mtok": 2.75,
          "short_context_limit": 272000,
          "long_context": {
            "input_per_mtok": 4.4,
            "output_per_mtok": 16.5,
            "cache_read_per_mtok": 0.22,
            "cache_write_5m_per_mtok": 5.5,
            "cache_write_1h_per_mtok": 5.5
          }
        }
      }
    }
  }
}
```

위 예제는 상용 regional Mantle Standard이며 Global CRIS 요금이 아닙니다. regional 가산 요금이 이미 들어 있으므로 10%를 다시 더하지 않습니다. Sol 6.1 캐시 읽기는 입력의 0.05배이며 Sol 6의 0.1배와 다릅니다. 단일 공개 캐시 쓰기 가격을 기존 TTL 두 필드에 넣는 것은 해당 TTL 지원을 뜻하지 않습니다. GPT-5.4/5.5는 각 구간에 `cache_write_unsupported: true`를 선언해 쓰기 단가 유도를 막습니다. 예상 밖 쓰기 사용량은 임의 단가나 무료가 아닌 비용 미확인으로 남깁니다. 명시 캐시 단가는 기존 비율 기본값보다 우선합니다.

누락·중첩·무료·미가격 long 구간, 음수·비유한·정수 microUSD 범위 초과 단가는 거부합니다. UI·재로드에서도 잘못된 정확 키가 기존 번들·리전 제거 키의 가격을 빌려 오지 못합니다. 고정 단가 선언은 기존 동작을 유지합니다. Standard 구간 요금 요청의 service_tier는 누락·null·default만 허용하며 다른 명시 값은 추론 전에 거부합니다.

## 구간 선택과 정산

정규 사용량은 서로 겹치지 않습니다. 비캐시 입력·캐시 읽기·5분 쓰기·1시간 쓰기를 한 번만 합쳐 오버플로 없이 비교합니다. 출력·추론 토큰은 경계 선택에 더하지 않으며 출력에는 추론이 이미 포함됩니다. 272,000 입력은 short, 272,001 입력부터 입력·캐시·출력 모두 long을 적용합니다. 초과 입력만 비싸지는 방식이 아닙니다. 세션 합계·스트림 조각 합계가 아니라 시도별 구간을 선택한 뒤 비용을 합칩니다. 토큰 종류별 기존 round-half-even microUSD 반올림은 유지합니다.

Global Sol 6.1의 비캐시 입력 300,000·출력 10,000은 $4/$15 단가로 **$1.35**, regional Mantle은 $4.40/$16.50으로 **$1.485**입니다. 272K는 가격 경계이며 최대 컨텍스트 윈도우가 아닙니다. 권한 승인 예약은 종류별 short/long 최대 단가와 올림을 사용하며 checked 정산도 동일한 선택기를 사용합니다. 미확인·잘못된 사용량은 영속 불확실성을 유지합니다. 정책·계정·grant·과거 audit는 다시 쓰지 않습니다.

## 증거와 운영

[검증된 가격 스냅샷](../../examples/pricing/bedrock-gpt-standard-2026-10-02.json)은 GPT 9개 공식 카드의 모델별 캐시 단가와 정확한 regional/global 구분을 담습니다. [native Sol 6.1 예제](../../examples/config.bedrock-gpt-6-1-sol.json)도 같은 regional 단가를 사용합니다. 출처·날짜는 스냅샷에 있으며 가격표가 모델 가용성·품질을 증명하지는 않습니다.

**게이트웨이를 먼저 업그레이드한 뒤** 추가된 설정 필드를 설치하세요. 이전 소비자는 새 필드를 무시하고 기본 단가만 청구할 수 있습니다. 전체 선언에 mayu pricing check를 실행하고 배포의 프로파일·readiness 요구를 유지하세요. 새 audit의 cost.amount_usd_micros·pricing_version·context_tier와 서로 겹치지 않는 토큰 종류를 확인하세요. 계산·audit·예산·사용량 telemetry·OTel은 같은 정산액을 사용합니다.

## 사용량 근거와 재시도 이벤트

새 감사 레코드의 `usage.evidence`에는 입력·출력·캐시 포함 입력 총량의 확인 여부와
`cache_write_ttl_known`, `accounting_uncertain`을 기록합니다. 이전 레코드의 필드 부재는
확인되지 않음을 뜻합니다. TTL 없는 캐시 쓰기 총량은 기존처럼 5m 요금으로 추정하지만,
5m 실측값으로 표시하지 않습니다. 불완전한 분해, 불일치, 30m 등 미지원 TTL, 잘못된 수량과
공급자가 표시한 불확실성도 보존합니다. 기존 정산과 authority 환불 조건은 바뀌지 않습니다.
중단된 응답은 감사 레코드의 `outcome.partial`도 확인해야 합니다.

TTL 분해가 일부만 있어도 평탄한 총량을 보존합니다. 예를 들어 총량 100과 5m 수량 40만
보고되면 quota와 컨텍스트 가격 구간 계산에 100을 반영합니다. 확인된 1h 수량은 해당 요금을
유지하고 나머지는 기존 5m 요금으로 추정합니다. 총량 확인은 TTL 확인이나 금전 예약 반환의
근거가 아닙니다. 음수·합계 오버플로·0이 아닌 완전한 분해와 총량의 불일치는 카운터 갱신 전에
거부합니다. 사용량이 없거나 잘못되면 최초 TPM 추정 차감을 유지합니다. 정산은 승인 건마다
한 번만 반영하며, 정산 대기 중인 TPM 기록은 정리 작업에서 삭제하지 않습니다.

`inferplane.cost.settled` 이벤트에는 해당 시도의 라우팅 모델·공급자 프로토콜·설정 이름·토큰·가격 버전·
구간·불확실성·중단 여부가 함께 들어갑니다. `gen_ai.usage.input_tokens`는 캐시 제외 값이며,
`inferplane.usage.input_tokens_total`은 확인 가능한 경우에만 캐시를 한 번 포함한 값을 기록합니다.
누락된 입력·출력 수량을 이벤트에서 실측 0으로 내보내지 않습니다. 모델은 설정된 업스트림 ID이며 원격 gateway가 실제 실행한 모델의 증명은 아닙니다. 부분 응답은 이벤트의 `accounting_uncertain`도 설정합니다.

같은 이벤트의 재전송은 생성된 불투명 `inferplane.cost.event_id`로 중복 제거합니다. 이 ID는 클라이언트 로그나
감사 레코드와의 공통 요청 ID가 아닙니다. span 비용은 마지막 정산 시도의 값이며 이벤트·감사 비용·
클라이언트 비용과 합산하면 안 됩니다. 스칼라 토큰 필드는 다음 시도의 0을 명시적으로 갱신하며, 이후 값이 미확인일 때도 증거 플래그를 따라야 합니다. 샘플링·부모 샘플링 결정·이벤트 제한·전송 유실 때문에 trace는 진단 자료이며 전체 청구 원장이 아닙니다. 비용 정산 없이 cost 이벤트를 만들지 않습니다. 현재 Claude Code 사용량 dashboard collector는 `inferplane` 서비스를 받지 않으므로 대시보드 수집과 출처 간 연결은 별도 작업입니다. 과거 audit를 소급 수정하지 않으며, 별도 표시한 과거 재계산 보고서도 정산 책임과 해시 기록을 바꾸지 않습니다.

## 가격 동기화의 범위

mayu pricing sync는 지원되는 bedrock 경로의 입력·출력만 생성합니다. bedrock_responses는 혼합 토폴로지에서도 미해결과 종료 1로 표시하며 Mantle 가격을 runtime에서 빌려 오거나 조용히 생략하지 않습니다. 모델 카드의 정확한 값을 검토해 선언하세요.

기존 컨텍스트 구간·명시 캐시 단가·쓰기 미지원·무료 선언은 입력·출력 전용 생성기가 바꾸지 못합니다. 보호는 정확 키 우선·기본 키 후순위 조회와 동일하며 해당 경로는 catalog를 호출하지 않습니다. 여러 가격 차원이나 하나의 불완전·제한 범위도 거부합니다. 해결된 경로만 부분 fragment를 만들 수 있으므로 종료 코드를 확인하세요. 도구는 리전 가격 동일성·빠진 서비스 구간·전체 공급자의 캐시 가격을 자동으로 증명하지 않습니다.
