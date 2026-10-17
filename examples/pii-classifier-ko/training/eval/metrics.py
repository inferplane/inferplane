"""Metrics over prediction files written by score_http.py or regexbaseline.

Usage: python3 metrics.py name=preds.jsonl [name=preds.jsonl ...] [--json out.json] [--threshold 0.5]
"""

from __future__ import annotations

import argparse
import collections
import json
import math
import statistics


def auroc(ps: list[float], ys: list[int]) -> float:
    pos = [p for p, y in zip(ps, ys) if y]
    neg = [p for p, y in zip(ps, ys) if not y]
    if not pos or not neg:
        return float("nan")
    order = sorted(range(len(ps)), key=lambda i: ps[i])
    ranks = [0.0] * len(ps)
    i = 0
    while i < len(order):  # average ranks for ties
        j = i
        while j + 1 < len(order) and ps[order[j + 1]] == ps[order[i]]:
            j += 1
        for k in range(i, j + 1):
            ranks[order[k]] = (i + j) / 2 + 1
        i = j + 1
    rpos = sum(r for r, y in zip(ranks, ys) if y)
    return (rpos - len(pos) * (len(pos) + 1) / 2) / (len(pos) * len(neg))


def ece(ps: list[float], ys: list[int], bins: int = 10) -> float:
    b = collections.defaultdict(list)
    for p, y in zip(ps, ys):
        b[min(int(p * bins), bins - 1)].append((p, y))
    return sum(len(v) / len(ps) * abs(statistics.mean(p for p, _ in v) - statistics.mean(y for _, y in v)) for v in b.values())


def summarize(rows: list[dict], thr: float, band: tuple[float, float]) -> dict:
    ok = [r for r in rows if r["p"] is not None]
    ps, ys = [r["p"] for r in ok], [int(r["label"]) for r in ok]
    pred = [p >= thr for p in ps]
    tp = sum(p and y for p, y in zip(pred, ys))
    fp = sum(p and not y for p, y in zip(pred, ys))
    fn = sum((not p) and y for p, y in zip(pred, ys))
    tn = len(ok) - tp - fp - fn
    prec = tp / (tp + fp) if tp + fp else float("nan")
    rec = tp / (tp + fn) if tp + fn else float("nan")
    out = {
        "n": len(rows), "errors": len(rows) - len(ok),
        "accuracy": (tp + tn) / len(ok) if ok else float("nan"),
        "precision": prec, "recall": rec,
        "f1": 2 * prec * rec / (prec + rec) if prec + rec and not math.isnan(prec + rec) else float("nan"),
        "fpr": fp / (fp + tn) if fp + tn else float("nan"),
        "auroc": auroc(ps, ys),
        "brier": statistics.mean((p - y) ** 2 for p, y in zip(ps, ys)) if ok else float("nan"),
        "ece": ece(ps, ys) if ok else float("nan"),
    }
    # Abstain band: Clear below lo, Flagged at or above hi, Abstain between.
    lo, hi = band
    decided = [(p, y) for p, y in zip(ps, ys) if p < lo or p >= hi]
    out["abstain_rate"] = 1 - len(decided) / len(ok) if ok else float("nan")
    out["decided_accuracy"] = statistics.mean((p >= hi) == bool(y) for p, y in decided) if decided else float("nan")
    out["decided_recall"] = (sum(p >= hi and y for p, y in decided) / sum(ys)) if sum(ys) else float("nan")
    out["decided_fpr"] = (sum(p >= hi and not y for p, y in decided) / (len(ys) - sum(ys))) if len(ys) - sum(ys) else float("nan")
    cat = collections.defaultdict(lambda: [0, 0])
    for r in ok:
        for c in r["categories"]:
            cat[c][0] += r["p"] >= thr
            cat[c][1] += 1
    out["recall_by_category"] = {c: round(a / n, 3) for c, (a, n) in sorted(cat.items())}
    neg = collections.defaultdict(lambda: [0, 0])
    for r in ok:
        if not r["label"]:
            key = "pair" if r["source"] == "pair" else r["kind"]
            neg[key][0] += r["p"] >= thr
            neg[key][1] += 1
    out["fpr_by_negative_kind"] = {k: round(a / n, 3) for k, (a, n) in sorted(neg.items())}
    langs = collections.defaultdict(list)
    for r in ok:
        langs[r["lang"]].append((r["p"] >= thr) == bool(r["label"]))
    out["accuracy_by_lang"] = {k: round(statistics.mean(v), 3) for k, v in sorted(langs.items())}
    ms = sorted(r["ms"] for r in ok)
    if ms:
        out["latency_ms_p50"] = round(ms[len(ms) // 2], 2)
        out["latency_ms_p95"] = round(ms[min(len(ms) - 1, int(len(ms) * 0.95))], 2)
    return out


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("preds", nargs="+", help="name=path")
    ap.add_argument("--threshold", type=float, default=0.5)
    ap.add_argument("--band", default="0.1,0.9")
    ap.add_argument("--json")
    a = ap.parse_args()
    band = tuple(float(x) for x in a.band.split(","))
    results = {}
    for spec in a.preds:
        name, path = spec.split("=", 1)
        with open(path, encoding="utf-8") as f:
            results[name] = summarize([json.loads(l) for l in f], a.threshold, band)
    cols = ["n", "accuracy", "precision", "recall", "f1", "fpr", "auroc", "brier", "ece", "abstain_rate", "decided_accuracy", "latency_ms_p50", "latency_ms_p95"]
    print("| model | " + " | ".join(cols) + " |")
    print("|" + "---|" * (len(cols) + 1))
    for name, m in results.items():
        cells = [f"{m.get(c, float('nan')):.3f}" if isinstance(m.get(c), float) else str(m.get(c, "")) for c in cols]
        print(f"| {name} | " + " | ".join(cells) + " |")
    if a.json:
        with open(a.json, "w", encoding="utf-8") as f:
            json.dump(results, f, ensure_ascii=False, indent=2)


if __name__ == "__main__":
    main()
