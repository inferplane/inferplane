"""A fixed probe set the checkpoint must answer as expected (a release gate).

Usage: python3 probes.py --base http://127.0.0.1:8095 [--probes probes.jsonl] [--threshold 0.5]
Exits 1 when any probe fails. Values in probes.jsonl are synthetic.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.request

INSTRUCTIONS = "Does this text contain personal information about a specific person, or a secret credential?"


def load(path: str) -> list[dict]:
    with open(path, encoding="utf-8") as f:
        return [json.loads(l) for l in f if l.strip()]


def check(probes: list[dict], scores: list[float], threshold: float) -> list[dict]:
    """A probe expecting 1 needs p >= threshold; a probe expecting 0 needs p < threshold."""
    if len(scores) != len(probes):
        raise ValueError(f"{len(scores)} scores for {len(probes)} probes")
    return [dict(p, p=s) for p, s in zip(probes, scores) if (s >= threshold) != bool(p["expect"])]


def score(base: str, texts: list[str]) -> list[float]:
    body = json.dumps({"model": "multilingual", "states": texts,
                       "questions": {"pii": {"type": "noul", "instructions": INSTRUCTIONS}}}).encode()
    req = urllib.request.Request(base + "/v1/systemone/batch", data=body, headers={"content-type": "application/json"})
    with urllib.request.urlopen(req, timeout=300) as resp:
        return [r["answers"]["pii"]["noul"] for r in json.load(resp)["results"]]


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", required=True)
    ap.add_argument("--probes", default=os.path.join(os.path.dirname(os.path.abspath(__file__)), "probes.jsonl"))
    ap.add_argument("--threshold", type=float, default=0.5)
    a = ap.parse_args()
    ps = load(a.probes)
    fails = check(ps, score(a.base, [p["text"] for p in ps]), a.threshold)
    for f in fails:
        print(f"FAIL expect {f['expect']} p {f['p']:.3f} ({f['why']}): {f['text']}")
    print(f"{len(ps) - len(fails)}/{len(ps)} probes pass")
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(main())
