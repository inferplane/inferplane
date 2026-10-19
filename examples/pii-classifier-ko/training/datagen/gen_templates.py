"""Generate slot templates with Claude on Amazon Bedrock.

The model writes documents that contain slot markers such as ⟦PHONE⟧ and never
values; fill.py replaces markers with synthetic values locally, so labels are
exact and the model cannot leak or invent a real person's data.

Usage: python3 gen_templates.py --out data/templates.jsonl [--calls 4] [--per-call 6]
Credentials: the default AWS credential chain, or --profile.
"""

from __future__ import annotations

import argparse
import concurrent.futures as cf
import json
import os
import re
import threading
import time

from kr_values import NEGATIVE_SLOTS, POSITIVE_SLOTS, SLOTS

GENRES = {
    # genre: (lang, description)
    "python_code": ("ko", "Python source code with Korean comments or strings (scripts, Django/FastAPI handlers, pandas jobs, unit tests)"),
    "java_code": ("ko", "Java/Spring source code with Korean comments, log messages or test fixtures"),
    "go_code": ("ko", "Go source code with Korean comments, struct literals or test tables"),
    "typescript_code": ("ko", "TypeScript/React code with Korean UI strings, form handling or mock data"),
    "sql": ("ko", "SQL statements, migrations, query results or seed data for a Korean service"),
    "json_fixture": ("ko", "JSON API payloads, fixtures or config with Korean values"),
    "csv_table": ("ko", "CSV or markdown tables exported from a Korean business system"),
    "app_log": ("ko", "application or access logs from a Korean web service, mixed Korean and English"),
    "stack_trace": ("ko", "exception stack traces and error reports with request context"),
    "git_diff": ("ko", "git diffs, commit messages or PR descriptions in a Korean team"),
    "yaml_config": ("ko", "YAML/ENV/properties configuration files and Helm values"),
    "dev_chat": ("ko", "Korean developer chat messages to a coding assistant (Slack style), asking for help"),
    "customer_ticket": ("ko", "Korean customer support tickets and agent replies"),
    "hr_document": ("ko", "Korean HR documents: onboarding forms, payroll notes, evaluations"),
    "medical_record": ("ko", "Korean clinic or hospital notes, prescriptions, appointment records"),
    "shopping_order": ("ko", "Korean e-commerce order, delivery and refund records"),
    "email_message": ("ko", "Korean business emails"),
    "meeting_notes": ("ko", "Korean meeting notes and project status reports"),
    "api_request": ("ko", "curl commands, HTTP requests and responses against a Korean service"),
    "markdown_doc": ("ko", "Korean technical documentation, READMEs and runbooks"),
    "insurance_claim": ("ko", "Korean insurance claim forms and adjuster notes"),
    "terminal_output": ("ko", "terminal sessions: shell commands and their output, psql/kubectl/aws CLI"),
    "kakao_message": ("ko", "KakaoTalk-style personal messages exported as text"),
    "chat_turn": ("ko", "single turns from Korean messenger chats, customer chat and community comment threads, casual spoken style"),
    "form_input": ("ko", "values typed into Korean web forms, sign-up fields, delivery requests and one-line support requests"),
    "english_code": ("en", "source code with English comments and fixtures"),
    "english_chat": ("en", "English developer chat or support messages"),
}

DIRECT = [s for s in POSITIVE_SLOTS if SLOTS[s][2]]
ATTR = [s for s in POSITIVE_SLOTS if not SLOTS[s][2] and s != "NAME"]

KINDS = {
    "pos_direct": f"Each document must contain at least one direct-identifier slot from {DIRECT}. It may also use ⟦NAME⟧, attribute slots {ATTR} and lookalike slots {NEGATIVE_SLOTS}.",
    "pos_direct_anon": f"Each document must contain at least one direct-identifier slot from {DIRECT} and must NOT use ⟦NAME⟧. Refer to people only as 저, 제, 고객님, 담당자, 기사님, 친구 or not at all: a value on its own, a value the writer gives as their own, or a value in a form field. Lookalike slots {NEGATIVE_SLOTS} are allowed.",
    "pos_attr": f"Each document must mention a specific person with ⟦NAME⟧ together with at least one attribute slot from {ATTR}. Do NOT use any of {DIRECT}. Lookalike slots {NEGATIVE_SLOTS} are allowed.",
    "pos_cred": "Each document must contain at least one ⟦CREDENTIAL⟧ slot used as a real secret value (password, API key, token) in code, config, logs or chat. Lookalike slots are allowed.",
    "neg_name_only": f"Each document may mention people only by ⟦NAME⟧ (e.g. authors, reviewers, assignees, greetings) with NO other information about them, and must NOT use any of {DIRECT + ATTR + ['CREDENTIAL']}. Lookalike slots {NEGATIVE_SLOTS} are allowed.",
    "neg_handling": f"Each document handles, discusses or validates personal-data FIELDS without containing any actual value: e.g. masking functions, schemas, validation regexes, privacy-policy text, questions about how to store 주민등록번호 safely. Use NO positive slots. Lookalike slots {NEGATIVE_SLOTS} are allowed and encouraged.",
    "pos_linked": "Each document identifies a person only by an internal ID, never by name: use ⟦EMPLOYEE_ID⟧ or ⟦PLATE⟧ together with ⟦DIAGNOSIS⟧ or ⟦SALARY⟧ about that same person (e.g. a sick-leave note by employee number, a payroll line, a parking permit for a disabled driver). Do NOT use ⟦NAME⟧ or any direct identifier; refer to the person as 해당 직원, 신청자, 차주, 이OO and similar. Lookalike slots are allowed.",
    "neg_linked_partial": "Each document has ONLY ONE side of an ID-plus-sensitive-fact pair: either ⟦EMPLOYEE_ID⟧ or ⟦PLATE⟧ with no health or salary fact, or ⟦DIAGNOSIS⟧ or ⟦SALARY⟧ stated as an aggregate, policy, range or about nobody in particular (e.g. 부서 평균 연봉, 우울증 지원 프로그램 안내) with no ID. Never put an ID and a health/salary fact in the same document. Do NOT use ⟦NAME⟧ or any direct identifier. Lookalike slots are allowed.",
    "neg_lookalike": f"Each document is a business or system artifact with identifiers that look like personal data but are not: use several of {NEGATIVE_SLOTS} (order ids, UUIDs, hashes, business registration numbers, 1588 switchboard numbers, masked values, example.com emails, versions, timestamps, private IPs). Use NO positive slots and no person names.",
}

SYSTEM = """You write realistic synthetic documents for training a personal-data detector.
Never write any concrete personal value (no names, phone numbers, emails, addresses, ID numbers, account or card numbers, secrets).
Wherever a value would appear, write a slot marker from the allowed list instead, exactly like ⟦PHONE⟧ (U+27E6 and U+27E7 brackets).
Allowed slot markers: {slots}.
{length} Make documents look like real {lang} working material, not tutorials about slots.
Return ONLY a JSON array of strings, one string per document, with no commentary."""

LENGTHS = {
    "doc": "Vary length (40 to 300 words), structure, tone and domain.",
    "short": "Write short texts: one to three sentences, 5 to 40 words each, like a single chat turn, a form field or a one-line request. Vary tone and domain.",
}

SLOT_RE = re.compile(r"⟦([A-Z_]+)⟧")


def validate(kind: str, text: str) -> bool:
    slots = SLOT_RE.findall(text)
    if any(s not in SLOTS for s in slots):
        return False
    present = set(slots)
    if kind == "pos_direct":
        return bool(present & set(DIRECT))
    if kind == "pos_direct_anon":
        return "NAME" not in present and bool(present & set(DIRECT))
    if kind == "pos_attr":
        return "NAME" in present and bool(present & set(ATTR)) and not present & set(DIRECT)
    if kind == "pos_cred":
        return "CREDENTIAL" in present
    if kind == "pos_linked":
        return "NAME" not in present and not present & set(DIRECT) and bool(present & {"EMPLOYEE_ID", "PLATE"}) and bool(present & {"DIAGNOSIS", "SALARY"})
    if kind == "neg_linked_partial":
        ids, facts = present & {"EMPLOYEE_ID", "PLATE"}, present & {"DIAGNOSIS", "SALARY"}
        return "NAME" not in present and not present & set(DIRECT) and bool(ids or facts) and not (ids and facts)
    if kind == "neg_name_only":
        return not present & set(DIRECT + ATTR + ["CREDENTIAL"])
    return not present & set(POSITIVE_SLOTS)


def job_id(genre: str, kind: str, idx: int, length: str) -> str:
    base = f"{genre}/{kind}/{idx}"
    return base if length == "doc" else f"{base}/{length}"


def prompt_for(genre: str, kind: str, idx: int, per_call: int, length: str) -> tuple[str, str]:
    lang, desc = GENRES[genre]
    allowed = ", ".join(f"⟦{s}⟧" for s in SLOTS)
    system = SYSTEM.format(slots=allowed, lang="Korean" if lang == "ko" else "English", length=LENGTHS[length])
    user = (f"Genre: {desc}.\nRequirement: {KINDS[kind]}\n"
            f"Write {per_call} different documents in {'Korean (English technical terms are fine)' if lang == 'ko' else 'English'}. "
            f"Variation seed {idx}: pick a different company, product and situation for each document.")
    return system, user


def main() -> None:
    import boto3
    from botocore.config import Config

    ap = argparse.ArgumentParser()
    ap.add_argument("--out", required=True)
    ap.add_argument("--calls", type=int, default=4, help="calls per (genre, kind)")
    ap.add_argument("--per-call", type=int, default=6)
    ap.add_argument("--model", default="us.anthropic.claude-sonnet-5-5")
    ap.add_argument("--profile", default=None)
    ap.add_argument("--region", default="us-east-1")
    ap.add_argument("--workers", type=int, default=12)
    ap.add_argument("--kinds", help="comma-separated subset of KINDS")
    ap.add_argument("--genres", help="comma-separated subset of GENRES")
    ap.add_argument("--length", choices=sorted(LENGTHS), default="doc")
    args = ap.parse_args()

    session = boto3.Session(profile_name=args.profile, region_name=args.region)
    client = session.client("bedrock-runtime", config=Config(read_timeout=300, retries={"max_attempts": 8, "mode": "adaptive"}))

    done: set[str] = set()
    if os.path.exists(args.out):
        with open(args.out, encoding="utf-8") as f:
            for line in f:
                done.add(json.loads(line)["job"])
    lock = threading.Lock()
    kinds = args.kinds.split(",") if args.kinds else list(KINDS)
    genres = args.genres.split(",") if args.genres else list(GENRES)
    jobs = [(g, k, i) for g in genres for k in kinds for i in range(args.calls) if job_id(g, k, i, args.length) not in done]
    print(f"{len(jobs)} jobs to run ({len(done)} already done)", flush=True)

    def run(job: tuple[str, str, int]) -> tuple[int, int]:
        genre, kind, idx = job
        lang = GENRES[genre][0]
        jid = job_id(genre, kind, idx, args.length)
        system, prompt = prompt_for(genre, kind, idx, args.per_call, args.length)
        for attempt in range(3):
            try:
                resp = client.converse(modelId=args.model, system=[{"text": system}],
                                       messages=[{"role": "user", "content": [{"text": prompt}]}],
                                       inferenceConfig={"maxTokens": 8000, "temperature": 1.0})
                text = "".join(b.get("text", "") for b in resp["output"]["message"]["content"]).strip()
                text = text[text.find("["): text.rfind("]") + 1]
                docs = json.loads(text)
                break
            except Exception as e:  # noqa: BLE001 - retried, then reported
                err = e
                time.sleep(2 + attempt * 5)
        else:
            print(f"FAIL {genre}/{kind}/{idx}: {type(err).__name__}: {str(err)[:200]}", flush=True)
            return 0, 0
        good = [d for d in docs if isinstance(d, str) and validate(kind, d)]
        with lock, open(args.out, "a", encoding="utf-8") as f:
            for j, d in enumerate(good):
                f.write(json.dumps({"job": jid, "template_id": f"{jid}/{j}", "genre": genre, "kind": kind, "lang": lang,
                                    "length": args.length, "text": d}, ensure_ascii=False) + "\n")
            if not good:  # record the job so a resume does not loop on it
                f.write(json.dumps({"job": jid, "template_id": None, "genre": genre, "kind": kind, "lang": lang,
                                    "length": args.length, "text": None}, ensure_ascii=False) + "\n")
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
