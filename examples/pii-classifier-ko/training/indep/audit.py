"""Second-opinion labels for the independent set.

Slot labels are exact only if the generator kept every value inside a slot.
A judge model reads each filled row against the written label policy; rows
where the judge disagrees with the slot label are listed for manual review
(they are not relabelled automatically).

Usage: python3 audit.py --rows ../data/indep/rows.jsonl --out ../data/indep/audit.jsonl
"""

from __future__ import annotations

import argparse
import concurrent.futures as cf
import json
import os
import threading


POLICY = """Label the text 1 or 0 under this policy.
1 (contains personal information or a secret) when the text contains ANY of:
- a direct identifier value of a specific person: resident or foreigner registration number, phone number,
  email address of a person (work addresses included), postal/street address of a person, bank account number,
  card number, passport number, driver's license number;
- a specific person's name TOGETHER WITH an attribute of that person: birth date or age, diagnosis, health condition
  or body measurements (blood type, height, weight), religion, salary or personal finances, employee ID,
  vehicle plate number;
- a secret credential value: password, API key, access token, private key, connection string containing a password.
- an internal linking identifier of a person (employee ID, vehicle plate) TOGETHER WITH that person's health condition or
  salary, even when the name is absent or masked.
0 otherwise, including:
- names alone (authors, assignees, reviewers, greetings) with no other information about the person, including a name
  with only an employer, team, job title or school;
- code, schemas, validation rules or policy text that handle personal-data fields without containing real values;
- masked or placeholder values (010-****-1234, 김OO, 홍길동, user@example.com, ${API_KEY}, <redacted>);
- organisation identifiers and system values: business registration numbers, 1588/1577 switchboard numbers,
  order IDs, UUIDs, hashes, versions, timestamps, private IP addresses, base64 blobs that are not credentials.
Answer with JSON only: {"label": 0 or 1, "reason": "<one short sentence>"}"""


def record(r: dict, judge: int | None, reason: str | None) -> dict:
    """One audit line; KDPII rows carry no generator."""
    return {"id": r["id"], "label": r["label"], "judge": judge, "reason": reason,
            "kind": r["kind"], "source": r["source"], "generator": r.get("generator")}


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--rows", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--model", default="us.anthropic.claude-sonnet-5-5")
    ap.add_argument("--profile", default=None)
    ap.add_argument("--region", default="us-east-1")
    ap.add_argument("--workers", type=int, default=16)
    args = ap.parse_args()

    import boto3
    from botocore.config import Config

    client = boto3.Session(profile_name=args.profile, region_name=args.region).client(
        "bedrock-runtime", config=Config(read_timeout=300, retries={"max_attempts": 8, "mode": "adaptive"}))
    done = set()
    if os.path.exists(args.out):
        with open(args.out, encoding="utf-8") as f:
            done = {json.loads(l)["id"] for l in f}
    with open(args.rows, encoding="utf-8") as f:
        rows = [r for r in (json.loads(l) for l in f) if r["id"] not in done]
    lock = threading.Lock()

    def run(r: dict) -> None:
        judge, reason = None, None
        for _ in range(3):
            try:
                resp = client.converse(modelId=args.model, system=[{"text": POLICY}],
                                       messages=[{"role": "user", "content": [{"text": r["text"]}]}],
                                       inferenceConfig={"maxTokens": 400})
                text = "".join(b.get("text", "") for b in resp["output"]["message"]["content"])
                obj = json.loads(text[text.find("{"): text.rfind("}") + 1])
                judge, reason = int(obj["label"]), str(obj.get("reason", ""))
                break
            except Exception as e:  # noqa: BLE001 - retried, then recorded
                reason = f"error: {type(e).__name__}: {str(e)[:150]}"
        with lock, open(args.out, "a", encoding="utf-8") as f:
            f.write(json.dumps(record(r, judge, reason), ensure_ascii=False) + "\n")

    with cf.ThreadPoolExecutor(args.workers) as ex:
        list(ex.map(run, rows))
    with open(args.out, encoding="utf-8") as f:
        res = [json.loads(l) for l in f]
    dis = [r for r in res if r["judge"] is not None and r["judge"] != r["label"]]
    print(f"audited {len(res)}, errors {sum(r['judge'] is None for r in res)}, disagreements {len(dis)}")


if __name__ == "__main__":
    main()
