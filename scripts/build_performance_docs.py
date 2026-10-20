"""Build public performance tables from saved measurements; never invoke models."""

import argparse
from collections import Counter
import hashlib
import json
from pathlib import Path
import re
from statistics import median


ROOT = Path(__file__).resolve().parents[1]
DEEP = "docs/benchmarks/deep-results-2026-10-19/data.json"
GATEWAY = "docs/benchmarks/results-2026-10-19/benchmark.json"
LIVE = "docs/benchmarks/converse-admin-results-2026-10-20/"
SOURCES = [DEEP, GATEWAY, LIVE + "live-ui.json", LIVE + "live-sdk-streams.json"]
# Publication is an allow-list: prompts, responses, errors and execution arguments
# remain in the repository archive, never in the site's public data projection.
RECORD_FIELDS = (
    "id", "model_id", "model_name", "route", "phase", "task", "region", "transport",
    "input_style", "context_window", "max_output_tokens", "success", "failure_class",
    "status", "stop_reason", "terminal", "retrieval_correct", "elapsed_ms", "ttft_ms",
    "observed_input_tokens", "context_occupancy", "billed_output_tokens_per_second",
    "estimated_cost_micro_usd", "financial_state", "cache_requested",
)
MODEL_FIELDS = (
    "id", "name", "provider", "api", "region", "context_window", "context_spec",
    "rates", "rate_source", "source", "usage_input_includes_cache",
)
FAILURES = {
    "context_limit": ("Context rejected", "컨텍스트 거절"),
    "content_filtered": ("Filtered", "콘텐츠 필터"),
    "output_limit": ("Output cap", "출력 한도"),
    "upstream_error": ("Upstream error", "업스트림 오류"),
    "unavailable": ("Unavailable in tested access", "시험 접근에서 호출 불가"),
    "body_limit": ("Body limit", "본문 크기 제한"),
    "retrieval_mismatch": ("Retrieval mismatch", "회수 오답"),
    "empty_output": ("Empty output", "빈 응답"),
    "measurement_interrupted": ("Interrupted", "중단"),
    "incomplete_stream": ("Incomplete stream", "스트림 미완료"),
    "timeout": ("Timeout", "시간 초과"),
    "budget_skipped": ("Not invoked", "호출하지 않음"),
}


def read(path):
    return json.loads((ROOT / path).read_text(encoding="utf-8"))


def project_record(record):
    projected = {key: record[key] for key in RECORD_FIELDS if key in record}
    projected["usage"] = {
        key: record.get("usage", {}).get(key)
        for key in ("input", "output", "read", "write")
        if key in (record.get("usage") or {})
    }
    return projected


def project_native_ui(row):
    result = row["result"]
    return {
        key: row[key] for key in ("sequence", "round", "side", "model", "protocol")
    } | {"result": {
        **{key: result[key] for key in (
            "protocol", "model", "http_status", "response_success", "outcome",
            "stop_reason", "duration_ms", "ttft_ms", "settled", "accounting_known",
        )},
        "usage": {key: result["usage"][key] for key in ("input_tokens", "output_tokens")},
        "cost": {key: result["cost"][key] for key in (
            "amount_usd_micros", "pricing_missing", "pricing_version",
        )},
    }}


def project_native_stream(row):
    return {
        **{key: row[key] for key in (
            "model", "complete", "ttft_ms", "cost_micro_usd", "http_status",
            "stop_reason", "duration_ms",
        )},
        "usage": {key: row["usage"][key] for key in (
            "inputTokens", "outputTokens", "totalTokens",
        )},
    }


def import_recordings():
    deep = read(DEEP)
    records = [
        project_record(r) for r in deep["records"] if r["route"] in ("direct", "mayu")
    ]
    by_id = {r["id"]: r for r in records}
    models = []
    for source in deep["inventory"]["models"]:
        model = {key: source[key] for key in MODEL_FIELDS if key in source}
        direct = [r for r in records if r["model_id"] == model["id"] and r["route"] == "direct"]
        accepted = [
            r for r in direct if r.get("success") and r.get("retrieval_correct")
            and 0 < (r.get("context_occupancy") or 0) <= 1
        ]
        model["largest_retrieval"] = max(
            accepted, key=lambda r: r["observed_input_tokens"], default=None
        )
        model["generation"] = {
            phase: next((r for r in direct if r["phase"] == phase), None)
            for phase in ("generation-short", "generation-98")
        }
        models.append(model)
    batches = []
    for batch in deep["batches"]:
        if batch["route"] != "mayu" or not batch["phase"].startswith("concurrency-"):
            continue
        batch_records = [by_id[key] for key in batch["request_ids"]]
        batches.append({
            "model": batch_records[0]["model_name"],
            "workers": batch["workers"],
            "requests": len(batch_records),
            "successes": batch["successes"],
            "elapsed_seconds": batch["elapsed_seconds"],
            "successful_output_tokens": batch["successful_output_tokens"],
            "aggregate_successful_output_tps": batch["aggregate_successful_output_tps"],
        })
    return {
        "schema": 1,
        "as_of": "2026-10-20",
        "measurement_dates": {"model_and_gateway": "2026-10-19", "native_converse": "2026-10-20"},
        "sources": [
            {"path": path, "sha256": hashlib.sha256((ROOT / path).read_bytes()).hexdigest()}
            for path in SOURCES
        ],
        "cost_basis": "Configured-price estimates in integer microUSD; not AWS invoices.",
        "models": models,
        "records": records,
        "gateway": [r for r in read(GATEWAY)["summary"] if r["route"] == "mayu"],
        "concurrency": sorted(batches, key=lambda b: (b["model"], b["workers"])),
        "native_ui": [project_native_ui(r) for r in read(LIVE + "live-ui.json")["rows"]],
        "native_streams": [project_native_stream(r) for r in read(LIVE + "live-sdk-streams.json")],
    }


def money(value):
    if value is None:
        return "—"
    # Costs are integer microUSD throughout; formatting does not use float money.
    return f"${value // 1_000_000:,}.{value % 1_000_000:06d}"


def median_micro_usd(values):
    values = sorted(values)
    middle = len(values) // 2
    if len(values) % 2:
        return values[middle]
    quotient, remainder = divmod(values[middle - 1] + values[middle], 2)
    return quotient + (remainder and quotient % 2)


def seconds(value):
    return "—" if value is None else f"{value / 1000:,.3f}"


def number(value):
    return "—" if value is None else f"{value:,.2f}"


def table(headers, rows):
    return "\n".join([
        "| " + " | ".join(headers) + " |",
        "| " + " | ".join("---" for _ in headers) + " |",
        *("| " + " | ".join(map(str, row)) + " |" for row in rows),
    ])


def tables(data, ko=False):
    language = int(ko)
    output = {}
    output["gateway"] = table(
        ["API", "동시 요청" if ko else "Concurrency", "성공/호출" if ko else "Success/calls",
         "E2E p50 (ms)", "E2E p95 (ms)", "TTFT p50 (ms)", "성공 req/s" if ko else "Success req/s"],
        [[
            "InvokeModel" if r["operation"] == "invoke" else "InvokeModelWithResponseStream",
            r["concurrency"], f'{r["successes"]}/{r["n"]}',
            number(r["p50_ms"]), number(r["p95_ms"]), number(r["ttft_p50_ms"]),
            number(r["success_per_second"]),
        ] for r in data["gateway"]],
    )
    output["concurrency"] = table(
        ["모델" if ko else "Model", "동시 요청" if ko else "Concurrency",
         "성공/호출" if ko else "Success/calls", "응답 실패율" if ko else "Response failure rate",
         "배치 시간 (s)" if ko else "Batch time (s)", "성공 출력 tok/s" if ko else "Successful output tok/s"],
        [[b["model"], b["workers"], f'{b["successes"]}/{b["requests"]}',
          f'{(b["requests"] - b["successes"]) / b["requests"]:.1%}',
          number(b["elapsed_seconds"]), number(b["aggregate_successful_output_tps"])]
         for b in data["concurrency"]],
    )
    cache_rows = []
    for model in data["models"]:
        records = [
            r for r in data["records"] if r["route"] == "mayu"
            and r["model_id"] == model["id"] and r["phase"].startswith("cache-")
        ]
        writes = [r for r in records if r["usage"].get("write", 0) > 0]
        reads = [r for r in records if r["usage"].get("read", 0) > 0]
        if len(writes) != 1 or not reads:
            continue
        first = writes[0]
        cache_rows.append([
            model["name"], f'{first["usage"]["write"]:,}', f'1 / {len(reads)}',
            money(first["estimated_cost_micro_usd"]),
            money(median_micro_usd(r["estimated_cost_micro_usd"] for r in reads)),
            seconds(first["ttft_ms"]), seconds(median(r["ttft_ms"] for r in reads)),
        ])
    output["cache"] = table(
        ["모델" if ko else "Model", "캐시 토큰" if ko else "Cached tokens",
         "쓰기/읽기 호출" if ko else "Write/read calls",
         "쓰기 요청 비용" if ko else "Write request cost",
         "읽기 요청 비용 중앙값" if ko else "Median read request cost",
         "쓰기 TTFT (s)" if ko else "Write TTFT (s)",
         "읽기 TTFT 중앙값 (s)" if ko else "Median read TTFT (s)"],
        cache_rows,
    )
    context_rows, generation_rows = [], []
    for model in data["models"]:
        r = model["largest_retrieval"]
        context_rows.append([
            model["name"], f'{model["context_window"]:,}',
            f'{r["observed_input_tokens"]:,}' if r else "—",
            f'{r["context_occupancy"]:.2%}' if r else "—",
            r.get("input_style", "—") if r else "—",
            seconds(r.get("ttft_ms")) if r else "—",
            money(r.get("estimated_cost_micro_usd")) if r else "—",
        ])
        cells = [model["name"]]
        for phase in ("generation-short", "generation-98"):
            g = model["generation"][phase]
            if not g:
                cells.extend(["—", "—", "호출 없음" if ko else "Not invoked"])
            elif not g.get("success"):
                cells.extend(["—", "—", FAILURES[g["failure_class"]][language]])
            else:
                cells.extend([
                    seconds(g.get("ttft_ms")),
                    number(g.get("billed_output_tokens_per_second")),
                    money(g.get("estimated_cost_micro_usd")),
                ])
        generation_rows.append(cells)
    output["context"] = table(
        ["모델" if ko else "Model", "선언 창 (tok)" if ko else "Declared window (tok)",
         "최대 회수 입력 (tok)" if ko else "Largest retrieved input (tok)",
         "점유율" if ko else "Occupancy", "입력군" if ko else "Input",
         "TTFT (s)", "요청 비용" if ko else "Request cost"], context_rows,
    )
    output["generation"] = table(
        ["모델" if ko else "Model", "짧은 TTFT (s)" if ko else "Short TTFT (s)",
         "짧은 tok/s" if ko else "Short tok/s", "짧은 비용/결과" if ko else "Short cost/result",
         "긴 TTFT (s)" if ko else "Long TTFT (s)", "긴 tok/s" if ko else "Long tok/s",
         "긴 비용/결과" if ko else "Long cost/result"], generation_rows,
    )
    phase_labels = [
        ("varied-25", "25% varied retrieval", "25% varied 회수"),
        ("varied-75", "75% varied retrieval", "75% varied 회수"),
        ("varied-99", "99% varied retrieval", "99% varied 회수"),
        ("generation-short", "Short generation", "짧은 입력 생성"),
        ("generation-98", "98% generation", "98% 입력 생성"),
        ("context-limit-102", "102% limit probe", "102% 한계 초과"),
    ]
    phase_rows = []
    for phase, en, kr in phase_labels:
        rows = [
            r for r in data["records"] if r["route"] == "direct"
            and r["phase"] == phase and r.get("failure_class") != "budget_skipped"
        ]
        successes = sum(bool(r.get("success")) for r in rows)
        failures = Counter(r["failure_class"] for r in rows if not r.get("success"))
        phase_rows.append([
            kr if ko else en, len(rows), successes,
            f"{(len(rows) - successes) / len(rows):.1%}",
            "; ".join(f"{FAILURES[key][language]} {count}" for key, count in sorted(failures.items())),
        ])
    output["failure"] = table(
        ["과제" if ko else "Task", "호출" if ko else "Calls",
         "성공" if ko else "Successes", "과제 실패율" if ko else "Task failure rate",
         "실패 분류" if ko else "Failure classes"], phase_rows,
    )
    native = []
    names = {m["id"]: m["name"] for m in data["models"]}
    for row in data["native_ui"]:
        r = row["result"]
        native.append([
            names.get(row["model"], row["model"]), "Converse · UI", "1/1",
            seconds(r["duration_ms"]), "—", money(r["cost"]["amount_usd_micros"]),
        ])
    for r in data["native_streams"]:
        native.append([
            names.get(r["model"], r["model"]), "ConverseStream · SDK", "1/1",
            seconds(r["duration_ms"]), seconds(r["ttft_ms"]), money(r["cost_micro_usd"]),
        ])
    output["native"] = table(
        ["모델" if ko else "Model", "API", "완료/호출" if ko else "Completed/calls",
         "E2E (s)", "TTFT (s)", "요청 비용" if ko else "Request cost"], native,
    )
    return output


def validate_snapshot(data):
    """Reject accidental raw-payload additions before publishing the snapshot."""
    allowed = {"schema", "as_of", "measurement_dates", "sources", "cost_basis",
               "models", "records", "gateway", "concurrency", "native_ui", "native_streams"}
    if set(data) != allowed:
        raise ValueError("Unexpected public snapshot fields")
    records = list(data["records"])
    for model in data["models"]:
        if set(model) - set(MODEL_FIELDS) - {"largest_retrieval", "generation"}:
            raise ValueError("Unexpected public model fields")
        if model["largest_retrieval"]:
            records.append(model["largest_retrieval"])
        records.extend(r for r in model["generation"].values() if r)
    if any(project_record(r) != r for r in records):
        raise ValueError("Unexpected public request fields")
    if any(project_native_ui(r) != r for r in data["native_ui"]):
        raise ValueError("Unexpected public UI fields")
    if any(project_native_stream(r) != r for r in data["native_streams"]):
        raise ValueError("Unexpected public stream fields")


def generated_files(data=None):
    if data is None:
        data = read("docs/assets/data/performance.json")
    validate_snapshot(data)
    result = {
        ROOT / "docs/assets/data/performance.json":
            json.dumps(data, ensure_ascii=False, indent=2, allow_nan=False) + "\n",
    }
    for ko in (False, True):
        generated = tables(data, ko)
        suffix = ".ko.md" if ko else ".md"
        for name in ("index", "models"):
            path = ROOT / f"docs/performance/{name}{suffix}"
            text = path.read_text(encoding="utf-8")
            for key, content in generated.items():
                pattern = rf"(<!-- performance:{key} -->\n).*?(<!-- /performance:{key} -->)"
                text = re.sub(pattern, lambda m: m[1] + content + "\n" + m[2], text, flags=re.S)
            result[path] = text
    # Generated English tables and their reviewed Korean equivalents stay paired.
    for name in ("index", "models"):
        en = ROOT / f"docs/performance/{name}.md"
        ko = en.with_suffix(".ko.md")
        digest = hashlib.sha256(result[en].encode()).hexdigest()
        result[ko] = re.sub(
            r"(?m)^translation_source_sha256:.*$",
            "translation_source_sha256: " + digest, result[ko],
        )
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="Refuse stale generated tables/data.")
    mode.add_argument("--import-recordings", action="store_true", help="Explicitly refresh public data from local, private recordings.")
    args = parser.parse_args()
    stale = []
    data = import_recordings() if args.import_recordings else None
    for path, content in generated_files(data).items():
        if not path.exists() or path.read_text(encoding="utf-8") != content:
            stale.append(path.relative_to(ROOT).as_posix())
            if not args.check:
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(content, encoding="utf-8")
    if args.check and stale:
        raise SystemExit("Stale performance data: " + ", ".join(stale))
    print("Performance data verified." if args.check else f"Updated {len(stale)} performance files.")


if __name__ == "__main__":
    main()
