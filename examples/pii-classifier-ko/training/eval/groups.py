"""Overall metrics and per-group recall/FPR for KDPII prediction files.

Usage: python3 groups.py --rows ../data/kdpii/test.jsonl name=preds.jsonl [...] \
         [--kind kdpii_sentence] [--exclude ids.txt] [--json out.json]
Positive groups report recall; negative groups report the flag rate (FPR).
"""

from __future__ import annotations

import argparse
import collections
import json

NAMEISH = {"PS_NAME", "PS_NICKNAME"}


def groups_of(row: dict) -> list[str]:
    if row["label"]:
        kind = "anon_direct" if row["anon_direct"] else ("named" if NAMEISH & set(row["pne"]) else "other_positive")
        return ["positive", kind] + [f"cat:{c}" for c in row["categories"]]
    out = ["negative"]
    if row["pne"] and set(row["pne"]) <= NAMEISH:
        out.append("neg_name_only")
    if not row["pne"]:
        out.append("neg_no_pne")
    return out


def summarize(rows: list[dict], preds: dict[str, float], threshold: float = 0.5, skip: set[str] = frozenset()) -> dict:
    tally = collections.defaultdict(lambda: [0, 0])
    tp = fp = fn = tn = 0
    for r in rows:
        if r["id"] in skip or r["id"] not in preds:
            continue
        flag = preds[r["id"]] >= threshold
        tp += r["label"] and flag
        fn += r["label"] and not flag
        fp += (not r["label"]) and flag
        tn += (not r["label"]) and not flag
        for g in groups_of(r):
            tally[g][0] += 1
            tally[g][1] += flag
    n = tp + fp + fn + tn
    precision = tp / (tp + fp) if tp + fp else 0.0
    recall = tp / (tp + fn) if tp + fn else 0.0
    overall = {"n": n, "acc": (tp + tn) / n if n else 0.0, "recall": recall, "fpr": fp / (fp + tn) if fp + tn else 0.0,
               "precision": precision, "f1": 2 * precision * recall / (precision + recall) if precision + recall else 0.0}
    return {"overall": overall, "groups": {g: {"n": c, "rate": k / c} for g, (c, k) in sorted(tally.items())}}


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--rows", required=True)
    ap.add_argument("--kind", help="kdpii_sentence or kdpii_window; default both")
    ap.add_argument("--exclude", help="file of row ids to leave out (label disputes)")
    ap.add_argument("--threshold", type=float, default=0.5)
    ap.add_argument("--json")
    ap.add_argument("preds", nargs="+", help="name=path")
    a = ap.parse_args()
    with open(a.rows, encoding="utf-8") as f:
        rows = [r for r in map(json.loads, f) if not a.kind or r["kind"] == a.kind]
    skip = set(open(a.exclude).read().split()) if a.exclude else set()
    report = {}
    for spec in a.preds:
        name, path = spec.split("=", 1)
        with open(path, encoding="utf-8") as f:
            preds = {p["id"]: p["p"] for p in map(json.loads, f) if p.get("p") is not None}
        s = summarize(rows, preds, a.threshold, skip)
        report[name] = s
        o = s["overall"]
        print(f"\n{name}: n {o['n']} acc {o['acc']:.4f} recall {o['recall']:.4f} fpr {o['fpr']:.4f} "
              f"precision {o['precision']:.4f} f1 {o['f1']:.4f}")
        for g, v in s["groups"].items():
            print(f"  {g:<24} n {v['n']:>6} rate {v['rate']:.3f}")
    if a.json:
        with open(a.json, "w", encoding="utf-8") as f:
            json.dump(report, f, indent=2, ensure_ascii=False)
            f.write("\n")


if __name__ == "__main__":
    main()
