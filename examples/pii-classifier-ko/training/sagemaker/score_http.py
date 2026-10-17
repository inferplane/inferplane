"""Score neutral rows against a /v1/systemone server (Laya or strands-decider).

Both backends share the wire: POST /v1/systemone {state, questions} ->
{answers: {<id>: {type: "noul", noul: P(true)}}}. This is the same request a
gateway `systemone` classifier plugin would send, so latency is measured on
the serving path, one request per row.
"""

from __future__ import annotations

import argparse
import json
import time
import urllib.request

INSTRUCTIONS = "Does this text contain personal information about a specific person, or a secret credential?"


def post(url: str, body: dict, timeout: float = 120) -> dict:
    req = urllib.request.Request(url, data=json.dumps(body).encode(), headers={"content-type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read())


def wait_health(base: str, timeout_s: int = 1800) -> None:
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(base + "/health", timeout=5) as r:
                if r.status == 200:
                    return
        except Exception:  # noqa: BLE001 - server still starting
            pass
        time.sleep(5)
    raise TimeoutError(f"{base} not healthy after {timeout_s}s")


def score(base: str, rows_path: str, out_path: str, model: str | None = None, criteria: dict | None = None, limit: int = 0) -> None:
    with open(rows_path, encoding="utf-8") as f, open(out_path, "w", encoding="utf-8") as out:
        for n, line in enumerate(f):
            if limit and n >= limit:
                break
            r = json.loads(line)
            q = {"type": "noul", "instructions": INSTRUCTIONS}
            if criteria:  # must match the option descriptions the checkpoint was trained with
                q["criteria"] = criteria
            body = {"state": r["text"], "questions": {"has_pii": q}}
            if model:
                body["model"] = model
            t0 = time.perf_counter()
            try:
                resp = post(base + "/v1/systemone", body)
                p = float(resp["answers"]["has_pii"]["noul"])
                err = None
            except Exception as e:  # noqa: BLE001 - recorded per row
                p, err = None, f"{type(e).__name__}: {str(e)[:200]}"
            ms = (time.perf_counter() - t0) * 1000
            out.write(json.dumps({"id": r["id"], "label": r["label"], "p": p, "ms": ms, "err": err,
                                  "categories": r["categories"], "genre": r["genre"], "kind": r["kind"],
                                  "lang": r["lang"], "source": r["source"]}, ensure_ascii=False) + "\n")


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:8000")
    ap.add_argument("--rows", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--model")
    ap.add_argument("--criteria", help='JSON {"false": ..., "true": ...}')
    ap.add_argument("--limit", type=int, default=0)
    a = ap.parse_args()
    wait_health(a.base)
    score(a.base, a.rows, a.out, a.model, json.loads(a.criteria) if a.criteria else None, a.limit)
