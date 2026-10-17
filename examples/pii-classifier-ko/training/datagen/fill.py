"""Fill slot templates with synthetic values, add masked minimal pairs, split.

Usage: python3 fill.py --templates data/templates.jsonl --out data/neutral [--seed 7]
Writes {train,val,test,ood}.jsonl in the neutral format:
  {id, text, label, categories, genre, kind, lang, source, template_id, split}
"""

from __future__ import annotations

import argparse
import collections
import hashlib
import json
import os
import random
import re

from kr_values import SLOTS, Values, label

SLOT_RE = re.compile(r"⟦([A-Z_]+)⟧")
OOD_GENRES = {"insurance_claim", "terminal_output"}
FILLS = {"train": 2, "val": 1, "test": 1, "ood": 1}

# Masking used to build minimal-pair negatives: same document, values masked.
MASKS = {
    "PHONE": lambda v: v.masked_phone(),
    "RRN": lambda v: v.masked_rrn(),
    "FRN": lambda v: v.masked_rrn(),
    "EMAIL": lambda v: v.placeholder_email(),
    "CARD": lambda v: "****-****-****-" + str(v.r.randint(1000, 9999)),
    "ACCOUNT": lambda v: v.r.choice(["***-**-******", "국민은행 ****-**-****", "(계좌번호 비공개)"]),
    "PASSPORT": lambda v: "M********",
    "LICENSE": lambda v: "**-**-******-**",
    "CREDENTIAL": lambda v: v.r.choice(["${API_KEY}", "<redacted>", "********", "os.environ['TOKEN']"]),
    "NAME": lambda v: v.r.choice(["김OO", "이OO", "박○○", "고객", "OOO", "홍길동"]),
    "EMPLOYEE_ID": lambda v: v.r.choice(["E******", "사번 비공개", "EMP-XXXXX", "(사번 삭제)"]),
    "PLATE": lambda v: v.r.choice(["12가 ****", "***허 ****", "(차량번호 가림)"]),
}


def split_of(template_id: str, genre: str) -> str:
    if genre in OOD_GENRES:
        return "ood"
    h = int(hashlib.sha256(template_id.encode()).hexdigest(), 16) % 100
    return "train" if h < 80 else ("val" if h < 90 else "test")


def fill(text: str, v: Values, masked: bool | str = False) -> tuple[str, list[str]]:
    """masked: False (no masking), True or "all" (every slot in MASKS), "name" (NAME only)."""
    mask = set(MASKS) if masked in (True, "all") else ({"NAME"} if masked == "name" else set())
    cache: dict[str, str] = {}
    present: list[str] = []
    first_name: list[str] = []

    def sub(m: re.Match) -> str:
        slot = m.group(1)
        if slot in mask:
            return MASKS[slot](v)
        present.append(slot)
        if slot in cache and v.r.random() < 0.5:
            return cache[slot]
        method = getattr(v, SLOTS[slot][0])
        val = method(first_name[0]) if slot == "EMAIL" and first_name and v.r.random() < 0.6 else method()
        if slot == "NAME" and not first_name:
            first_name.append(val)
        cache[slot] = val
        return val

    return SLOT_RE.sub(sub, text), present


def maskable(text: str) -> bool:
    """A full-mask pair exists when the document is positive and masking every slot in MASKS makes it negative."""
    slots = set(SLOT_RE.findall(text))
    return label(list(slots))[0] == 1 and label(list(slots - set(MASKS)))[0] == 0


def name_maskable(text: str) -> bool:
    """A name-only pair stays positive under rule B: linking ID + sensitive attribute, no direct identifier."""
    slots = set(SLOT_RE.findall(text))
    return "NAME" in slots and not any(SLOTS[s][2] for s in slots) and label(list(slots - {"NAME"}))[0] == 1


def pair_modes(text: str, rng, pair_rate: float) -> list[str]:
    """Masked variants to add for one template; rule-B templates get both, as a positive/negative pair."""
    modes = []
    if name_maskable(text) and rng.random() < pair_rate:
        modes += ["name", "all"]
    elif maskable(text) and rng.random() < pair_rate:
        modes.append("all")
    return modes


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--templates", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--seed", type=int, default=7)
    ap.add_argument("--pair-rate", type=float, default=0.35)
    args = ap.parse_args()

    v = Values(random.Random(args.seed))
    os.makedirs(args.out, exist_ok=True)
    rows: dict[str, list[dict]] = collections.defaultdict(list)
    seen: set[str] = set()
    with open(args.templates, encoding="utf-8") as f:
        templates = [json.loads(l) for l in f]
    for t in templates:
        if not t.get("text"):
            continue
        sp = split_of(t["template_id"], t["genre"])
        variants = [(False, i) for i in range(FILLS[sp])] + [(m, 0) for m in pair_modes(t["text"], v.r, args.pair_rate)]
        for masked, i in variants:
            text, present = fill(t["text"], v, masked=masked)
            if text in seen:
                continue
            seen.add(text)
            lab, cats = label(present)
            rows[sp].append({
                "id": hashlib.sha256(text.encode()).hexdigest()[:16], "text": text, "label": lab, "categories": cats,
                "genre": t["genre"], "kind": t["kind"], "lang": t["lang"], "source": {"all": "pair", "name": "pair_b"}.get(masked, "llm"),
                "template_id": t["template_id"], "split": sp,
            })
    for sp in ("train", "val", "test", "ood"):
        rs = rows.get(sp, [])
        v.r.shuffle(rs)
        with open(os.path.join(args.out, f"{sp}.jsonl"), "w", encoding="utf-8") as f:
            for r in rs:
                f.write(json.dumps(r, ensure_ascii=False) + "\n")
        pos = sum(r["label"] for r in rs)
        cats = collections.Counter(c for r in rs for c in r["categories"])
        print(f"{sp}: {len(rs)} rows, positive {pos} ({pos / max(len(rs), 1):.0%}), pairs {sum(r['source'] == 'pair' for r in rs)}, rule-B pairs {sum(r['source'] == 'pair_b' for r in rs)}, top categories {cats.most_common(6)}")


if __name__ == "__main__":
    main()
