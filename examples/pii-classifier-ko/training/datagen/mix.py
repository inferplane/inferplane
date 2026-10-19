"""Add KDPII rows to the synthetic neutral splits.

Usage: python3 mix.py --synthetic ../data/neutral_v4 --kdpii ../data/kdpii --out ../data/mixed_v4 \
         [--kdpii-frac 0.6] [--max-share 0.5] [--seed 13]

Writes train/val/test/ood.jsonl for convert.py and kdpii_test.jsonl, which
convert.py never reads: KDPII test dialogues stay evaluation-only. At most
--max-share of the train rows come from KDPII, so the coding-traffic genres
keep their weight.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import random


def read(path: str) -> list[dict]:
    with open(path, encoding="utf-8") as f:
        return [json.loads(l) for l in f]


def mix(synth: dict[str, list[dict]], kd: dict[str, list[dict]], frac: float, max_share: float,
        rng: random.Random) -> dict[str, list[dict]]:
    cap = math.floor(max_share / (1 - max_share) * len(synth["train"])) if max_share < 1 else len(kd["train"])
    n = min(round(len(kd["train"]) * frac), cap)
    out = {
        "train": synth["train"] + rng.sample(kd["train"], n),
        "val": synth["val"] + kd["val"],
        "test": list(synth["test"]),
        "ood": list(synth["ood"]),
        "kdpii_test": list(kd["test"]),
    }
    for v in out.values():
        rng.shuffle(v)
    return out


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--synthetic", required=True)
    ap.add_argument("--kdpii", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--kdpii-frac", type=float, default=0.6)
    ap.add_argument("--max-share", type=float, default=0.5)
    ap.add_argument("--seed", type=int, default=13)
    a = ap.parse_args()
    synth = {sp: read(os.path.join(a.synthetic, f"{sp}.jsonl")) for sp in ("train", "val", "test", "ood")}
    kd = {sp: read(os.path.join(a.kdpii, f"{sp}.jsonl")) for sp in ("train", "val", "test")}
    out = mix(synth, kd, a.kdpii_frac, a.max_share, random.Random(a.seed))
    os.makedirs(a.out, exist_ok=True)
    for sp, rs in out.items():
        with open(os.path.join(a.out, f"{sp}.jsonl"), "w", encoding="utf-8") as f:
            for r in rs:
                f.write(json.dumps(r, ensure_ascii=False) + "\n")
        print(f"{sp}: {len(rs)} rows, kdpii {sum(r.get('source') == 'kdpii' for r in rs)}, positive {sum(r['label'] for r in rs)}")


if __name__ == "__main__":
    main()
