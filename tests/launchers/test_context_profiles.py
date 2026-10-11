"""Operator context profiles change client headroom, never gateway model bounds."""

import copy
import json
import os
from pathlib import Path
import runpy
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]


class ContextProfileTests(unittest.TestCase):
    def setUp(self):
        self.launcher = runpy.run_path(str(ROOT / "scripts/inferplane-codex"))
        self.entries = [{"id": "test.model", "context_window": 1000000}]
        self.profile = {"test.model": {
            "context_window": 1000000, "usable_percent": 95, "compact_percent": 90,
        }}

    def catalog(self, profiles):
        return self.launcher["build_catalog"](
            self.entries, context_profiles=profiles
        )["models"][0]

    def test_profile_changes_headroom_and_compaction_without_raising_cap(self):
        model = self.catalog(self.profile)
        self.assertEqual(model["context_window"], 1000000)
        self.assertEqual(model["max_context_window"], 1000000)
        self.assertEqual(model["effective_context_window_percent"], 95)
        self.assertEqual(model["auto_compact_token_limit"], 900000)

    def test_other_models_keep_existing_defaults(self):
        model = self.catalog({"other.model": self.profile["test.model"]})
        self.assertEqual(model["effective_context_window_percent"], 80)
        self.assertEqual(model["auto_compact_token_limit"], 800000)

    def test_changed_gateway_bound_requires_requalification(self):
        for bound in [524288, 2000000]:
            with self.subTest(bound=bound):
                self.entries[0]["context_window"] = bound
                with self.assertRaises(self.launcher["LauncherError"]):
                    self.catalog(self.profile)

    def test_missing_gateway_bound_cannot_be_qualified_by_a_profile(self):
        self.entries[0].pop("context_window")
        self.profile["test.model"]["context_window"] = 16384
        with self.assertRaises(self.launcher["LauncherError"]):
            self.catalog(self.profile)

    def test_native_features_are_preserved(self):
        self.entries[0].update(responses_mode="native", codex_model="native")
        self.launcher["build_catalog"].__globals__["bundled_models"] = lambda: {
            "native": {"slug": "native", "effective_context_window_percent": 95,
                       "use_responses_lite": True, "input_modalities": ["text", "image"]},
        }
        model = self.catalog(self.profile)
        self.assertEqual(model["effective_context_window_percent"], 95)
        self.assertEqual(model["auto_compact_token_limit"], 900000)
        self.assertTrue(model["use_responses_lite"])
        self.assertEqual(model["input_modalities"], ["text", "image"])

    def load(self, payload, raw=None):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "profiles.json"
            path.write_text(raw if raw is not None else json.dumps(payload))
            return self.launcher["load_context_profiles"](str(path))

    def test_profile_file_schema(self):
        self.assertEqual(self.load({"schema_version": 1, "models": self.profile}), self.profile)

    def test_invalid_profiles_fail_before_launch(self):
        payload = {"schema_version": 1, "models": self.profile}
        for field, value in [
            ("context_window", True), ("context_window", 0),
            ("usable_percent", True), ("usable_percent", 0), ("usable_percent", 101),
            ("compact_percent", 0), ("compact_percent", 95), ("compact_percent", 100),
            ("unknown", 1),
        ]:
            with self.subTest(field=field, value=value):
                bad = copy.deepcopy(payload)
                bad["models"]["test.model"][field] = value
                with self.assertRaises(self.launcher["LauncherError"]):
                    self.load(bad)

    def test_duplicate_members_and_wrong_version_are_refused(self):
        for raw in [
            '{"schema_version":1,"schema_version":2,"models":{}}',
            '{"schema_version":1,"models":{"m":{},"m":{}}}',
            '{"schema_version":true,"models":{}}',
            '{"schema_version":2,"models":{}}',
        ]:
            with self.subTest(raw=raw):
                with self.assertRaises(self.launcher["LauncherError"]):
                    self.load(None, raw)


@unittest.skipUnless(os.environ.get("INFERPLANE_TEST_CODEX"), "installed Codex opt-in required")
class InstalledContextProfileTests(unittest.TestCase):
    def test_profiles_change_runtime_windows_when_switching_models(self):
        tests = runpy.run_path(str(ROOT / "tests/launchers/test_inferplane_codex.py"))
        fixture = tests["LauncherTests"]()
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        cli = fixture.directory / "codex"
        cli.unlink()
        cli.symlink_to(Path(os.environ["INFERPLANE_TEST_CODEX"]).resolve())
        home = Path(fixture.env["CODEX_HOME"])
        home.mkdir()
        (home / "config.toml").write_text("model_context_window=1048576\n")
        fixture.payload = {"data": [{
            "id": name, "context_window": bound, "responses_mode": "bridge", "capabilities": ["tools"],
        } for name, bound in [("global.xai.grok-4.7", 500000), ("qwen.qwen3-coder-next", 256000)]]}
        fixture.env["INFERPLANE_CODEX_CONTEXT_PROFILES"] = str(ROOT / "examples/codex/context-profiles.json")
        client = tests["AppServerClient"](fixture, "global.xai.grok-4.7")
        self.addCleanup(client.close)
        client.initialize()
        thread = client.call("thread/start", {"cwd": str(fixture.directory), "ephemeral": True, "sandbox": "read-only"})
        thread_id = thread["thread"]["id"]
        first = client.turn(thread_id, "Reply offline without tools.")
        second = client.turn(thread_id, "Reply offline without tools.", model="qwen.qwen3-coder-next")
        self.assertEqual(first["modelContextWindow"], 425000)
        self.assertEqual(second["modelContextWindow"], 243200)


if __name__ == "__main__":
    unittest.main()
