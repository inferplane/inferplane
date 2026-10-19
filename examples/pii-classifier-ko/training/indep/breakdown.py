"""Accuracy, recall and FPR of each prediction file split by generator, value format, kind, language and genre.

Usage: python3 breakdown.py --rows ../data/indep/rows.jsonl name=preds.jsonl [...] [--exclude ids.txt]
"""

from __future__ import annotations

import argparse
import collections
import json


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--rows", required=True)
    ap.add_argument("--exclude", help="file of row ids to leave out (label disputes)")
    ap.add_argument("preds", nargs="+")
    a = ap.parse_args()
    meta = {}
    with open(a.rows, encoding="utf-8") as f:
        for l in f:
            r = json.loads(l)
            meta[r["id"]] = r
    skip = set(open(a.exclude).read().split()) if a.exclude else set()
    for spec in a.preds:
        name, path = spec.split("=", 1)
        groups = collections.defaultdict(lambda: [0, 0, 0, 0])  # tp fp fn tn
        with open(path, encoding="utf-8") as f:
            for l in f:
                p = json.loads(l)
                if p["p"] is None or p["id"] in skip:
                    continue
                m = meta[p["id"]]
                y, yhat = bool(p["label"]), p["p"] >= 0.5
                idx = (0 if y else 1) if yhat else (2 if y else 3)
                for key in ("all", f"gen:{m['generator']}", f"fmt:{m['fmt']}", f"kind:{m['source'] if m['source'].startswith('pair') else m['kind']}", f"lang:{m['lang']}", f"genre:{m['genre']}"):
                    groups[key][idx] += 1
        print(f"\n{name}")
        print(f"  {'group':<44} {'n':>5} {'acc':>6} {'recall':>7} {'fpr':>6}")
        for key in sorted(groups, key=lambda k: (k != "all", k)):
            tp, fp, fn, tn = groups[key]
            n = tp + fp + fn + tn
            rec = f"{tp / (tp + fn):.3f}" if tp + fn else "-"
            fpr = f"{fp / (fp + tn):.3f}" if fp + tn else "-"
            print(f"  {key:<44} {n:>5} {(tp + tn) / n:>6.3f} {rec:>7} {fpr:>6}")


if __name__ == "__main__":
    main()
