"""Fill independent templates into evaluation rows.

Three checks on top of datagen/fill.py:
  leak      a template that writes a value outside a slot (a literal phone,
            ID number, email, card or secret) is dropped: its label would be
            wrong for negatives and untested for positives.
  overlap   character 5-gram Jaccard against every training template; anything
            at or above --max-overlap is dropped as a near copy.
  formats   half the rows use AltValues: value formats that never appear in
            training (+82 phones, spaced RRNs, company email domains, jibun
            addresses, compound surnames, AWS keys).

Usage: python3 build.py --templates ../data/indep/templates.jsonl \
         --train-templates ../data/templates.jsonl --out ../data/indep/rows.jsonl
"""

from __future__ import annotations

import argparse
import collections
import hashlib
import json
import os
import random
import re
import string
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "datagen"))
from fill import fill, pair_modes  # noqa: E402
from kr_values import Values, label  # noqa: E402

SLOT_RE = re.compile(r"⟦[A-Z_]+⟧")
LEAKS = {
    "phone": re.compile(r"(?<![\d.])(?:\+82[- ]?)?0?1[016789][-. )]?\d{3,4}[-. ]?\d{4}(?!\d)|(?<![\d.])0[2-6]\d?[-. )]\d{3,4}[-. ]\d{4}(?!\d)"),
    "rrn": re.compile(r"(?<!\d)\d{6}\s?-\s?[1-8]\d{6}(?!\d)"),
    "email": re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+\.[A-Za-z.]{2,}"),
    "card": re.compile(r"(?<!\d)\d{4}[- ]\d{4}[- ]\d{4}[- ]\d{4}(?!\d)"),
    "secret": re.compile(r"\b(?:sk-[A-Za-z0-9]{16,}|ghp_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{16}|xox[bp]-[0-9A-Za-z-]{10,}|eyJ[A-Za-z0-9_-]{20,})"),
}

COMPOUND_SURNAMES = ["남궁", "선우", "제갈", "황보", "독고", "사공"]
COMPANY_DOMAINS = ["corp.co.kr", "hanbit-tech.co.kr", "seoul-clinic.kr", "minjung.or.kr", "daehan-logis.com"]
DONG = ["역삼동", "서교동", "잠실동", "우동", "정자동", "봉명동", "연동", "상무동"]


class AltValues(Values):
    """Value formats absent from training. Same categories, same label policy."""

    def name(self) -> str:
        if self.r.random() < 0.25:
            return self.r.choice(COMPOUND_SURNAMES) + "".join(self.r.choice("민서지수현준예도하윤우진영") for _ in range(2))
        return super().name()

    def phone(self) -> str:
        a, b = self.r.randint(1000, 9999), self.r.randint(1000, 9999)
        return self.r.choice([f"+82 10-{a}-{b}", f"+821{0}{a}{b}", f"(010) {a} {b}", f"010)  {a}-{b}", f"010-{a}-{b} (휴대폰)"])

    def rrn(self) -> str:
        s = super().rrn().replace("-", "")
        return self.r.choice([f"{s[:6]} - {s[6:]}", f"{s[:6]}–{s[6:]}", f"주민번호 앞 {s[:6]} 뒤 {s[6:]}"])

    def email(self, name: str | None = None) -> str:
        local = super().email(name).split("@")[0]
        return self.r.choice([f"{local}@{self.r.choice(COMPANY_DOMAINS)}", f"{local.upper()}@NAVER.COM", f"{local} at gmail dot com"])

    def address(self) -> str:
        return self.r.choice([
            f"서울 강남구 {self.r.choice(DONG)} {self.r.randint(1, 999)}-{self.r.randint(1, 99)}",
            f"경기 성남시 분당구 {self.r.choice(DONG)} {self.r.randint(1, 300)}번지 {self.r.randint(1, 20)}층",
            super().address().replace("특별시", "").replace("광역시", ""),
        ])

    def account(self) -> str:
        num = super().account().split(" ")[-1]
        return self.r.choice([f"{num} ({self.r.choice(['신한', '국민', '우리', '하나'])})", num.replace("-", ""), f"계좌 {num} 예금주 본인"])

    def card(self) -> str:
        s = super().card().replace("-", "")
        return self.r.choice([" ".join(s[i:i + 4] for i in range(0, 16, 4)), f"{s[:4]}-{s[4:8]}-{s[8:12]}-{s[12:]} 유효기간 0{self.r.randint(1, 9)}/2{self.r.randint(7, 9)}"])

    def credential(self) -> str:
        rand = lambda n, al=string.ascii_uppercase + string.digits: "".join(self.r.choice(al) for _ in range(n))
        return self.r.choice([
            "AKIA" + rand(16),
            rand(40, string.ascii_letters + string.digits + "/+"),
            "glpat-" + rand(20, string.ascii_letters + string.digits),
            "postgres://app:" + rand(14, string.ascii_letters + string.digits) + "@db.internal:5432/app",
        ])


def shingles(text: str, n: int = 5) -> set[str]:
    t = re.sub(r"\s+", " ", SLOT_RE.sub("§", text))
    return {t[i:i + n] for i in range(max(len(t) - n + 1, 1))}


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--templates", required=True)
    ap.add_argument("--train-templates", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--seed", type=int, default=11)
    ap.add_argument("--pair-rate", type=float, default=0.35)
    ap.add_argument("--alt-rate", type=float, default=0.5)
    ap.add_argument("--max-overlap", type=float, default=0.5)
    args = ap.parse_args()

    rng = random.Random(args.seed)
    std, alt = Values(rng), AltValues(rng)
    with open(args.train_templates, encoding="utf-8") as f:
        train = [json.loads(l)["text"] for l in f]
    train_sh = [shingles(t) for t in train if t]
    index: dict[str, list[int]] = collections.defaultdict(list)
    for i, s in enumerate(train_sh):
        for g in s:
            index[g].append(i)

    with open(args.templates, encoding="utf-8") as f:
        templates = [t for t in (json.loads(l) for l in f) if t.get("text")]
    drops = collections.Counter()
    rows, seen = [], set()
    overlaps = []
    for t in templates:
        bare = SLOT_RE.sub(" ", t["text"])
        leak = next((k for k, rx in LEAKS.items() if rx.search(bare)), None)
        if leak:
            drops[f"leak_{leak}"] += 1
            continue
        s = shingles(t["text"])
        shared = collections.Counter(i for g in s for i in index.get(g, ()))
        best = max((c / (len(s) + len(train_sh[i]) - c) for i, c in shared.items()), default=0.0)
        overlaps.append(best)
        if best >= args.max_overlap:
            drops["overlap"] += 1
            continue
        variants = [False] + pair_modes(t["text"], rng, args.pair_rate)
        for masked in variants:
            fmt = "alt" if rng.random() < args.alt_rate else "std"
            text, present = fill(t["text"], alt if fmt == "alt" else std, masked=masked)
            if text in seen:
                continue
            seen.add(text)
            lab, cats = label(present)
            rows.append({"id": hashlib.sha256(text.encode()).hexdigest()[:16], "text": text, "label": lab, "categories": cats,
                         "genre": t["genre"], "kind": t["kind"], "lang": t["lang"], "source": {"all": "pair", "name": "pair_b"}.get(masked, "llm"),
                         "template_id": t["template_id"], "split": "indep", "generator": t["generator"], "fmt": fmt,
                         "train_overlap": round(best, 3)})
    rng.shuffle(rows)
    with open(args.out, "w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")
    overlaps.sort()
    pos = sum(r["label"] for r in rows)
    print(f"templates {len(templates)}, dropped {dict(drops)}")
    if overlaps:
        print(f"train overlap (5-gram Jaccard) p50 {overlaps[len(overlaps) // 2]:.3f} p95 {overlaps[int(len(overlaps) * .95)]:.3f} max {overlaps[-1]:.3f}")
    print(f"rows {len(rows)}, positive {pos} ({pos / max(len(rows), 1):.0%}), pairs {sum(r['source'] == 'pair' for r in rows)}, alt {sum(r['fmt'] == 'alt' for r in rows)}")
    print("by generator", dict(collections.Counter(r["generator"] for r in rows)))
    print("by kind", dict(collections.Counter(r["kind"] for r in rows)))


if __name__ == "__main__":
    main()
