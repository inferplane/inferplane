**Bedrock 직접 사용과 inferplane 경유의 사용성 동등성 검토 — 2026-10-12**

상태: 소스·공식 문서·offline 재현을 바탕으로 한 개선안 보완.
기존 [정확성 개선안](2026-10-11-gateway-correctness-improvements.md)의 후속 검토다.
중앙 저장소와 운영 배포는 이번 범위에서 제외한다.
2026-10-12 검토 반영: native 상태 참조의 즉시 guard(PR-10a), 기존 model-substring
option 제거 규칙, Mantle `/v1` base path, endpoint별 stored/background/project 차이,
저장소 밖 증거 링크 표기를 보완했다.

**결론**

같은 client가 같은 endpoint/API/model의 지원 기능을 사용하고 정책상 허용된
요청이라면, base URL과 인증 설정을 바꾼 뒤 사용성 차이를 최소화하는 것은
현실적인 목표다. 현재 개선안의 오류 수정만으로 달성되지는 않는다.
네이티브 API를 보존하는 경로, 상태 API의 권한·회계 계약, 직접/경유 비교 검증을
추가해야 한다.

정책의 거부·명시적인 Mask·강제 model substitution, 인증 주체와 IAM attribution,
추가 hop의 지연은 별도의 의도된 차이다. 이것들을 없애거나 보안·재무 검사를
우회하여 “완전 동일”을 만들지 않는다. 서로 다른 API 사이의 bridge도 네이티브
API의 모든 의미를 동일하게 구현한다고 주장하지 않는다.

**1. 비교 기준부터 고정한다**

동등성의 단위는 `client/version + endpoint + API + model/profile ID + region +
요청 옵션 + 세션 기능`이다. Runtime의 Converse 직접 호출과 gateway의
Responses→Converse bridge를 비교하면 처음부터 다른 계약을 비교하게 된다.

비교할 세 기준 경로:

- Runtime의 OpenAI-compatible Chat Completions 직접 호출과 경유 호출.
- Runtime의 OpenAI-compatible Responses 직접 호출과 경유 호출.
- Mantle의 모델별 Chat/Responses 직접 호출과 경유 호출.

기존 Converse/Invoke와 다른 wire의 portable bridge는 별도의 compatibility
검증 대상으로 유지한다.
AWS SDK의 Converse 직접 호출도 기준에 포함한다면 현재 gateway에는 native
Converse/ConverseStream ingress가 없다는 차이를 먼저 해소해야 한다.
현재 Invoke-shaped Bedrock ingress를 Converse API와 동일하다고 부르지 않는다.
이 경우 AWS eventstream·native body·SDK의 gateway 인증 연동까지 PR-11의
추가 protocol profile로 설계한다.

요청과 응답의 동등성은 인증 material과 선언된 model alias rewrite를 제외하여
검증한다. 실제 model switching과 masking이 발생한 요청은 같은-model native
동등성 결과와 섞지 않고 해당 정책의 결과로 따로 측정한다.

**2. 최신 AWS API 계약에 맞춰 endpoint 구분을 갱신해야 한다**

2026-10-12 확인한 AWS 공식 문서는 Runtime에서 OpenAI-compatible Responses와
Chat Completions를 제공한다고 명시한다. 이전 “Runtime은 Converse 중심,
네이티브 Responses는 Mantle 전용” 가정으로 구현 범위를 정하면 안 된다.

| 구분 | Runtime 네이티브 OpenAI API | Mantle 네이티브 OpenAI API |
|---|---|---|
| 주요 base path | `/openai/v1` | 모델별 `/v1`(대부분) 또는 `/openai/v1`; 잘못된 prefix는 `isn't supported on this route`로 거부 |
| 인증 | Bedrock API key 또는 SigV4 | Bedrock API key 또는 SigV4 |
| Responses | 지원 model/profile에 제공; AWS 권장 endpoint | 지원 model에 제공; Mantle 전용 기능이 필요할 때의 compatibility endpoint |
| 실행 방식 | 항상 synchronous | synchronous와 background |
| client-side function tools | 제공되는 profile에서 지원 | 제공되는 profile에서 지원 |
| server-side tools·background | Runtime Responses는 지원하지 않음 | 제공되는 model/profile에서 지원 |
| 저장된 Responses | 지원; default project만, serving region 규칙 존재 | 지원; project 규칙 존재 |
| cancel | 대상 없음(background 없음) | background response에 적용 |
| Runtime model 지정 | 매 요청 명시, 해당 모델의 system inference profile 확인 | previous response에서 model 상속 가능 |
| OpenAI Models API | 제공하지 않음 | 제공 |
| Bedrock Guardrails | Runtime의 지원 API에 제공 | 이 검토의 Mantle 경로에서는 지원하지 않음 |

특정 model이 한 endpoint에서 Chat을 지원한다고 Responses도 지원하는 것은
아니다. AWS 문서는 GPT OSS의 Runtime Chat과 Runtime Responses 지원도 구분한다.
모델 이름이나 vendor substring으로 위 기능을 추정하지 않는다.

이 표는 endpoint 계약이며 실제 계정의 model access·IAM·region 가용성을 검증한
결과가 아니다. 근거는 AWS 사용자 가이드의
[Responses API](https://docs.aws.amazon.com/bedrock/latest/userguide/inference-responses-api.html)
페이지다. 최신 공식 문서의 제목·URL·snapshot hash는 operator-local source manifest
(`review/bedrock-parity-2026-10-12/source-manifest.json`)에 보관했다. `review/`는
`.gitignore` 대상이므로 이 증거는 저장소에 포함되지 않는다.

**3. 현재 소스의 차이와 새 재현**

| 차이 | 현재 소스/재현 | 판단 |
|---|---|---|
| Runtime IAM 네이티브 경로 | `bedrock` 기본 비-Claude 경로는 Converse. `bedrock_responses`는 Mantle `/openai[/v1]`만 허용 | native Runtime Responses/Chat의 IAM coverage를 별도로 추가해야 함 |
| Mantle IAM base path | 같은 정규식이 `/openai[/v1]`만 허용하지만 대부분의 Mantle model은 `/v1`에서 제공 | Runtime뿐 아니라 Mantle 대부분의 model도 IAM native 경로로 구성할 수 없음 |
| native 상태 참조 | bridge는 `previous_response_id`/`conversation`을 거부하지만 native forwarding은 `ValidateConversion`을 호출하지 않음. privacy rule이 없는 team은 공유 IAM principal로 그대로 전달 | 현재 존재하는 cross-user 참조 위험. PR-12를 기다리지 않고 즉시 guard 필요 |
| model-substring option 제거 | `mantleChatStripParams`가 `openai.gpt-5.6` substring으로 `temperature`/`top_p`/`stop`을 삭제하고 `max_tokens`를 `max_completion_tokens`로 rename | “native 옵션을 임의로 빼지 않는다”·“model 이름으로 기능을 추정하지 않는다” 원칙과 충돌 |
| AWS SDK의 native Converse | `/model/{id}/converse`, `/model/{id}/converse-stream` ingress가 없음 | 직접 SDK를 base URL만 바꿔 사용하는 것은 현재 지원 계약이 아님 |
| Mantle native Chat 요청 | `toMantleChatBody`가 OpenAI→canonical→OpenAI로 재생성 | 같은 wire인데도 의미 손실 발생 |
| Mantle native Chat 옵션 | `reasoning_effort`, `response_format`, `parallel_tool_calls`, `seed` 소실을 재현 | upstream에 전달해 지원 또는 검증 오류를 결정하게 해야 함 |
| developer role | native Chat의 developer message가 system message로 바뀌는 사례 재현 | cross-wire 변환 규칙을 native path에 적용한 차이 |
| 정상 incomplete | HTTP 200 / `status: incomplete` / 유효 usage의 native nonstream 응답이 502로 바뀜 | direct client의 정상 lifecycle과 다르고 fallback까지 유발할 수 있음 |
| 저장된 response lifecycle | gateway는 POST `/v1/responses`만 mount | retrieve/delete/cancel 등을 직접 SDK처럼 사용할 수 없음 |
| Mantle model 상속 | native observation parser가 항상 `model`을 요구 | endpoint가 허용하는 previous-response model 상속과 다름 |
| provider header | native transport가 incoming headers를 일반적으로 전달하지 않음 | project·guardrail·진단 header의 허용/강제 계약 필요 |
| bridge 출력 default | Responses→Anthropic envelope에 기본 4096 추가 | native 직접 사용의 출력/reasoning budget과 동일하지 않음 |
| client metadata | native model이 설치된 Codex bundled catalog에 없으면 picker에서 제외 | upstream 가용성과 client metadata 가용성이 별개인데 사용성에서 결합됨 |
| Chat usage 요청 | OpenAI-compatible native stream에도 `include_usage`를 추가하고 일부 frame을 숨김 | native wire 보존과 회계 증거 수집의 관계를 명시적으로 해결해야 함 |

관련 위치(main `8c12590` 기준):
[Mantle Chat 생성](../../../providers/bedrock/mantle.go) `toMantleChatBody`(L183),
[model-substring 제거 규칙](../../../providers/bedrock/mantle.go) `mantleChatStripParams`(L145),
[native incomplete 처리](../../../providers/openairesponses/openairesponses.go)(L172),
[native IAM endpoint 제한](../../../providers/bedrockresponses/bedrockresponses.go)(L27),
[bridge 상태 참조 거부](../../../internal/responses/request.go) `ValidateConversion`(L263),
[privacy coverage 표시](../../../internal/sensitivity/responses.go)(L20),
[gateway mount](../../../internal/server/server.go)(L147),
[bridge default](../../../internal/responses/anthropic.go)(L14),
[picker 제한](../../../scripts/inferplane-codex)(L157).

네이티브 경로가 전혀 없다는 뜻은 아니다. 일반 `openai_compatible` /
`openai_responses`와 서버가 보관하는 Bedrock API-key ref를 조합하면 지원되는
Runtime/Mantle base URL의 일부 native 호출은 가능하다. 하지만 IAM 전용 연결,
강제 guardrail, resource ownership, 전체 lifecycle을 갖춘 Bedrock native
profile과 동일하게 취급할 수는 없다. guardrail이 필요한 요청을 generic provider로
우회하여 지원하는 것처럼 표시하지 않는다.

새 overlay 재현은 옵션 네 개, developer role, nonstream incomplete의 여섯
사례에서 동등성 불변조건이 실패했다. 세 test function과 여섯 case로 구성된다.
재현 로그(`review/bedrock-parity-2026-10-12/parity-reproductions.log`)는
operator-local이며 저장소에 포함되지 않는다.
AWS가 예시 옵션을 특정 모델에서 지원한다는 실측은 아니다. 지원하지 않는 옵션도
원래 요청대로 AWS에 도달해야 원래의 validation error를 받을 수 있다는
preservation 검증이다. live/유료 모델을 호출하지 않았다.

**4. 네이티브 경로를 우선하는 구조**

요청한 wire와 upstream wire가 같으면 원문을 전달한다. canonical representation은
관측용으로 사용하고, 변환이 필요한 portable bridge에서만 송신 body를 생성한다.
입력 검사·권한·region·budget·guardrail 판정은 원문을 기준으로 별도로 유지한다.

native 요청에서는 reasoning·structured output·tool strictness·parallel option·
developer role·cache control·새 native field를 임의로 빼지 않는다.
지원하지 않는 옵션은 해당 endpoint의 원래 오류로 돌아오거나, 운영상 지원
범위 밖이라면 dispatch 전에 명시적인 gateway 오류로 거부한다.

기존 `mantleChatStripParams`와 `max_tokens` rename도 이 원칙으로 정리한다.
substring 규칙을 단순 삭제하면 현재 `openai.gpt-5.6` 사용자가 AWS validation
error를 새로 받게 되는 동작 변경이다. PR-11은 이 규칙을 route에 선언된
capability/요청 변환으로 옮기고, 변환이 발생한 요청은 native 동등성 결과와
분리하여 기록한다. native wire에서 선언 없이 옵션을 삭제하거나 이름을 바꾸지 않는다.

route description은 provider 이름 하나가 아니라 endpoint 종류, native protocol,
base path, model/profile, 실제 지원 기능을 포함한다. common 계약은 core PR에서
추가하고, 후속 native provider PR은 provider 확장 경계를 지킨다.
기존 `providerWire(name)`처럼 provider package 이름으로 응답 wire를 추정하는
경로도 이 계약으로 정리한다.

endpoint/SigV4 검증은 정확한 host·path·region·partition에 묶는다. 현재 Mantle
정규식을 넓혀 임의 host를 허용하는 방식으로 Runtime을 지원하지 않는다.
redirect·client credential 전달·broker→default-chain fallback 금지는 유지한다.

Runtime native guardrail은 지원되는 header를 통해 적용한다. provider/team의
필수 설정과 client header가 충돌하면 강제 설정을 유지하거나 거부한다.
Mantle로 fallback하여 필수 guardrail을 생략하지 않는다.
project/workspace header도 사용자 입력 그대로 authority로 사용하지 않고
허용된 namespace와 resource ownership을 확인한다. Runtime은 default project만
지원하므로 Runtime route에서는 non-default project 지정을 dispatch 전에 거부한다.
ownership 확인은 Mantle route에 적용한다.

Mantle `/v1`과 `/openai/v1`은 model별로 정확히 하나가 맞다. route 설정이 base path를
명시적으로 선언하고, 정규식은 두 path만 허용하되 host·region·partition 제약은 유지한다.

**5. 완료 상태와 회계 상태의 정합성을 native에도 적용한다**

native의 유효한 HTTP-200 `incomplete`는 같은 lifecycle로 전달한다.
`max_output_tokens` 도달을 gateway 502로 바꾸거나 새 model을 호출하지 않는다.
stream과 nonstream은 같은 의미를 가져야 한다.

표현 가능한 native application response와 알려진 usage를 먼저 분리한다.
native output의 새로운 item을 bridge canonical decoder가 이해하지 못한다는
이유만으로 잘라내거나 전체 response를 다른 성공으로 재작성하지 않는다.
malformed terminal/usage나 unsafe provider error와의 구분은 유지한다.

Chat streaming에서는 upstream usage 수집과 native 요청 보존이 충돌할 수 있다.
첫 qualification profile은 client가 직접 `include_usage`를 요청하도록 구성하고
동일한 client 설정을 direct와 gateway 양쪽에 적용한다. gateway가 client의
옵션을 몰래 변경하는 것을 무차이 증거로 삼지 않는다.
usage를 관측하지 못한 호출은 unknown으로 남기며 0 비용/환불로 만들지 않는다.

native cache 수량과 cache TTL은 독립적으로 보존한다. 모든 API를 TTL-split
형태로 가정하여 정상 cached 세션의 credit을 계속 과도하게 유지하는 문제는
실측해야 한다. TTL가 없어도 유일한 cache 가격이 입증되는 API에 대한 cost-evidence
확장은 별도 ADR·immutable pricing contract·adversarial 검증 대상이다.
단순히 두 단가를 같게 설정하거나 TTL을 만들어 authority의 기존 검증을 우회하지 않는다.
과거 unknown liability를 소급 환불하지 않는다.

**6. 상태 API와 background는 단순 passthrough만으로 열 수 없다**

AWS의 stored Responses는 create뿐 아니라 retrieve/delete와 이전 response 참조를
포함하고, Mantle background에는 cancel이 추가된다. Runtime은 항상 synchronous이므로
cancel·background 계약은 Mantle route에만 적용한다.
응답 ID를 알고 있다는 것만으로 다른 사용자의 대화를 사용할 수 있어서는 안 된다.

이 위험은 미래의 lifecycle API만의 문제가 아니다. 현재 native forwarding은
`ValidateConversion`을 거치지 않으므로, privacy rule이 없는 team의 native route에서
`previous_response_id`/`conversation`이 공유 IAM principal로 그대로 전달된다.
privacy rule이 있는 team은 coverage가 incomplete로 표시되어 fail closed하지만,
그것은 ownership 검사가 아니다.

PR-10a 즉시 guard(PR-12 이전, #110에서 구현):

- 모든 Responses 대상에서 `previous_response_id`, `conversation`, `item_reference`
  입력 항목을 라우팅 전에 400 `stored_reference_unsupported`로 거부한다.
  bridge는 이미 이 필드를 거부하므로 대상과 무관하게 같은 결과가 된다.
  오류는 제출된 ID를 되돌려 보내지 않는다.
- `store`와 `background`는 거부하지 않는다. retrieve/delete/cancel endpoint가
  mount되어 있지 않고 위의 참조가 막혀 있으므로, 저장된 response에 gateway를
  통해 다른 사용자가 접근할 경로가 없다. 기본값이 `store: true`인 일반 client의
  동작을 깨지 않기 위한 결정이다.
- route별 opt-in은 두지 않는다. 설정 field는 config·providerstore·admin
  read/write/export 전체를 round-trip해야 하며, 안전한 기본값은 거부다.
  PR-12의 ownership binding이 검증된 profile만 이 거부를 해제한다.

추가해야 할 계약:

- 최초 create의 authenticated principal, provider/project, model/profile,
  serving region과 resource를 결합한다. opaque resource 참조의 검증 정보는
  digest로 관리하며 원문 대화나 raw session ID를 저장하지 않는다.
- verified person/service identity로 ownership을 결합한다. legacy profile은
  명시적인 key scope처럼 증명할 수 있는 범위로 제한하며 JSON user, email,
  검증되지 않은 owner/accounting 문자열에서 공유 소유권을 추정하지 않는다.
- 새로운 create의 previous-response 참조부터 ownership을 확인한다.
  GET/DELETE/cancel만 보호하는 것으로 끝내지 않는다.
- 동일 사용자 key rotation, key revoke, 다른 사용자/팀, 여러 node와 restart를
  검증한다. local binding이 없으면 shared IAM role로 임의 resource를 조회해서
  신뢰를 복원하지 않는다.
- Runtime의 model-required 규칙과 Mantle의 model-inheritance 규칙을 각각 보존한다.
  누락된 model을 모든 endpoint에 임의로 채우지 않는다.
- stored resource의 region과 endpoint를 보존한다. global profile의 source
  region을 처리/저장 residency로 간주하지 않는다.
- provider에 보관된 history/opaque reasoning을 검사하지 못하는 privacy 규칙은
  현 계약대로 fail closed한다. native라는 이유로 coverage를 clean으로 바꾸지 않는다.

background는 응답 반환 후에도 생성과 과금이 이어질 수 있다. pending job의
ownership, durable reservation, 최종 usage 수집, cancel 결과, late settlement,
restart와 hard deadline 계약을 먼저 설계한다. create의 queued response가 usage를
아직 포함하지 않는다는 이유로 실패 retry하여 job을 여러 개 만들지 않는다.
GET/delete/cancel을 새 inference generation으로 reserve/charge하지 않는다.
취소 ACK나 resource 삭제를 미사용 credit 증거로 취급하지 않는다.

hosted tools/search의 별도 과금과 여러 내부 model step도 가격/상한 coverage에
포함하거나 명확히 제한한다. token 비용만 계산하면서 전체 hard spend를 보장하지 않는다.
Runtime 자체가 제공하지 않는 server tools/background를 Mantle로 몰래 전환하지 않는다.

이 작업은 기존 node-local profile에 숨은 shared DB 의존성을 추가하는 방식으로
구현하지 않는다. multi-node state ownership과 재시작 복구가 필요한 경우에는
그 기능의 별도 durable profile과 가용성 경계를 명시한다.
기존 SQLite/선택된 shared store의 확장을 검토하며 새 중앙 저장소나 state service
구축을 이번 계획의 전제로 두지 않는다.

**7. context와 client 사용성**

사용자가 명시적으로 선택한 native target에서 coarse estimate가 window를 넘는다는
사실만으로 실제 overflow가 증명되지는 않는다. native preflight의 hard refusal은
신뢰 가능한 tokenizer/측정/검증된 근거와 분리하여 검토한다.
heuristic를 경고·client headroom에 활용하는 것과 재무/자동 routing의 conservative
admission bound를 약화시키는 것은 별개의 작업이다. 후자는 변경하지 않는다.

model의 출력 default, reasoning budget, 실제 native usage를 그대로 client에
보여 준다. bridge의 4096 default나 합성 usage를 native context/모델 품질 문제로
오해하지 않도록 native/bridge와 실패 원인을 설명한다.

기존 SDK 사용의 첫 목표는 base URL과 credential 변경이며, 모델별로 복잡한
사용자 보정 설정을 강요하지 않는 것이다. gateway의 `/models`는 설정·권한·
endpoint 기능에 맞는 catalog를 제공한다. Runtime에 원래 없는 Models API를
무조건 upstream 조회하려 하지 않는다.

Codex bundled template에 없는 native model은 검증된 명시적 client profile로
등록할 수 있는 설계를 검토한다. reasoning/media/hosted-tool capability를
모델 이름에서 추정하거나 다른 모델의 base instructions를 무조건 복사하지 않는다.
기존 client/version과 원래 direct profile을 기준으로 qualification한다.
불가능한 세션 전환은 호환 target 또는 새 portable 세션을 선택하도록 구체적으로
설명하되, opaque history를 gateway가 변조하지 않는다.

**8. 추가 구현 작업과 출시 gate**

| 작업 | 내용 | 첫 목표 |
|---|---|---|
| PR-10a Native 상태 참조 guard | 모든 Responses 대상에서 `previous_response_id`/`conversation`/`item_reference` 라우팅 전 거부(#110) | 현재의 cross-user 참조 위험 차단 |
| PR-11 Native endpoint 계약 | Runtime/Mantle, Chat/Responses, IAM/API-key, 모델별 base path(Mantle `/v1` 포함)와 native wire 보존, substring option 규칙의 capability 이전 | sync native coding profile |
| PR-12 Native state lifecycle | resource ownership, inherited model, retrieve/delete/cancel, background의 durable 회계 | 검증한 state profile에 한해 확대 |
| PR-13 Direct/proxy 비교 suite | 같은 request builder와 upstream fixture로 SDK·기능·wire·usage·지연 비교 | 사용자 관점의 qualification 증거 |

PR-10a는 다른 작업과 독립적으로 가장 먼저 반영한다. PR-01/02의 종료·retry 수정 후
PR-11을 PR-06/07의 context/변환 작업보다 먼저 착수한다. PR-13은 PR-11과 함께
fixture부터 만든다. PR-12는 ownership·재무·privacy 계약이 검증되기 전까지
capability를 광고하지 않는다.

PR-11/12는 새 protocol profile, provider 확장 경계, ownership state를 도입하므로
착수 전에 별도 ADR로 결정을 기록할 것을 제안한다. 이 계획은 ADR을 대신하지 않는다.

필수 비교:

- [ ] privacy rule 유무와 무관하게 `previous_response_id`/`conversation`/
  `item_reference` 참조가 dispatch 전에 거부됨(PR-10a).
- [ ] Runtime/Mantle 각각 Chat/Responses 및 실제 지원 base path(Mantle `/v1`·`/openai/v1`).
- [ ] Runtime의 non-default project 지정과 cancel/background 요청이 명시적으로 거부됨.
- [ ] 선언된 capability 변환 외에는 native option이 삭제·rename되지 않음.
- [ ] 동일 model/profile·region·credential scope·요청 옵션의 direct/proxy 비교.
- [ ] native body는 승인된 alias/정책 변화를 제외하고 보존; unknown native field,
  developer role, reasoning, response format, tool strict/parallel option을 검증.
- [ ] completed/incomplete/refusal, parallel/custom tool, opaque native item,
  CRLF/keepalive/SSE terminal, cancellation과 nonstream 결과의 의미가 일치.
- [ ] 같은 오류 status와 가능한 bounded recovery code/Retry-After/request ID 보존.
  unsafe 오류 원문과 자격 증명은 전달하지 않음.
- [ ] client가 요구한 usage frame과 cache controls가 유지되고 내부 normalization이
  native client의 token/context 표현을 바꾸지 않음.
- [ ] context 경계·이미지·긴 tool history·출력/추론 한도에서 gateway만의 오거부를 분류.
- [ ] key rotation·revoke·cross-user/project·wrong region·restart·cross-node·
  unknown state·background duplicate-create·cancel/late usage·TTL uncertainty 검증.
- [ ] 단일 target과 명시적 fallback을 나누고 실제 모든 attempt 비용을 계산.
- [ ] 설치된 coding client의 tool 실행·model switch·compaction·최종 완료까지 비교.
- [ ] 신기능이 policy·authority·leaf/provider 경계를 우회하지 않음.

offline에서는 한 upstream fixture를 양쪽에 공급하여 body/semantic 차이를
정확히 판정한다. live에서는 확률적인 모델 출력 문장이 같아야 한다고 요구하지
않고 작업 성공·도구/형식 준수·usage·cache·모든 retry 비용과 추가 지연을 비교한다.
warm/cold cache와 계정/endpoint quota 차이도 나눈다. 추가 latency의 허용값은
배포 환경별로 실험 전에 정하며 0 latency나 미측정 절감률을 보장하지 않는다.
유료 paired probe는 별도 승인된 synthetic budget 캠페인에서 수행한다.

**9. 이번 검토의 범위**

AWS 공식 API 문서와 현재 코드를 대조하고 여섯 사례를 offline overlay로 재현했다.
소스 기능·운영 설정·AWS IAM/모델 호출은 변경하지 않았다.
native API 확장과 정책에 따른 차이를 분리한 위 내용을 기존 개선안의 목표와
완료 조건에 추가한다. “기존 10개 PR만 완료하면 Bedrock 전체와 무차이”라는
결론으로 읽으면 안 된다.
