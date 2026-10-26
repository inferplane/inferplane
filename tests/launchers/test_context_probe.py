"""Cost limits and error evidence for synthetic live context probes."""

from decimal import Decimal
from pathlib import Path
import runpy
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]


class ContextProbeTests(unittest.TestCase):
    def setUp(self):
        self.probe = runpy.run_path(str(ROOT / "scripts/inferplane-context-probe"))

    def test_reservation_includes_derived_cache_write_rates(self):
        rate = self.probe["rates"]({"input_per_mtok": Decimal("2"), "output_per_mtok": Decimal("6")})
        self.assertEqual(rate["input"], 2000000)
        self.assertEqual(rate["write"], 4000000)
        self.assertEqual(self.probe["reserve_micros"](1000000, 128, rate), 4000768)

    def test_partial_or_invalid_usage_never_releases_reservation(self):
        rate = self.probe["rates"]({"input_per_mtok": 2, "output_per_mtok": 6})
        for usage in [None, {}, {"input_tokens": 1},
                      {"input_tokens": True, "output_tokens": 2},
                      {"input_tokens": 1, "output_tokens": 2, "input_tokens_details": {"cached_tokens": 2}}]:
            with self.subTest(usage=usage):
                self.assertIsNone(self.probe["usage_cost"](usage, rate, 128))

    def test_usage_cost_disjoins_cache_and_includes_output(self):
        rate = self.probe["rates"]({"input_per_mtok": 2, "output_per_mtok": 6,
                                    "cache_read_per_mtok": Decimal("0.5")})
        self.assertEqual(self.probe["usage_cost"]({
            "input_tokens": 1000, "output_tokens": 2,
            "input_tokens_details": {"cached_tokens": 600, "cache_write_tokens": 100},
        }, rate, 128), 1312)

    def test_context_pricing_bounds_and_cost_use_the_same_tiers_as_gateway(self):
        rate = self.probe["rates"]({"input_per_mtok":2, "output_per_mtok":10, "cache_read_per_mtok":Decimal('.1'),
                                    "short_context_limit":272000,
                                    "long_context":{"input_per_mtok":4, "output_per_mtok":15, "cache_read_per_mtok":Decimal('.2')}})
        # Upper bound includes the more expensive long output/cache prices.
        self.assertEqual(self.probe["reserve_micros"](1000000,128,rate),8001920)
        for total, want in [(272000,31020),(272001,62034)]:
            usage={"input_tokens":total,"output_tokens":2,"input_tokens_details":{"cached_tokens":270000}}
            self.assertEqual(self.probe["usage_cost"](usage,rate,128),want)

    def test_errors_without_context_code_are_not_context_limits(self):
        for status in [400, 502, 503]:
            self.assertEqual(self.probe["classify"](status, {"error": {"type": "upstream_error"}}),
                             "rejected_unclassified")
        self.assertEqual(self.probe["classify"](429, {}), "rate_or_quota_refusal")
        self.assertEqual(self.probe["classify"](400, {
            "error": {"code": "context_length_exceeded"},
        }), "context_refusal")

    def test_proxy_and_redirect_destinations_cannot_receive_key(self):
        self.assertIsNone(self.probe["NoRedirect"]().redirect_request(None, None, 302, "", {}, "http://elsewhere/"))

    def test_native_probe_does_not_invent_reasoning_effort(self):
        native = self.probe["payload"]("m", 100, "native", 128)
        self.assertNotIn("reasoning", native)
        bridge = self.probe["payload"]("m", 100, "bridge", 128)
        self.assertEqual(bridge["reasoning"], {"effort": "none"})

    def test_probe_qualifies_only_declared_native_target_without_fallback(self):
        class Response:
            status = 200
            def __init__(self, model, headers):
                self.model, self.headers = model, headers
            def __enter__(self):
                return self
            def __exit__(self, *args):
                pass
            def read(self, limit):
                import json
                return json.dumps({"model": self.model, "usage": {
                    "input_tokens": 10, "output_tokens": 2}}).encode()
        class Opener:
            def __init__(self, response):
                self.response = response
            def open(self, request, timeout):
                return self.response
        rate = self.probe["rates"]({"input_per_mtok": 2, "output_per_mtok": 6})
        cases = [
            ("upstream-native", {}, "accepted"),
            ("unexpected-native", {}, "substituted_unqualified"),
            ("upstream-native", {"x-inferplane-fallback": "other"}, "substituted_unqualified"),
            ("upstream-native", {"x-inferplane-model-fallback": "other"}, "substituted_unqualified"),
            ("upstream-native", {"x-inferplane-routed-model": "other"}, "substituted_unqualified"),
        ]
        for model, headers, expected in cases:
            with self.subTest(model=model, headers=headers):
                record = {"model": "public.alias", "reserved_micro_usd": 2000, "held_micro_usd": 2000}
                self.probe["probe"](Opener(Response(model, headers)), "offline-placeholder", b"{}",
                                    record, rate, 128, expected_model="upstream-native")
                self.assertEqual(record["state"], expected)
                self.assertNotIn("upstream-native", str(record))

    def test_local_port_override_has_no_remote_credential_destination(self):
        for port, expected in [("", "http://127.0.0.1:8080/v1"),
                               ("8081", "http://127.0.0.1:8081/v1")]:
            with self.subTest(port=port), patch.dict("os.environ", {"INFERPLANE_PORT": port}):
                self.assertEqual(self.probe["gateway_base_url"](), expected)
        for port in ["0", "65536", "elsewhere", "8080/path", "8080@elsewhere"]:
            with self.subTest(port=port), patch.dict("os.environ", {"INFERPLANE_PORT": port}):
                with self.assertRaises(ValueError):
                    self.probe["gateway_base_url"]()

    def test_unknown_errors_and_missing_usage_hold_the_full_reservation(self):
        class Response:
            headers = {}

            def __init__(self, status, raw):
                self.status, self.raw = status, raw

            def __enter__(self):
                return self

            def __exit__(self, *args):
                pass

            def read(self, limit):
                return self.raw

        class Opener:
            calls = 0

            def __init__(self, response):
                self.response = response

            def open(self, request, timeout):
                self.calls += 1
                return self.response

        rate = self.probe["rates"]({"input_per_mtok": 2, "output_per_mtok": 6})
        for status, raw, expected in [
            (400, b'{"error":{"message":"private-detail"}}', "rejected_unclassified"),
            (200, b'{"model":"m","usage":{"input_tokens":1}}', "usage_uncertain"),
        ]:
            with self.subTest(status=status):
                record = {"model": "m", "reserved_micro_usd": 2000, "held_micro_usd": 2000}
                opener = Opener(Response(status, raw))
                self.probe["probe"](opener, "offline-placeholder", b'{}', record, rate, 128)
                self.assertEqual(record["state"], expected)
                self.assertEqual(record["held_micro_usd"], 2000)
                self.assertEqual(opener.calls, 1)
                self.assertNotIn("private-detail", str(record))


if __name__ == "__main__":
    unittest.main()
