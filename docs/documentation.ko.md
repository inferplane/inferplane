---
translation_source: documentation.md
translation_source_sha256: b57db677dd76bd3e26b4ea602e4715e9b75742ae8d55704fd1b13b791d258f45
---

# 문서 관리 {#maintain-the-documentation}

저장소 Markdown을 MkDocs Material로 빌드하고 GitHub Actions로 **https://inferplane.github.io/inferplane/**에 게시합니다. 현재 main을 설명하는 사이트이며 버전별 API 호환성 보장은 아닙니다.

## 미리보기와 검증 {#preview-and-validate}

```bash
python3 -m venv .venv-docs
.venv-docs/bin/python -m pip install -r requirements-docs.txt
.venv-docs/bin/python -m mkdocs serve
```

MkDocs가 출력하는 루프백 URL을 여세요. CI와 같은 검사는 다음과 같습니다.

```bash
.venv-docs/bin/python -m unittest discover -s tests/docs -v
.venv-docs/bin/python scripts/check_docs_translations.py
.venv-docs/bin/python -m mkdocs build --strict
.venv-docs/bin/python scripts/check_docs_site.py site
```

`requirements-docs.txt`에 의존성을 고정합니다. 의도적으로 갱신할 때는 `.in` 파일을 수정하고 `uv pip compile --generate-hashes`로 잠금 파일을 만든 뒤 차이를 검토합니다.

## 작성 원칙 {#editorial-contract}

준비 사항, 지원 설정, 예상 결과, 실패 동작, 다음 단계처럼 운영자의 작업을 기준으로 작성하세요. 알파 상태와 프로파일 한계를 명시하고 구현·자동 테스트·실제 모델 호환·운영 검증을 구분합니다.

새 동작은 레퍼런스·설계 결정에 연결합니다. 운영 절차는 자격 증명, 신원 이력, 감사 증거, 미해결 권한을 보존해야 합니다. 예제를 현재 모델 가용성·가격 보장으로 바꾸지 마세요.

탐색은 `mkdocs.yml`에서 관리합니다. 과거 사양·계획·고객 분석은 GitHub에만 남기고 사이트 검색에서 제외합니다. ADR은 원래 상태로 검색할 수 있습니다. 훅은 저장소 전용 링크를 GitHub로 바꿉니다. ADR 추가·이름 변경 후 `python scripts/docs_hooks.py`로 커밋되는 한영 색인을 재생성하세요. 검증은 오래된 색인을 거부합니다.

## 한국어와 영어 {#korean-and-english}

영어는 기존 루트, 한국어는 `/ko/`입니다. 언어 선택기는 현재 문서를 유지합니다. 공개 제품·운영·API·구현 레퍼런스·프로젝트 문서마다 `page.ko.md`를 함께 작성하세요. ADR은 한국어 색인과 명시적인 원문 안내를 제공하되 영어 원문을 유지합니다. 과거 계획·사양은 게시하지 않습니다.

명령, 필드명, 코드 예제, 안전 조건을 보존하세요. 한국어 제목에 `{#original-anchor}`로 원문 앵커를 부여해 양쪽 절 링크가 유지되게 합니다. 원문과 번역을 함께 갱신하고, 검토한 영어 파일의 SHA-256을 `translation_source_sha256`에 기록하며 `translation_source` 경로를 유지합니다. 검사는 누락·오래된 번역을 탐지하지만 해시 일치가 번역 품질을 증명하지는 않습니다. 표시만 갱신하지 말고 의미를 검토하세요. 검색·메타데이터·메뉴·모바일 언어 전환도 확인합니다.

## 게시 {#publication}

모든 PR에서 문서를 검증합니다. main 푸시 또는 main의 수동 실행에서 빌드가 성공해야 배포합니다. Pages 쓰기·OIDC 권한은 `github-pages` 환경의 배포 작업에만 있으며 동시 게시를 직렬화합니다.

저장소 Pages는 **GitHub Actions**로 설정해야 합니다. 첫 배포가 공개 사이트를 만들며 병합 전에 빌드를 검토합니다. 저장소의 필수 AI 리뷰·CI 조건을 따라 게시하세요.

워크플로 성공 후 공개 URL, 탐색, 검색, 모바일, 절 링크를 확인합니다. 문서 게시를 위해 리뷰 검사를 우회하지 마세요.
