"""Convert neutral rows to each backend's training format.

Usage: python3 convert.py --neutral data/neutral --out data/backends [--replay <strands-decider>/data/synthetic]

strands-decider: one `noul` Example per row (src/strands_decider/data/format.py).
Laya: {state, questions:{has_pii:{type:noul,...}}, expected:{has_pii:bool}} (docs/finetune.md).
"""

from __future__ import annotations

import argparse
import glob
import json
import os
import random

QUESTION_ID = "has_pii"
INSTRUCTIONS = "Does this text contain personal information about a specific person, or a secret credential?"
VARIANTS = [
    "Does the text include personal data that identifies or describes a particular individual, or a password, API key or token?",
    "Is there any personal information (ID numbers, phone, email, address, account or card numbers, a named person's health or salary) or a secret in this text?",
    "이 텍스트에 특정 개인의 개인정보나 비밀 자격 증명이 포함되어 있습니까?",
    "이 내용에 주민등록번호, 전화번호, 이메일, 주소, 계좌·카드번호, 특정인의 건강·급여 정보, 또는 비밀번호·API 키가 들어 있나요?",
]
OPTIONS = [["false", "no personal information about a specific person and no secret"],
           ["true", "contains personal information about a specific person or a secret credential"]]


def strands_row(r: dict) -> dict:
    return {"kind": "noul", "state": r["text"], "instructions": INSTRUCTIONS, "options": OPTIONS,
            "label": int(r["label"]), "task": f"ko_pii:{r['genre']}", "weight": 1.0, "instruction_variants": VARIANTS}


def laya_row(r: dict, variant: str | None = None) -> dict:
    return {"state": r["text"], "questions": {QUESTION_ID: {"type": "noul", "instructions": variant or INSTRUCTIONS}},
            "expected": {QUESTION_ID: bool(r["label"])}, "tags": {"genre": r["genre"], "kind": r["kind"], "source": r["source"]},
            "language": r["lang"]}


def read(path: str) -> list[dict]:
    with open(path, encoding="utf-8") as f:
        return [json.loads(l) for l in f]


def write(path: str, rows: list[dict]) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--neutral", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--replay", help="strands-decider data/synthetic directory (Apache-2.0 committed rows)")
    ap.add_argument("--replay-rows", type=int, default=4000)
    ap.add_argument("--seed", type=int, default=11)
    args = ap.parse_args()
    rng = random.Random(args.seed)

    splits = {sp: read(os.path.join(args.neutral, f"{sp}.jsonl")) for sp in ("train", "val", "test", "ood")}

    # strands-decider: train (+replay), and one held-out file per split; its
    # calibrate/eval commands carve calib/test halves from a single file, so
    # val is used for calibration and test/ood are evaluated separately.
    sd = [strands_row(r) for r in splits["train"]]
    if args.replay:
        replay = []
        for p in sorted(glob.glob(os.path.join(args.replay, "*.jsonl"))):
            if p.endswith("_eval.jsonl"):
                continue
            replay += [r for r in read(p) if "kind" in r]  # teacher/replay prob files have another schema
        rng.shuffle(replay)
        sd += replay[: args.replay_rows]
        print(f"strands replay rows: {min(len(replay), args.replay_rows)} of {len(replay)}")
    rng.shuffle(sd)
    write(os.path.join(args.out, "strands", "train.jsonl"), sd)
    for sp in ("val", "test", "ood"):
        write(os.path.join(args.out, "strands", f"{sp}.jsonl"), [strands_row(r) for r in splits[sp]])

    # Laya: instruction variants as data augmentation (its trainer has no
    # variant field); one variant per row keeps the size unchanged.
    lt = [laya_row(r, rng.choice([None, None] + VARIANTS)) for r in splits["train"]]
    write(os.path.join(args.out, "laya", "train.jsonl"), lt)
    for sp in ("val", "test", "ood"):
        write(os.path.join(args.out, "laya", f"{sp}.jsonl"), [laya_row(r) for r in splits[sp]])
    for name in ("strands", "laya"):
        for sp in ("train", "val", "test", "ood"):
            n = sum(1 for _ in open(os.path.join(args.out, name, f"{sp}.jsonl"), encoding="utf-8"))
            print(f"{name}/{sp}: {n}")


if __name__ == "__main__":
    main()
