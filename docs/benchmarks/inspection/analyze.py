"""Validate raw trials and summarize local synthetic inspection measurements."""

import json
from pathlib import Path
import re
import statistics
import sys


def read_trials(path):
    trials = {}
    pending = None
    text = path.read_text()
    if not re.search(r"(?m)^PASS$", text) or re.search(r"(?m)^FAIL", text):
        raise ValueError(f"{path.name}: missing successful completion")
    for line in text.splitlines():
        name = re.match(r"(Benchmark\S+)", line)
        if name:
            pending = re.sub(r"-\d+$", "", name[1])
            line = line[name.end():]
        # Go writes the benchmark name before execution. A logger can split
        # that prefix from its numeric row; keep the pending name across logs.
        fields = line.split()
        if len(fields) < 3 or not fields[0].isdigit() or fields[2] != "ns/op":
            continue
        if pending is None or len(fields[1:]) % 2:
            raise ValueError(f"{path.name}: malformed benchmark row")
        values = {"operations": int(fields[0])}
        for value, unit in zip(fields[1::2], fields[2::2]):
            values[unit] = float(value)
        if values["failures"] != 0:
            raise ValueError(f"{path.name}: operation failed")
        if pending.startswith("BenchmarkGatewayInspection/"):
            denied = "/block/" in pending or "/bedrock-converse/classifier/" in pending
            if values["dispatches/op"] != (0 if denied else 1):
                raise ValueError(f"{path.name}: incorrect dispatch count")
        trials.setdefault(pending, []).append(values)
    if not trials or any(len(rows) != 5 for rows in trials.values()):
        raise ValueError(f"{path.name}: expected five trials per case")
    return {
        name: {
            "trials": len(rows),
            "operations": [row["operations"] for row in rows],
            "workers": [row["workers"] for row in rows],
            "median": {
                unit: statistics.median(row[unit] for row in rows)
                for unit in rows[0] if unit != "operations"
            },
            "ns_op_range": [min(row["ns/op"] for row in rows), max(row["ns/op"] for row in rows)],
        }
        for name, rows in sorted(trials.items())
    }


def summarize(directory, phase):
    result = {}
    for kind in ("sensitivity", "router", "gateway"):
        for suffix in ("", "-concurrent"):
            path = directory / f"{phase}-{kind}{suffix}.txt"
            result[kind + suffix] = read_trials(path)
    (directory / f"{phase}-summary.json").write_text(json.dumps(result, indent=2) + "\n")
    return result


def comparison(baseline, after):
    changes = {}
    for group, cases in after.items():
        changes[group] = {}
        for name, current in cases.items():
            old_name = name.replace("BenchmarkInspect/full/", "BenchmarkInspect/").replace(
                "BenchmarkInspect/structure/", "BenchmarkInspect/"
            )
            old = baseline[group][old_name]
            changes[group][name] = {
                "baseline_ns_op": old["median"]["ns/op"],
                "after_ns_op": current["median"]["ns/op"],
                "ns_op_change_percent": 100 * (current["median"]["ns/op"] / old["median"]["ns/op"] - 1),
                "baseline_bytes_op": old["median"]["B/op"],
                "after_bytes_op": current["median"]["B/op"],
                "bytes_op_change_percent": 100 * (current["median"]["B/op"] / old["median"]["B/op"] - 1),
                "baseline_workers": old["workers"],
                "after_workers": current["workers"],
            }
    return changes


if __name__ == "__main__":
    directory = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).parent
    baseline = summarize(directory, "baseline")
    print(f"Baseline: {sum(len(cases) for cases in baseline.values())} cases, five trials each.")
    if (directory / "after-gateway-concurrent.txt").is_file():
        after = summarize(directory, "after")
        changes = comparison(baseline, after)
        (directory / "comparison.json").write_text(json.dumps(changes, indent=2) + "\n")
        print(f"After: {sum(len(cases) for cases in after.values())} cases, five trials each.")
    if (directory / "full-repeat-after.txt").is_file():
        old = read_trials(directory / "full-repeat-baseline.txt")
        current = read_trials(directory / "full-repeat-after.txt")
        repeated = comparison({"full-repeat": old}, {"full-repeat": current})["full-repeat"]
        (directory / "full-repeat-comparison.json").write_text(
            json.dumps(repeated, indent=2) + "\n"
        )
        print(f"Interleaved full repeats: {len(repeated)} cases, five trials each.")
