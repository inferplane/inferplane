---
translation_source: codex-launcher.md
translation_source_sha256: 5f54a1dbe32fdf307b5a4266409bda74ad5c40cf83bc6d9ca72159360adb2927
---

# 모델별 Codex 실행 도구 {#model-aware-codex-launcher}

`scripts/inferplane-codex`는 로컬 게이트웨이를 위한 Python 3.9+ 표준 라이브러리 도구입니다. 상속된 `INFERPLANE_API_KEY`로 `GET http://127.0.0.1:8081/v1/models`를 인증합니다. mayu.env를 읽거나 업스트림 자격 증명을 가져오거나 공급자 직접 연결로 대체하지 않습니다.

**연동 검증:** Codex 0.154.0과 루프백 가짜 서버로 카탈로그, 모델별 컨텍스트, 인수 우선순위, 브리지의 중립 추론 강도, 네이티브 선호, 요청 라우팅을 확인했습니다. 배포에는 게이트웨이의 `reasoning.effort="none"` 처리와 아래 메타데이터 계약이 필요합니다. 실제 백엔드 지원을 증명하는 시험은 아닙니다.

## 실행 {#launching}

게이트웨이 키를 비공개로 내보낸 뒤 사용합니다.

```bash
scripts/inferplane-codex
scripts/inferplane-codex -m global.xai.grok-4.6
scripts/inferplane-codex -m openai.gpt-6-astra  # explicit native Responses session
printf '%s\n' 'Explain the current diff' | scripts/inferplane-codex exec -
scripts/inferplane-codex exec --sandbox read-only -- 'A prompt beginning with --'
```

키에 실제 반환되는 모델 ID를 사용하세요. 기본 `global.openai.gpt-6-astra`는 Converse 브리지의 이식 가능한 Astra입니다. 중립 추론 강도와 무상태 대화로 시작해 브리지 모델끼리 전환하기 좋습니다. `openai.gpt-6-astra`는 네이티브 추론 선호·기능을 유지하는 명시적 Responses 선택입니다.

이식 가능한 기본 모델이 없으면 네이티브 Astra로 몰래 바꾸지 않고 중단합니다. 다른 접근 가능한 ID를 직접 선택하세요. `-m`, `--model`, 붙여 쓰는·등호 형식, `-c model=...`을 인증된 목록과 확인합니다. 명시적 모델 플래그가 `-c model`보다 우선합니다. 실행 도구 기본값은 사용자·프로파일의 모델 설정보다 우선해 직접 Codex의 기본 모델과 독립적으로 동작합니다. `-c model`은 평문 ID, JSON 호환 큰따옴표, 작은따옴표 리터럴을 받으며 전체 TOML 문법·끝 인라인 주석은 해석하지 않습니다. 명확한 선택에는 `-m MODEL`을 사용하세요.

호출자의 인수와 stdin을 보존합니다. 공급자·카탈로그 설정은 **하위 명령과 호출자 옵션 뒤**, 첫 `--` 바로 앞에 추가합니다. 하위 명령의 `-c` 목록이 루트 설정을 덮는 동작을 피하기 위함입니다. 승인·샌드박스·프로파일 설정은 덮지 않습니다. 최초 브리지 모델에는 호출자가 비중립 값을 명시했더라도 뒤에 `model_reasoning_effort="none"`을 추가합니다. 네이티브 최초 모델은 사용자 추론 선호를 유지합니다. `--oss`, `--local-provider`, `--remote` 같은 직접 공급자 전환은 거부합니다.

inferplane 공급자는 로컬 `/v1`, Responses wire API, `env_key="INFERPLANE_API_KEY"`, `requires_openai_auth=false`, `supports_websockets=false`로 설정하고 호스팅 웹 검색을 끕니다. 키 누락, 접근·파싱 불가 메타데이터, 알 수 없거나 Responses·도구 미지원 또는 매핑되지 않은 네이티브 선택은 시작을 거부합니다. 잘못된 네이티브 바인딩·설치 카탈로그 읽기 실패도 중단합니다. 바인딩 자체는 유효하지만 설치 카탈로그에 slug가 없으면 경고 후 그 항목만 건너뜁니다.

검색 요청은 프록시를 무시하고 리다이렉트를 거부하며 제한 시간 5초·응답 4 MiB를 적용합니다. 오류에 본문을 넣지 않습니다. Codex 자식 프로세스에는 기존 `NO_PROXY`·`no_proxy`를 합치고 양쪽에 `127.0.0.1`, `localhost`, `::1`을 추가합니다. 자식 환경 복사본만 수정하므로 부모 환경·HTTP/HTTPS/ALL 프록시는 유지합니다. 다른 목적지는 기존 프록시·제외 설정을 따릅니다.

## 모델 카탈로그와 컨텍스트 {#model-catalog-and-context}

권한 0700 디렉터리에 0600 `model_catalog_json` 임시 파일을 만듭니다. Codex 실행 동안 유지하고 정상 종료·시작 실패·종료 신호에 정리합니다. SIGHUP·SIGTERM을 전달합니다. Ctrl-C는 같은 전경 프로세스 그룹으로 전달되므로 두 번 보내지 않습니다. 프로그램에서 래퍼를 종료할 때는 SIGTERM을 쓰세요. SIGKILL은 정리할 수 없습니다. 카탈로그에는 모델 메타데이터·지침만 있고 키는 없습니다.

적격 게이트웨이 ID가 `/model` 선택기에 표시됩니다. `context_window`·`max_context_window`는 선언된 context_window, 없으면 max_model_len, 둘 다 있으면 작은 값을 사용합니다. Codex 0.154.0은 다른 모델 해석 시에도 상속된 전역 model_context_window를 항목의 max_context_window로 제한합니다. 실행 도구는 전역 컨텍스트를 덮어쓰지 않습니다.

명시적으로 선택한 이식 가능한 Responses 모델의 게이트웨이 승인은 `max(1, request_bytes / 4)`와 출력 예산을 사용합니다. 백엔드 토크나이저의 정확한 값이 아닙니다. 민감 정보 검사기의 더 큰 바이트 상한은 재귀 디코딩 JSON까지 고려하므로 실제 입력 토큰과 혼동하지 마세요. 자동 대안·엄격 예산·InternalOnly는 보수적 용량 검사를 유지하며 검사 범위·기능·가격·예산 예약도 적용합니다.

격리된 Codex 0.154.0 앱 서버에 전역 `model_context_window=1048576`을 설정해 검증했습니다. config/read에는 전역 값이 남지만 런타임 thread/tokenUsage/updated의 **Grok 사용 가능 토큰은 419,430**입니다. 카탈로그 524,288과 이식 가능한 항목의 80%를 적용한 값입니다. 전역 값이 모델별 상한을 이기지 않습니다. `codex debug models`의 원시 메타데이터만으로 런타임 한도를 증명할 수 없습니다.

다른 시험은 이식 가능한 Astra로 시작해 같은 스레드에서 Grok으로 바꿉니다. Astra 시험 선언 1,000,000에서 사용 가능 값이 800,000→419,430으로 바뀌며 전역 설정은 그대로입니다. 두 번째 요청은 불투명 추론 항목이나 previous_response_id 없이 첫 사용자·응답 턴을 재생합니다. 가짜 공급자 시험이며 모든 백엔드·기존 네이티브 이력에 대한 보장은 아닙니다.

실행 도구의 상한 적용을 위해 사용자 설정을 지울 필요는 없습니다. 별도 직접 Codex 정리와 관련해서 설치된 0.154.0의 Astra 기본 메타데이터는 context_window=272000, max_context_window=872000, 사용 가능 95%입니다. 게이트웨이 별칭은 이 값 대신 자체 선언을 사용합니다. 과거 전역 설정을 지우는 일은 별도 연동 작업이며 이 도구가 ~/.codex/config.toml을 수정하지 않습니다.

한도가 둘 다 없으면 경고하고 **16,384 토큰**을 사용합니다. 실제 용량 보장이 아니라 보수적 운영 대체값이며 더 작은 모델에는 정확한 메타데이터가 필요합니다. 이식 가능한 항목은 20% 여유와 80% 압축 기준을, 네이티브는 내장 여유와 80% 압축 기준을 사용합니다. Codex가 더 제한하거나 호출자의 압축 설정을 적용할 수 있습니다. 잘못된 상한·중복·형식은 거부합니다.

카탈로그는 시작 시 스냅샷이므로 새 모델·메타데이터에는 재실행이 필요합니다. 요청 시 권한·라우팅은 게이트웨이가 책임집니다. 선택기에 보인다는 사실이 Responses·도구 호환 증명은 아닙니다. 네이티브→브리지 전환 후 불투명 이력이 거부되면 새 세션을 시작하세요.

## Responses·도구 기능 필터링 {#responses-and-tool-capability-filtering}

`responses_mode="unsupported"`는 tools가 있어도 해당 항목만 제외합니다. 공급자 체인이 네이티브·정규 브리지 어느 것도 처리하지 못한다는 뜻입니다. 기능 처리·컨텍스트 대체·네이티브 템플릿 조회 전에 제외합니다.

나머지는 명시적 capabilities 메타데이터로 도구 적격성을 판단합니다.

| 메타데이터 | Codex 적격성 |
| --- | --- |
| 필드 없음 | 구형 서버 호환을 위해 포함하지만 도구 지원을 증명하지 않음 |
| tools가 있는 문자열 배열 | 다른 검사도 통과하면 포함 |
| tools 없는 문자열 배열, 빈 배열 | 카탈로그·명시적 -m 선택에서 제외 |
| null, 배열 아님, 문자열 아닌 원소 | 잘못된 메타데이터로 시작 거부 |

기본 모델이 제외되어도 다른 모델을 자동 선택하지 않습니다. 제외는 템플릿·컨텍스트 처리 전이며 실행 도구에만 적용합니다. 게이트웨이 조회에는 계속 나타날 수 있고 텍스트 API 접근도 권한·백엔드 가용성을 따릅니다. 텍스트 전용·계정 차단 경로에서 tools 선언을 빼면 도구 코드를 바꾸지 않아도 됩니다. 이름·브랜드로 판단하거나 프롬프트로 도구를 흉내 내지 않습니다. 다른 capability 문자열이 추가 Codex 기능을 켜지 않으며 Responses 모드와 신뢰된 네이티브 메타데이터가 프로파일을 정합니다.

## 필요한 게이트웨이 메타데이터 {#metadata-needed-from-the-gateway}

이름·브랜드 추측이 아닌 선언된 경로 기능에서 `/v1/models` 필드를 만드세요.

| 필드 | 값 | 의미 |
| --- | --- | --- |
| `responses_mode` | native·bridge·unsupported | 네이티브, 무상태 브리지, 둘 다 지원하지 않는 체인. 네이티브 선언은 모든 적격 경로·폴백에서 유효해야 함 |
| `codex_model` | 공개 모델에서 유도한 정확한 내장 slug(예: gpt-6-astra) | 적격 네이티브 경로 필수. 브리지·미지원이면 생략. 비공개 업스트림 배포 ID 노출 금지 |
| `capabilities` | 검증한 기능 문자열 배열 | 선언·지원한 tools만 포함. 빈 배열은 제외, 누락은 구형 호환. null 금지 |
| `context_window`·`max_model_len` | 양수 정수 | 실제 설정된 모델 컨텍스트 한도 |

예시:

```json
{
  "object": "list",
  "data": [
    {
      "id": "global.openai.gpt-6-astra",
      "context_window": 1000000,
      "responses_mode": "bridge",
      "capabilities": ["tools"]
    },
    {
      "id": "openai.gpt-6-astra",
      "context_window": 1000000,
      "responses_mode": "native",
      "codex_model": "gpt-6-astra",
      "capabilities": ["tools", "vision", "reasoning"]
    },
    {
      "id": "bedrock.grok",
      "context_window": 524288,
      "responses_mode": "bridge",
      "capabilities": ["tools"]
    },
    {
      "id": "other.model",
      "responses_mode": "unsupported",
      "capabilities": ["tools"]
    }
  ]
}
```

responses_mode가 없으면 호환성을 위해 중립 추론을 포함한 이식 가능 메타데이터를 사용합니다. 네이티브 선호를 유지하려면 모드를 선언해야 합니다. 브리지의 none 처리 요구는 사라지지 않습니다. openai.* 등 이름으로 네이티브를 추측하지 않습니다. 잘못되거나 모순된 기능은 거부합니다. 네이티브 템플릿은 새로고침·사용자 설정 읽기를 건너뛰는 `codex debug models --bundled`에서 얻고 slug·컨텍스트를 게이트웨이 값으로 바꿉니다. 설치 템플릿과 일치하는 네이티브만 포함하며 유효하지만 매칭이 없으면 경고 후 제외합니다.

codex_model은 **공개 이름**의 openai. 또는 openai/ 네임스페이스를 제거해 유도하며 비공개 업스트림 ID에서 만들지 않습니다. 예제 네이티브 openai.gpt-6-astra는 정확한 gpt-6-astra slug와 대응합니다.

정규화한 공개 이름이 내장 항목에 없는 네이티브 별칭은 선택할 수 없습니다. `skipping a native model: codex_model is absent from the installed Codex catalog` 경고와 함께 생략하며 유효한 브리지·매칭 네이티브는 계속 사용합니다. 제외된 별칭을 직접 고르면 selected model is unavailable 오류입니다. 다른 모델로 대체하거나 비공개 이름·추측 템플릿을 사용하지 않습니다.

이 예외는 매핑은 유효하나 설치 항목만 없는 경우에 한합니다. codex_model 누락·무효, 기능·컨텍스트 오류, 카탈로그 읽기·파싱 실패는 여전히 치명적입니다. 경고는 신뢰할 수 없는 모델·템플릿 값을 그대로 표시하지 않습니다.

이식 가능한 항목은 텍스트·직접 도구와 단일 none 추론만 표시합니다. 추론 요약, verbosity, Responses Lite, 실험적 컨텍스트 상태는 지원하지 않습니다. Astra 지침을 복제하거나 브랜드에서 기능을 추론하지 않습니다.

## Codex 0.154.0의 추론 강도 처리 {#codex-01540-reasoning-handling}

가짜 서버로 캡처한 요청의 결과입니다.

| 최초 경로 | 합성 사용자 설정 | 실제 reasoning.effort |
| --- | --- | --- |
| 브리지 | 덮어쓰기 없음 | none |
| 브리지 | 상속된 xhigh | none |
| 브리지 | 명시적 high | none |
| 네이티브 | 상속된 xhigh | xhigh |

빈 supported_reasoning_levels와 supports_reasoning_summary_parameter=false만으로 **상속된 effort가 해제되지 않습니다.** 요청 작성기는 설정값을 카탈로그 기본값보다 우선하며 일반 값을 지원 목록으로 필터링하지 않습니다. CLI에는 해당 값을 TOML-null로 지우는 방법이 없어 최초 브리지에 중립 센티널을 명시하고 모든 브리지 항목의 유일한 지원·기본 수준을 none으로 둡니다. 브리지는 이를 추론 기능 요구 없음으로 해석하되 비중립 effort·실제 불투명 이력은 계속 거부해야 합니다.

네이티브 최초 모델에는 덮어쓰지 않고 내장 추론 선택을 유지합니다. 브리지 시작 옵션은 영속 사용자 설정·승인·샌드박스를 수정하지 않습니다. 선택기는 같은 모델별 추론·컨텍스트를 제공하고 기존 세션 이력을 지우거나 변환하지 않습니다.

Codex는 일반 Responses에서도 include=[reasoning.encrypted_content]를 요청합니다. 카탈로그 플래그는 Responses Lite 활성화를 막을 수 있지만 include를 제거하거나 과거 불투명 이력을 지울 수 없습니다.

이는 필드 생략이 아니라 중립 effort입니다. 불투명 이력을 버리거나 이름·일반 reasoning 선언만으로 추가 기능을 추론하지 않습니다.

## 오프라인 검증 {#offline-verification}

```bash
# Standard library only; fake HTTP model list and recording CLI.
python3 -B -m unittest discover -s tests/launchers -p 'test_inferplane_codex.py' -v

# Optional real installed CLI; all requests still go to loopback fakes.
# Creates an isolated synthetic CODEX_HOME, with no real auth or user config.
INFERPLANE_TEST_CODEX="$(command -v codex)" \
  python3 -B -m unittest discover -s tests/launchers -p 'test_inferplane_codex.py' -v

# Included automatically in the existing harness.
bash tests/run-all.sh inferplane-codex
```

설치 클라이언트 시험은 브리지·네이티브 effort 구분, 전역 고정값 아래 실제 컨텍스트, 같은 스레드의 Astra→Grok 전환과 이력 재생을 확인합니다. 실제 백엔드 호환성의 증거는 아닙니다. 카탈로그 권한·수명, 컨텍스트 대체, 인증 실패, 기능 필터·구형 서버, 리다이렉트·프록시 격리, 설정 우선순위, --·stdin·네이티브 조회·종료 상태·신호도 확인합니다.

스키마·동작은 설치 CLI의 bundled 출력과 rust-v0.154.0의 openai_models.rs, reasoning_effort.rs, model_info.rs, client.rs, config_override.rs에서 확인했습니다. 공식 문서는 <https://developers.openai.com/codex/config-reference/>와 <https://developers.openai.com/codex/config-advanced/>입니다.

## 셸 통합 예시 {#suggested-shell-integration}

일치하는 게이트웨이 메타데이터와 중립 effort 처리를 배포한 뒤 검토한 스크립트를 ~/.local/bin/inferplane-codex에 설치합니다. 기존 icodex의 비공개 환경 로딩 부분은 유지하고 하드코딩된 Codex 설정·인수 부분을 다음 명령으로 바꿀 수 있습니다.

```bash
icodex() (
    if [[ ! -r "$HOME/inferplane-local/mayu.env" ]]; then
        printf '%s\n' 'icodex: cannot read ~/inferplane-local/mayu.env' >&2
        return 1
    fi
    . "$HOME/inferplane-local/mayu.env" || return
    if [[ -z "${INFERPLANE_VKEY:-}" ]]; then
        printf '%s\n' 'icodex: INFERPLANE_VKEY is not configured' >&2
        return 1
    fi
    export INFERPLANE_API_KEY="$INFERPLANE_VKEY"
    command "$HOME/.local/bin/inferplane-codex" "$@"
)
```

이 문서 작업 자체는 .bashrc 수정, 실행 도구 설치, mayu.env 접근을 수행하지 않습니다.
