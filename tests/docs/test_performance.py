"""Public measurement evidence must stay bounded and preserve denominators."""

from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "scripts"))
import build_performance_docs as performance


class PerformanceEvidenceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.data = performance.read("docs/assets/data/performance.json")

    def test_public_projection_excludes_payloads_and_unknown_usage_fields(self):
        row = performance.project_record({
            "model_id": "test", "success": False,
            "output_text": "PRIVATE", "error": "PRIVATE", "prompt": "PRIVATE",
            "usage": {"input": 7, "output": 0, "debug": "PRIVATE"},
            "authorization": "PRIVATE",
        })
        self.assertEqual(row, {
            "model_id": "test", "success": False, "usage": {"input": 7, "output": 0},
        })

    def test_native_projection_does_not_copy_new_payload_fields(self):
        row = performance.read("docs/assets/data/performance.json")["native_ui"][0]
        row["result"]["answer"] = "PRIVATE"
        row["result"]["cost"]["debug"] = "PRIVATE"
        row["result"]["usage"]["prompt"] = "PRIVATE"
        self.assertNotIn("PRIVATE", str(performance.project_native_ui(row)))
        stream = performance.read("docs/assets/data/performance.json")["native_streams"][0]
        stream["events"] = ["PRIVATE"]
        stream["usage"]["debug"] = "PRIVATE"
        self.assertNotIn("PRIVATE", str(performance.project_native_stream(stream)))

    def test_context_success_does_not_include_over_window_or_failed_tasks(self):
        models = self.data["models"]
        self.assertEqual(len(models), 49)
        observed = [m["largest_retrieval"] for m in models if m["largest_retrieval"]]
        self.assertEqual(len(observed), 42)
        self.assertEqual(sum(r["context_occupancy"] >= .95 for r in observed), 26)
        for r in observed:
            self.assertTrue(r["success"] and r["retrieval_correct"])
            self.assertLessEqual(r["context_occupancy"], 1)

    def test_snapshot_rejects_raw_payload_added_to_committed_data(self):
        import copy
        data = copy.deepcopy(self.data)
        performance.validate_snapshot(data)
        data["models"][0]["generation"]["generation-short"] = {
            "model_id": "test", "output_text": "PRIVATE", "usage": {},
        }
        with self.assertRaisesRegex(ValueError, "request fields"):
            performance.validate_snapshot(data)

    def test_calls_exclude_noninvoked_records_and_other_gateway(self):
        records = self.data["records"]
        self.assertEqual({r["route"] for r in records}, {"direct", "mayu"})
        limit = [r for r in records if r["phase"] == "context-limit-102"]
        self.assertEqual(len(limit), 42)
        invoked = [r for r in limit if r["failure_class"] != "budget_skipped"]
        self.assertEqual(len(invoked), 38)
        self.assertEqual(sum(r["success"] for r in invoked), 10)

    def test_batch_throughput_includes_failed_request_wall_time(self):
        for batch in self.data["concurrency"]:
            self.assertEqual(batch["requests"], 8)
            self.assertAlmostEqual(
                batch["aggregate_successful_output_tps"],
                batch["successful_output_tokens"] / batch["elapsed_seconds"],
            )
        self.assertTrue(any(b["successes"] < b["requests"] for b in self.data["concurrency"]))

    def test_microdollar_format_is_exact_and_unknown_is_not_free(self):
        self.assertEqual(performance.money(9_007_199_254_740_993), "$9,007,199,254.740993")
        self.assertEqual(performance.money(0), "$0.000000")
        self.assertEqual(performance.money(None), "—")
        self.assertEqual(performance.median_micro_usd([2, 3]), 2)
        self.assertEqual(performance.median_micro_usd([3, 4]), 4)
        self.assertEqual(performance.median_micro_usd([9_007_199_254_740_992, 9_007_199_254_740_995]), 9_007_199_254_740_994)


if __name__ == "__main__":
    unittest.main()
