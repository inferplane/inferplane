"""Turn KDPII (Zenodo record 16759166, CC BY 4.0) into neutral rows under this recipe's label policy.

Usage:
  python3 kdpii.py fetch --out ../data/kdpii/PII_dataset_V3.json
  python3 kdpii.py build --src ../data/kdpii/PII_dataset_V3.json --out ../data/kdpii

build writes {train,val,test}.jsonl. Dialogues are split 8:1:1 by a hash of
the dialogue id, so no dialogue spans two splits, and a val or test row whose
text also occurs in train is dropped. Each dialogue yields one row per
sentence (kdpii_sentence) and one row per window of consecutive sentences of
at most WINDOW_RUNES characters (kdpii_window), the shape the gateway reads.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import urllib.request

URL = "https://zenodo.org/api/records/16759166/files/PII_dataset_V3.json/content"
MD5 = "419921a5e153e8346b6d320d5f084a4a"
WINDOW_RUNES = 1000

# Positive on their own (the policy's direct identifiers).
DIRECT = {
    "QT_RESIDENT_NUMBER": "rrn", "QT_ALIEN_NUMBER": "rrn", "QT_PASSPORT_NUMBER": "passport",
    "QT_DRIVER_NUMBER": "driver_license", "QT_MOBILE": "phone", "TMI_EMAIL": "email",
    "QT_ACCOUNT_NUMBER": "bank_account", "QT_CARD_NUMBER": "card",
}
# Positive only next to a person's name in the same text: the policy's personal
# attributes. Employer, team, job title and school are not among them; the
# synthetic negatives teach "담당자 ⟦NAME⟧ (결제팀)" as 0, and KDPII must agree.
ATTR = {
    "DT_BIRTH": "birthdate", "QT_AGE": "birthdate", "TM_BLOOD_TYPE": "health", "QT_WEIGHT": "health",
    "QT_LENGTH": "health", "OGG_RELIGION": "religion",
}
NAMEISH = {"PS_NAME", "PS_NICKNAME"}
DIRECT_CATS = set(DIRECT.values()) | {"phone", "address"}
SWITCHBOARD = re.compile(r"^\s*1[5-9]\d{2}[-. ]?\d{4}\s*$")
HAS_DIGIT = re.compile(r"\d")
# Everything else (PS_ID, PS_NICKNAME, QT_IP, QT_PLATE_NUMBER, TMI_SITE, LC_PLACE,
# LCP_COUNTRY, OGG_CLUB, CV_SEX, OG_WORKPLACE, OG_DEPARTMENT, CV_POSITION,
# OGG_EDUCATION, FD_MAJOR, QT_GRADE, CV_MILITARY_CAMP) is negative on its own and
# is not an attribute.


def pne_label(spans: list[tuple[str, str]]) -> tuple[int, list[str]]:
    """spans are (KDPII label, surface form) pairs from one text."""
    cats: set[str] = set()
    attrs: set[str] = set()
    named = False
    for lab, form in spans:
        if lab in DIRECT:
            cats.add(DIRECT[lab])
        elif lab == "QT_PHONE":
            if not SWITCHBOARD.match(form):
                cats.add("phone")
        elif lab == "LC_ADDRESS":
            (cats if HAS_DIGIT.search(form) else attrs).add("address")
        elif lab in ATTR:
            attrs.add(ATTR[lab])
        elif lab == "PS_NAME":
            named = True
    if named:
        cats |= attrs
    return (1 if cats else 0), sorted(cats)


def split_of(dialogue_id: str) -> str:
    h = int(hashlib.sha256(dialogue_id.encode()).hexdigest(), 16) % 10
    return "train" if h < 8 else ("val" if h == 8 else "test")


def _row(kind: str, text: str, spans: list[tuple[str, str]], did: str, split: str, pos: int) -> dict:
    label, cats = pne_label(spans)
    labels = sorted({l for l, _ in spans})
    return {
        "id": hashlib.sha256(f"{kind}:{text}".encode()).hexdigest()[:16], "text": text, "label": label,
        "categories": cats, "genre": "kdpii_dialogue", "kind": kind, "lang": "ko", "source": "kdpii",
        "template_id": did, "split": split, "pne": labels,
        "anon_direct": bool(set(cats) & DIRECT_CATS) and not NAMEISH & set(labels), "pos": pos,
    }


def dialogue_rows(dlg: dict) -> list[dict]:
    sents = [s for s in dlg["sentence"] if 0 < len(s["form"]) <= WINDOW_RUNES]
    if not sents:
        return []
    did = ".".join(dlg["sentence"][0]["id"].split(".")[:2])
    split = split_of(did)
    spans_of = [[(p["label"], p["form"]) for p in s["PNE"]] for s in sents]
    rows = [_row("kdpii_sentence", s["form"], sp, did, split, i) for i, (s, sp) in enumerate(zip(sents, spans_of))]
    buf: list[str] = []
    spans: list[tuple[str, str]] = []
    start = 0
    for i, (s, sp) in enumerate(zip(sents, spans_of)):
        if buf and len("\n".join(buf + [s["form"]])) > WINDOW_RUNES:
            rows.append(_row("kdpii_window", "\n".join(buf), spans, did, split, start))
            buf, spans, start = [], [], i
        buf.append(s["form"])
        spans += sp
    rows.append(_row("kdpii_window", "\n".join(buf), spans, did, split, start))
    return rows


def build(dialogues: list[dict]) -> dict[str, list[dict]]:
    out: dict[str, list[dict]] = {"train": [], "val": [], "test": []}
    for dlg in dialogues:
        for r in dialogue_rows(dlg):
            out[r["split"]].append(r)
    train_texts = {r["text"] for r in out["train"]}
    for sp in out:
        seen: set[str] = set()
        kept = []
        for r in out[sp]:
            if r["id"] in seen or (sp != "train" and r["text"] in train_texts):
                continue
            seen.add(r["id"])
            kept.append(r)
        out[sp] = kept
    return out


def fetch(out: str) -> None:
    os.makedirs(os.path.dirname(out) or ".", exist_ok=True)
    tmp = out + ".part"
    with urllib.request.urlopen(URL, timeout=300) as resp, open(tmp, "wb") as f:
        f.write(resp.read())
    with open(tmp, "rb") as f:
        digest = hashlib.md5(f.read()).hexdigest()  # noqa: S324 - matches the checksum Zenodo publishes
    if digest != MD5:
        os.remove(tmp)
        raise SystemExit(f"md5 {digest} does not match {MD5}")
    os.replace(tmp, out)


def main() -> None:
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    f = sub.add_parser("fetch")
    f.add_argument("--out", required=True)
    b = sub.add_parser("build")
    b.add_argument("--src", required=True)
    b.add_argument("--out", required=True)
    a = ap.parse_args()
    if a.cmd == "fetch":
        fetch(a.out)
        return
    with open(a.src, encoding="utf-8") as fh:
        splits = build(json.load(fh))
    os.makedirs(a.out, exist_ok=True)
    for sp, rows in splits.items():
        with open(os.path.join(a.out, f"{sp}.jsonl"), "w", encoding="utf-8") as fh:
            for r in rows:
                fh.write(json.dumps(r, ensure_ascii=False) + "\n")
        pos = sum(r["label"] for r in rows)
        anon = sum(r["anon_direct"] for r in rows)
        print(f"{sp}: {len(rows)} rows, positive {pos} ({pos / max(len(rows), 1):.1%}), anonymous direct {anon}")


if __name__ == "__main__":
    main()
