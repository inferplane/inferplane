---
translation_source: review-remediation-2026-09-21.md
translation_source_sha256: 6a986c0b661f03c2e3cadf156dd915650a2f57cc2d7482e45cfe02f61da0212e
---

# 리뷰 개선 조치 — 2026-09-21

기준: `6e3c4dc`(main). 출처: 분기된 `401dd20` 작업 트리를 검토한 운영자 로컬
`review/kimi-k3` 리뷰와 `5fb3f94`를 검토한 `review/fable-51` 리뷰입니다. 무시 목록에
있는 이 리뷰 디렉터리가 없어도 이 문서의 처리 내역은 유효합니다. 아래 Finding ID는
원래 보고서를 가리키며, 새로 검증한 심각도 등급이 아닙니다.

## 이번 패치: 공급자 회계

- **Fable B1:** Anthropic 비스트리밍 Complete와 Bedrock InvokeModel은 이제 성공 응답의
  JSON이 잘못됐거나 usage 객체가 없거나 null이면 고정된 Anthropic 형식의 502 업스트림
  오류로 거부합니다. 성공 프록시 응답을 반환하지 않으며, 합성 오류에 업스트림 내용을
  그대로 싣지 않습니다. 명시적인 0 usage는 계속 유효하고, 유효한 응답 바이트는 바뀌지
  않습니다. Anthropic의 non-2xx 전달과 토큰 계산 동작은 그대로입니다.
- **Fable B3:** TTL별 분류 없이 0이 아닌 cache-write 합계만 있는 Bedrock Converse SDK
  usage는 이제 기존 complete/stream 어댑터를 통해 `AccountingUncertain`을 전달합니다.
  더 싼 등급 기준의 가격 추정은 그대로입니다. 명시적 0 합계는 TTL 분할이 필요 없습니다.
  음수 합계는 정확한 정산의 근거가 아니라 여전히 불확실로 처리합니다.

이 변경은 실패한 호출에 대해 실제 비용을 지어내지 않습니다. 거부된 업스트림 응답에도
공급자 요금이 발생했을 수 있고, 일반 라우팅이 그 502를 재시도할 수 있습니다. 로컬
회계는 누락된 공급자 usage를 복구할 수 없습니다. ADR-045/046은 기존 시도 종료 경로로
불확실한 예약을 유지합니다. null은 아니지만 불완전한 usage 객체, usage 프레임 이전의
중단, OpenAI 형식의 cache-write 확장은 별도로 다뤄야 하며, 이 패치가 완전한 재무
대사를 주장하지는 않습니다.

ADR-045가 TTL 분할 없는 양수 cache 합계를 정확한 값으로 받아들인다는 보고서의 주장은
이 기준 시점에서 사실이 아닙니다. `requestpolicy.authorityUsage`는 이미 TTL 분할 없는
양수 합계를 거부합니다. B3 패치는 그 authority 게이트를 고치는 대신 공급자 관측
경계에서 불확실성을 보존합니다.

## main에서 이미 해결되었거나 대체된 항목

| 출처 | 처리와 근거 |
| --- | --- |
| Kimi F1 | 검토 대상이던 경쟁 stateless 어댑터는 main의 아키텍처가 아닙니다. ADR-043은 정책 기반 라우팅이며, ADR-047과 `internal/responses`가 유지되는 Responses 변환 경로입니다. 폐기된 어댑터를 통째로 옮기지 마세요. |
| Kimi F2 | PR #96(`a4eec84`)에서 외부 thinking Responses 처리와 회귀 테스트, 가격이 포함된 모델 예제를 커밋했습니다. 재현 가능한 소스 수정이며, 새로운 운영 수용 테스트는 아닙니다. |
| Kimi F3–F6, F8 | 이전 어댑터·변환 구현을 가리키는 지적입니다. 정확한 위치를 현재 main의 결함으로 볼 수 없습니다. 현재의 역할 순서, 커스텀 도구, 버퍼링, service-tier 계약은 각각 별도로 평가해야 합니다. |
| Kimi harness-CI 권고 | `.github/workflows/ci.yml`이 이미 `bash tests/run-all.sh`를 실행합니다. |
| Fable B5 | PR #97(`6e3c4dc`)이 정책 오버레이 아래에서 더 엄격한 기본 RPM/TPM을 유지합니다. |
| Fable S2, D2 | PR #97이 감사 보장의 범위를 명확히 하고 컨트롤 플레인 우회 위협 모델 문서를 추적합니다. 필수 sink 내구성을 구현하지는 않습니다. |

## 남은 우선순위 — 이번 패치로 닫히지 않음

1. **Fable A1 / A4 (P0):** 모든 공급자 시도·폴백에서 가드레일 호환성을 강제하고, 의도적인
   zero-sink 설정 계약과 함께 명시적인 기본 Helm 감사 sink를 제공합니다. 가드레일 설정만으로
   적용되었다는 증거로 제시하면 안 됩니다. 감사 sink 검증에는 마이그레이션·픽스처 커버리지가
   필요하며, 시작 시 오류를 추가하는 것만으로는 충분하지 않습니다.
2. **Fable B2 / R4, A2:** 요금을 지어내지 않고 usage 이전 중단을 테스트합니다. 감사 sink
   실패, WAL 복구, readiness 동작을 함께 정의합니다.
3. **Fable B4 / B6:** 지원하는 OpenAI cache-write 와이어 필드를 픽스처로 확정하고, 설정·저장·
   대체된 대상 전반의 가격을 커버합니다.
4. **Fable S1 / S3, R1–R3:** 관리 TLS, `on_exceeded` 검증, Responses 거부 관측성과 종단 간
   거버넌스 테스트.
5. **Kimi F7 / F9와 Fable P2:** 새 커밋의 DCO 강제와 모델 한도 근거. 검증되지 않은 리뷰
   주장을 근거로 공개된 히스토리를 다시 쓰거나 운영 모델 한도를 바꾸지 마세요.
6. **Kimi admin A1과 Fable A3, D1, D3–D5, P1, P3–P6:** 컨트롤 플레인 쓰기 권한·감사, panic
   경로 회계, API 레퍼런스 완성도, 프로필별 주장, 릴리스·메인테이너 절차. 관련 없는
   아키텍처 변경을 회계 수정에 묶지 말고 열어 두세요.

## 검증

새 거부·flat-cache 테스트는 수정 전에 실행해 보고된 결함에서 실패하는 것을 확인했습니다.
수정 후 대상 공급자 테스트가 `-race`로 통과했습니다. 명시적 0 usage, 원본 바이트 보존,
non-success 전달, Converse 불확실성 전파, 기존 InvokeModel 변환 테스트가 포함됩니다.

전체 로컬 검증은 이 환경의 소켓 제한을 받습니다. `httptest`와 런처의 loopback 서버가
바인드할 수 없습니다. 권한 상승 서비스는 `no policy-permitted compatible Responses target`을
반환했으며, 어떤 제한도 우회하지 않았습니다. 머지 전에 권한이 있는 환경이나 CI에서 전체
race·harness 성공과 Postgres 통합 커버리지를 확인해야 합니다.
