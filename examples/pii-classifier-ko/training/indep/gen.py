"""Independent evaluation templates: other model families, unseen genres.

The training and test sets were written by one generator (Claude Sonnet) in 25
genres. This set is written by non-Anthropic models on Amazon Bedrock in genres
that never appear in training, with a prompt written separately from
datagen/gen_templates.py. The slot contract and label policy are unchanged so
labels stay exact: models write ⟦SLOT⟧ markers, never values.

Usage: python3 gen.py --out ../data/indep/templates.jsonl [--per-call 5]
"""

from __future__ import annotations

import argparse
import concurrent.futures as cf
import json
import os
import re
import sys
import threading
import time

import boto3
from botocore.config import Config

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "datagen"))
from gen_templates import ATTR, DIRECT, GENRES as TRAIN_GENRES, validate  # noqa: E402
from kr_values import NEGATIVE_SLOTS, SLOTS  # noqa: E402

# model id -> accepts temperature
MODELS = {
    "us.openai.gpt-6.1-sol": False,
    "deepseek.v3.2": True,
    "us.moonshotai.kimi-k3": False,
    "mistral.mistral-large-3-675b-instruct": True,
}

GENRES = {
    "real_estate": ("ko", "부동산 임대차 계약서 초안, 중개사 메모, 전입신고 안내"),
    "resume": ("ko", "입사지원서, 이력서, 채용 담당자의 면접 평가 메모"),
    "call_center_stt": ("ko", "콜센터 상담 녹취를 STT로 옮긴 대화록 (말 더듬, 끊긴 문장 포함)"),
    "civil_complaint": ("ko", "공공기관 민원 접수 내용과 담당 공무원 답변"),
    "school_record": ("ko", "학원이나 학교의 상담일지, 학부모 안내문, 출결 기록"),
    "delivery_app": ("ko", "배달앱 주문 내역, 라이더 전달 메모, 가게 리뷰와 사장님 답글"),
    "bank_consult": ("ko", "은행·카드사 상담 기록, 이상거래 조사 메모"),
    "issue_tracker": ("ko", "Jira/Linear 이슈 본문과 댓글 스레드 (버그 재현 절차 포함)"),
    "notebook_output": ("ko", "Jupyter 노트북 셀 코드와 출력 (DataFrame head, 오류 메시지)"),
    "ci_pipeline": ("ko", "Dockerfile, GitHub Actions 워크플로, CI 실행 로그"),
    "legal_memo": ("ko", "법률 상담 메모, 내용증명 초안, 합의서"),
    "community_post": ("ko", "중고거래나 지역 커뮤니티 게시글과 댓글 (구어체, 맞춤법 틀림)"),
    "incident_postmortem": ("en", "English incident postmortems and on-call handoff notes for a service with Korean users"),
    "crm_notes": ("en", "English CRM notes and sales emails about Korean customers"),
}
assert not set(GENRES) & set(TRAIN_GENRES), "independent genres must not overlap training genres"

KINDS = {
    "pos_direct": f"문서마다 특정인을 바로 식별하는 슬롯을 하나 이상 넣는다: {DIRECT}. ⟦NAME⟧과 {ATTR}, {NEGATIVE_SLOTS}도 써도 된다.",
    "pos_attr": f"문서마다 ⟦NAME⟧으로 특정인을 언급하고, 그 사람에 대한 속성 슬롯을 하나 이상 함께 쓴다: {ATTR}. {DIRECT}는 쓰지 않는다. {NEGATIVE_SLOTS}는 써도 된다.",
    "pos_cred": "문서마다 ⟦CREDENTIAL⟧을 실제 비밀값(비밀번호, API 키, 토큰)으로 하나 이상 쓴다.",
    "neg_name_only": f"사람은 ⟦NAME⟧으로만 언급하고 (작성자, 담당자, 인사말 등) 그 사람에 대한 다른 정보는 쓰지 않는다. {DIRECT + ATTR + ['CREDENTIAL']}는 쓰지 않는다. {NEGATIVE_SLOTS}는 써도 된다.",
    "neg_handling": f"개인정보 항목을 다루거나 설명하지만 실제 값은 없는 문서: 마스킹 로직, 스키마, 검증 규칙, 개인정보 처리방침, 주민번호 저장 방법 질문 등. 개인정보 슬롯은 쓰지 않는다. {NEGATIVE_SLOTS}는 써도 된다.",
    "pos_linked": "사람을 이름 없이 내부 식별자로만 가리킨다: ⟦EMPLOYEE_ID⟧나 ⟦PLATE⟧를 같은 사람의 ⟦DIAGNOSIS⟧나 ⟦SALARY⟧와 함께 쓴다 (사번 기준 병가 기록, 급여 명세 한 줄, 장애인 주차 허가 등). ⟦NAME⟧과 직접 식별 슬롯은 쓰지 않고 '해당 직원', '신청인', '차주', '김OO'처럼 부른다.",
    "neg_linked_partial": "식별자와 민감 정보 중 한쪽만 쓴다: ⟦EMPLOYEE_ID⟧나 ⟦PLATE⟧만 있고 건강·급여 정보는 없거나, ⟦DIAGNOSIS⟧나 ⟦SALARY⟧를 통계·정책·범위처럼 특정인과 무관하게 쓰고 식별자는 없다. 한 문서에 둘을 같이 쓰지 않는다. ⟦NAME⟧과 직접 식별 슬롯은 쓰지 않는다.",
    "neg_lookalike": f"개인정보처럼 보이지만 아닌 식별자가 많은 업무·시스템 문서. {NEGATIVE_SLOTS} 중 여러 개를 쓴다. 개인정보 슬롯과 사람 이름은 쓰지 않는다.",
}

SYSTEM = """당신은 개인정보 탐지기 평가용 합성 문서를 만든다.
구체적인 개인 값(이름, 전화번호, 이메일, 주소, 각종 번호, 계좌, 카드, 비밀값)은 절대 직접 쓰지 않는다.
값이 들어갈 자리에는 아래 목록의 슬롯 표기를 그대로 쓴다. 목록에 없는 슬롯을 만들지 않는다. 숫자로 된 번호가 필요하면 슬롯을 쓴다.
허용 슬롯: {slots}
현업에서 실제로 나올 법한 문서처럼 쓴다. 길이(30~250단어), 형식, 말투를 문서마다 다르게 한다.
출력은 문서 문자열들의 JSON 배열 하나뿐이다. 설명, 코드 펜스, 객체를 쓰지 않는다."""

SLOT_RE = re.compile(r"⟦([A-Z_]+)⟧")


def parse(text: str) -> list:
    text = text[text.find("["): text.rfind("]") + 1]
    docs = json.loads(text)
    return docs if isinstance(docs, list) else []


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", required=True)
    ap.add_argument("--per-call", type=int, default=5)
    ap.add_argument("--profile", default=None)
    ap.add_argument("--region", default="us-east-1")
    ap.add_argument("--workers", type=int, default=16)
    ap.add_argument("--kinds", help="comma-separated subset of KINDS")
    args = ap.parse_args()

    client = boto3.Session(profile_name=args.profile, region_name=args.region).client(
        "bedrock-runtime", config=Config(read_timeout=600, retries={"max_attempts": 8, "mode": "adaptive"}))
    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    done: set[str] = set()
    if os.path.exists(args.out):
        with open(args.out, encoding="utf-8") as f:
            done = {json.loads(l)["job"] for l in f}
    kinds = args.kinds.split(",") if args.kinds else list(KINDS)
    jobs = [(m, g, k) for m in MODELS for g in GENRES for k in kinds if f"{m}/{g}/{k}" not in done]
    print(f"{len(jobs)} jobs to run ({len(done)} done)", flush=True)
    lock = threading.Lock()
    allowed = ", ".join(f"⟦{s}⟧" for s in SLOTS)

    def run(job: tuple[str, str, str]) -> tuple[int, int]:
        model, genre, kind = job
        lang, desc = GENRES[genre]
        prompt = (f"장르: {desc}\n조건: {KINDS[kind]}\n"
                  f"서로 다른 문서 {args.per_call}개를 {'한국어로 (기술 용어는 영어 가능)' if lang == 'ko' else '영어로'} 써라. "
                  "회사, 상황, 형식을 문서마다 바꿔라.")
        cfg = {"maxTokens": 12000}
        if MODELS[model]:
            cfg["temperature"] = 0.9
        err: Exception | None = None
        docs: list = []
        for attempt in range(3):
            try:
                resp = client.converse(modelId=model, system=[{"text": SYSTEM.format(slots=allowed)}],
                                       messages=[{"role": "user", "content": [{"text": prompt}]}], inferenceConfig=cfg)
                docs = parse("".join(b.get("text", "") for b in resp["output"]["message"]["content"]).strip())
                break
            except Exception as e:  # noqa: BLE001 - retried, then reported
                err = e
                time.sleep(3 + attempt * 5)
        else:
            print(f"FAIL {model}/{genre}/{kind}: {type(err).__name__}: {str(err)[:200]}", flush=True)
            return 0, 0
        good = [d for d in docs if isinstance(d, str) and validate(kind, d)]
        with lock, open(args.out, "a", encoding="utf-8") as f:
            for j, d in enumerate(good):
                f.write(json.dumps({"job": f"{model}/{genre}/{kind}", "template_id": f"indep/{model}/{genre}/{kind}/{j}",
                                    "generator": model, "genre": genre, "kind": kind, "lang": lang, "text": d},
                                   ensure_ascii=False) + "\n")
            if not good:
                f.write(json.dumps({"job": f"{model}/{genre}/{kind}", "template_id": None, "generator": model,
                                    "genre": genre, "kind": kind, "lang": lang, "text": None}, ensure_ascii=False) + "\n")
        return len(good), len(docs)

    kept = total = 0
    with cf.ThreadPoolExecutor(args.workers) as ex:
        for n, (g, t) in enumerate(ex.map(run, jobs), 1):
            kept, total = kept + g, total + t
            if n % 20 == 0:
                print(f"{n}/{len(jobs)} jobs, kept {kept}/{total}", flush=True)
    print(f"done: kept {kept}/{total}", flush=True)


if __name__ == "__main__":
    main()
