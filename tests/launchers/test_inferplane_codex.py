"""Offline launcher contract tests: fake model server and a recording Codex CLI."""

import copy
import json
import os
from pathlib import Path
import queue
import signal
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


ROOT = Path(__file__).resolve().parents[2]
LAUNCHER = ROOT / "scripts" / "inferplane-codex"
MODELS = {
    "object": "list",
    "data": [
        {"id": "global.openai.gpt-6-astra", "context_window": 1000000},
        {"id": "test.grok", "context_window": 524288, "max_model_len": 524288},
        {"id": "test.alias", "max_model_len": 32768},
        {"id": "openai.gpt-6-astra", "context_window": 1000000},
    ],
}
FAKE_CODEX = """#!{python}
import json, os, pathlib, signal, stat, sys, time
args = sys.argv[1:]
if args == ['debug', 'models', '--bundled']:
    print(pathlib.Path(os.environ['FAKE_BUNDLED']).read_text())
    sys.exit(0)
config = {{}}
for index, arg in enumerate(args):
    if arg == '--':
        break
    if arg == '-c':
        key, value = args[index + 1].split('=', 1)
        config[key] = value
catalog_path = pathlib.Path(json.loads(config['model_catalog_json']))
record = {{
    'pid': os.getpid(),
    'args': args,
    'config': config,
    'catalog': json.loads(catalog_path.read_text()),
    'catalog_path': str(catalog_path),
    'file_mode': stat.S_IMODE(catalog_path.stat().st_mode),
    'dir_mode': stat.S_IMODE(catalog_path.parent.stat().st_mode),
    'key_present': os.environ.get('INFERPLANE_API_KEY') == 'offline-placeholder',
    'stdin': '' if os.environ.get('FAKE_WAIT') else sys.stdin.read(),
    'proxy_env': {{key: os.environ.get(key) for key in (
        'HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'http_proxy', 'https_proxy', 'all_proxy',
        'NO_PROXY', 'no_proxy',
    )}},
}}
if os.environ.get('FAKE_HANDLE_SIGINT'):
    count = 0
    def interrupt(signum, frame):
        global count
        count += 1
        pathlib.Path(os.environ['FAKE_RECORD'] + '.signals').write_text(str(count))
    signal.signal(signal.SIGINT, interrupt)
pathlib.Path(os.environ['FAKE_RECORD']).write_text(json.dumps(record))
if os.environ.get('FAKE_WAIT'):
    time.sleep(30)
sys.exit(int(os.environ.get('FAKE_EXIT', '0')))
"""
# Change only the in-process endpoint for tests; the production helper has no
# user-controlled remote endpoint or credential loading mechanism.
BOOTSTRAP = """
import os, runpy, sys
parent_env = dict(os.environ)
module = runpy.run_path(sys.argv[1], run_name='launcher_under_test')
main = module['main']
main.__globals__['BASE_URL'] = sys.argv[2]
status = main(sys.argv[3:])
assert dict(os.environ) == parent_env, 'launcher mutated its parent environment'
sys.exit(status)
"""


class AppServerClient:
    """Drive the installed app server through the launcher using JSONL RPC."""

    def __init__(self, fixture, model):
        self.stderr = tempfile.TemporaryFile(mode="w+")
        model_args = ["-c", "model=" + json.dumps(model)] if model is not None else []
        self.process = subprocess.Popen(
            fixture.command("app-server", "--stdio", *model_args),
            env=fixture.env, cwd=fixture.directory, stdin=subprocess.PIPE,
            stdout=subprocess.PIPE, stderr=self.stderr, text=True, bufsize=1,
        )
        self.messages = queue.Queue()
        self.events = []
        self.request_id = 0

        def read():
            try:
                for line in self.process.stdout:
                    self.messages.put(json.loads(line))
            finally:
                self.messages.put(None)

        self.reader = threading.Thread(target=read, daemon=True)
        self.reader.start()

    def send(self, method, params, request_id=None):
        message = {"method": method, "params": params}
        if request_id is not None:
            message["id"] = request_id
        self.process.stdin.write(json.dumps(message) + "\n")
        self.process.stdin.flush()

    def wait(self, predicate):
        deadline = time.monotonic() + 15
        while True:
            message = self.messages.get(timeout=max(0, deadline - time.monotonic()))
            if message is None:
                self.stderr.seek(0)
                raise AssertionError("app server exited: " + self.stderr.read()[-1200:])
            self.events.append(message)
            if predicate(message):
                return message

    def call(self, method, params):
        self.request_id += 1
        request_id = self.request_id
        self.send(method, params, request_id)
        reply = self.wait(lambda message: message.get("id") == request_id)
        if "error" in reply:
            raise AssertionError("RPC error: " + json.dumps(reply["error"]))
        return reply["result"]

    def initialize(self):
        self.call("initialize", {
            "clientInfo": {"name": "inferplane_offline_tests", "version": "1"},
            "capabilities": {"experimentalApi": True},
        })
        self.send("initialized", {})

    def turn(self, thread_id, text, model=None):
        params = {"threadId": thread_id, "input": [{"type": "text", "text": text}]}
        if model is not None:
            params["model"] = model
        turn_id = self.call("turn/start", params)["turn"]["id"]
        self.wait(lambda event: (
            event.get("method") == "turn/completed"
            and event["params"]["turn"]["id"] == turn_id
        ))
        usage = [
            event["params"]["tokenUsage"] for event in self.events
            if event.get("method") == "thread/tokenUsage/updated"
            and event["params"]["turnId"] == turn_id
        ]
        if not usage:
            raise AssertionError("Codex did not report its effective model context window")
        return usage[-1]

    def close(self):
        if self.process.poll() is None:
            self.process.terminate()
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=5)
        self.reader.join(timeout=5)
        self.process.stdin.close()
        self.process.stdout.close()
        self.stderr.close()


class LauncherTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="inferplane-launcher-test-")
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.record_path = self.directory / "record.json"
        self.payload = copy.deepcopy(MODELS)
        self.status = 200
        self.requests = []
        self.response_requests = []
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                owner.requests.append((self.path, self.headers.get("Authorization")))
                self.send_response(owner.status)
                if owner.status == 302:
                    self.send_header("Location", "/redirect-target")
                self.end_headers()
                body = owner.payload
                if not isinstance(body, bytes):
                    body = json.dumps(body).encode()
                self.wfile.write(body)

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                owner.response_requests.append(
                    (self.path, self.headers.get("Authorization"), body)
                )
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                sequence = len(owner.response_requests)
                item = {
                    "id": "msg_offline_{}".format(sequence), "type": "message", "role": "assistant",
                    "content": [{"type": "output_text", "text": "offline ok"}],
                }
                events = [
                    {"type": "response.output_item.added", "output_index": 0, "item": item},
                    {"type": "response.output_item.done", "output_index": 0, "item": item},
                    {"type": "response.completed", "response": {
                        "id": "resp_offline_{}".format(sequence),
                        "status": "completed", "output": [item],
                        "usage": {"input_tokens": 5, "output_tokens": 2, "total_tokens": 7},
                    }},
                ]
                for event in events:
                    self.wfile.write(("data: " + json.dumps(event) + "\n\n").encode())

            def log_message(self, *args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(
            target=lambda: self.server.serve_forever(poll_interval=0.01), daemon=True
        )
        self.thread.start()
        self.addCleanup(self.stop_server)
        self.base_url = "http://127.0.0.1:{}/v1".format(self.server.server_port)
        cli = self.directory / "codex"
        cli.write_text(FAKE_CODEX.format(python=sys.executable))
        cli.chmod(0o700)
        self.env = {
            "PATH": str(self.directory),
            "HOME": str(self.directory),
            "CODEX_HOME": str(self.directory / "codex-home"),
            "TMPDIR": str(self.directory),
            "INFERPLANE_API_KEY": "offline-placeholder",
            "FAKE_RECORD": str(self.record_path),
        }

    def stop_server(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def command(self, *args):
        return [sys.executable, "-c", BOOTSTRAP, str(LAUNCHER), self.base_url, *args]

    def run_launcher(self, *args, stdin=""):
        return subprocess.run(
            self.command(*args), input=stdin, capture_output=True, text=True,
            env=self.env, cwd=self.directory, timeout=8,
        )

    def record(self):
        return json.loads(self.record_path.read_text())

    def assert_refused(self, result):
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.record_path.exists(), "Codex launched after failed admission")
        self.assertNotIn("offline-placeholder", result.stdout + result.stderr)
        self.assertEqual(list(self.directory.glob("inferplane-codex-*")), [])

    def test_authenticated_catalog_gives_each_model_its_own_bound(self):
        result = self.run_launcher("-m", "test.grok")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.requests, [("/v1/models", "Bearer offline-placeholder")])
        record = self.record()
        catalog = {m["slug"]: m for m in record["catalog"]["models"]}
        self.assertEqual(set(catalog), {
            "global.openai.gpt-6-astra", "openai.gpt-6-astra", "test.grok", "test.alias",
        })
        for name, context in [
            ("global.openai.gpt-6-astra", 1000000), ("openai.gpt-6-astra", 1000000),
            ("test.grok", 524288), ("test.alias", 32768),
        ]:
            model = catalog[name]
            self.assertEqual(model["context_window"], context)
            # Without this cap Codex's existing config can reapply Astra's 1M.
            self.assertEqual(model["max_context_window"], context)
            self.assertLessEqual(model["auto_compact_token_limit"], context * 9 // 10)
            self.assertEqual(model["visibility"], "list")
            self.assertTrue(model["supported_in_api"])
        self.assertNotIn("model_context_window", record["config"])
        self.assertEqual(record["file_mode"], 0o600)
        self.assertEqual(record["dir_mode"], 0o700)
        self.assertFalse(Path(record["catalog_path"]).parent.exists())
        self.assertTrue(record["key_present"])
        self.assertNotIn("offline-placeholder", json.dumps(record))

    def test_portable_catalog_does_not_advertise_native_reasoning_or_state(self):
        result = self.run_launcher()
        self.assertEqual(result.returncode, 0, result.stderr)
        record = self.record()
        self.assertEqual(json.loads(record["config"]["model"]), "global.openai.gpt-6-astra")
        for model in record["catalog"]["models"]:
            self.assertFalse(model["use_responses_lite"])
            self.assertFalse(model["supports_experimental_context"])
            self.assertFalse(model["supports_reasoning_summary_parameter"])
            self.assertEqual(model["default_reasoning_level"], "none")
            self.assertEqual(
                [level["effort"] for level in model["supported_reasoning_levels"]], ["none"]
            )
            self.assertFalse(model["support_verbosity"])
            self.assertEqual(model["input_modalities"], ["text"])
            self.assertEqual(model["tool_mode"], "direct")
            self.assertNotIn("guardian", model)
        self.assertEqual(json.loads(record["config"]["model_reasoning_effort"]), "none")

    def test_missing_portable_default_does_not_silently_select_native_astra(self):
        self.payload["data"] = [
            model for model in self.payload["data"] if model["id"] != "global.openai.gpt-6-astra"
        ]
        self.assert_refused(self.run_launcher())

    def test_bridge_effort_override_follows_user_options_and_precedes_separator(self):
        self.payload["data"][1]["responses_mode"] = "bridge"
        args = [
            "-c", 'model_reasoning_effort="xhigh"',
            "exec", "-m", "test.grok", "-c", 'model_reasoning_effort="high"',
            "--", "a literal prompt",
        ]
        result = self.run_launcher(*args)
        self.assertEqual(result.returncode, 0, result.stderr)
        record = self.record()
        self.assertEqual(json.loads(record["config"]["model_reasoning_effort"]), "none")
        split = args.index("--")
        self.assertEqual(record["args"][:split], args[:split])
        self.assertIn('model_reasoning_effort="none"', record["args"][split:])
        self.assertEqual(record["args"][-2:], ["--", "a literal prompt"])

    def test_explicit_capabilities_filter_non_tools_and_keep_legacy_entries(self):
        self.payload["data"][0]["capabilities"] = []
        self.payload["data"][1]["capabilities"] = ["tools", "reasoning"]
        self.payload["data"].append({
            "id": "text.only", "capabilities": ["vision", "structured_output"],
            "context_window": 32768,
        })
        result = self.run_launcher("-m", "test.grok")
        self.assertEqual(result.returncode, 0, result.stderr)
        catalog = self.record()["catalog"]["models"]
        self.assertEqual(
            {model["slug"] for model in catalog}, {"test.grok", "test.alias", "openai.gpt-6-astra"}
        )
        self.assertEqual(next(m for m in catalog if m["slug"] == "test.grok")["max_context_window"], 524288)

    def test_explicit_non_tool_model_or_default_cannot_bypass_filter_with_model_flag(self):
        for capabilities in [[], ["reasoning"], ["vision", "structured_output"]]:
            with self.subTest(capabilities=capabilities):
                self.payload["data"][1]["capabilities"] = capabilities
                self.assert_refused(self.run_launcher("-m", "test.grok"))
        self.payload["data"][0]["capabilities"] = []
        self.payload["data"][1]["capabilities"] = ["tools"]
        self.assert_refused(self.run_launcher())

    def test_all_explicit_non_tool_models_are_refused(self):
        for entry in self.payload["data"]:
            entry["capabilities"] = []
        self.assert_refused(self.run_launcher())

    def test_invalid_capability_metadata_is_not_treated_as_legacy_metadata(self):
        for capabilities in [None, "tools", {"tools": True}, [True], ["tools", 42]]:
            with self.subTest(capabilities=capabilities):
                self.payload["data"][0]["capabilities"] = capabilities
                self.assert_refused(self.run_launcher())

    def test_excluded_native_model_does_not_need_codex_template_lookup(self):
        self.payload["data"][0].update({
            "capabilities": [], "responses_mode": "native", "codex_model": "unavailable",
        })
        self.payload["data"][1]["capabilities"] = ["tools"]
        result = self.run_launcher("-m", "test.grok")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            {m["slug"] for m in self.record()["catalog"]["models"]},
            {"test.grok", "test.alias", "openai.gpt-6-astra"},
        )

    def test_unsupported_routes_are_skipped_even_with_tools_or_legacy_capabilities(self):
        for capabilities in [None, ["tools"]]:
            with self.subTest(capabilities=capabilities):
                self.payload = copy.deepcopy(MODELS)
                unsupported = {
                    "id": "unsupported.route", "responses_mode": "unsupported",
                }
                if capabilities is not None:
                    unsupported["capabilities"] = capabilities
                self.payload["data"].append(unsupported)
                result = self.run_launcher("-m", "test.grok")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(
                    {m["slug"] for m in self.record()["catalog"]["models"]},
                    {"global.openai.gpt-6-astra", "openai.gpt-6-astra", "test.grok", "test.alias"},
                )
                # An unsupported route needs neither a context fallback nor
                # native template metadata to be omitted from this catalog.
                self.assertEqual(result.stderr, "")
                self.record_path.unlink()

    def test_unsupported_selection_or_default_is_refused_without_replacement(self):
        self.payload["data"][0]["responses_mode"] = "unsupported"
        for args in [(), ("-m", "global.openai.gpt-6-astra")]:
            result = self.run_launcher(*args)
            self.assert_refused(result)
            self.assertIn("unsupported", result.stderr)
        for entry in self.payload["data"]:
            entry["responses_mode"] = "unsupported"
        self.assert_refused(self.run_launcher("-m", "test.grok"))

    def test_native_metadata_requires_explicit_route_and_exact_bundled_slug(self):
        self.payload["data"][3].update({
            "responses_mode": "native", "codex_model": "gpt-6-astra",
        })
        bundled = self.directory / "bundled.json"
        bundled.write_text(json.dumps({"models": [{
            "slug": "gpt-6-astra", "display_name": "Native fixture",
            "base_instructions": "Native model instructions.",
            "context_window": 272000, "max_context_window": 872000,
            "default_reasoning_level": "low",
            "supported_reasoning_levels": [
                {"effort": "low", "description": "Low"},
                {"effort": "xhigh", "description": "High"},
            ],
            "supports_reasoning_summary_parameter": True,
            "support_verbosity": True,
            "input_modalities": ["text", "image"],
            "use_responses_lite": True,
        }]}))
        self.env["FAKE_BUNDLED"] = str(bundled)
        result = self.run_launcher("-m", "openai.gpt-6-astra", "-c", 'model_reasoning_effort="xhigh"')
        self.assertEqual(result.returncode, 0, result.stderr)
        model = next(m for m in self.record()["catalog"]["models"] if m["slug"] == "openai.gpt-6-astra")
        self.assertEqual(model["slug"], "openai.gpt-6-astra")
        self.assertEqual(model["context_window"], 1000000)
        self.assertEqual(model["max_context_window"], 1000000)
        self.assertTrue(model["use_responses_lite"])
        self.assertTrue(model["support_verbosity"])
        self.assertEqual(model["default_reasoning_level"], "low")
        self.assertEqual(model["supported_reasoning_levels"][1]["effort"], "xhigh")
        self.assertEqual(json.loads(self.record()["config"]["model_reasoning_effort"]), "xhigh")
        self.assertNotIn('model_reasoning_effort="none"', self.record()["args"])
        self.record_path.unlink()
        for slug in ["new-native-model", "offline-placeholder"]:
            with self.subTest(slug=slug):
                alias = "openai." + slug
                self.payload["data"].append({
                    "id": alias, "responses_mode": "native", "codex_model": slug,
                    "context_window": 32768, "capabilities": ["tools"],
                })
                result = self.run_launcher("-m", "test.grok")
                self.assertEqual(result.returncode, 0, result.stderr)
                catalog = {m["slug"] for m in self.record()["catalog"]["models"]}
                self.assertIn("test.grok", catalog)
                self.assertIn("openai.gpt-6-astra", catalog)
                self.assertNotIn(alias, catalog)
                self.assertIn("skipping a native model", result.stderr)
                self.assertIn("codex_model is absent from the installed Codex catalog", result.stderr)
                self.assertNotIn("offline-placeholder", result.stderr)
                self.record_path.unlink()
                result = self.run_launcher("-m", alias)
                self.assert_refused(result)
                self.assertIn("selected model is unavailable", result.stderr)
                self.assertIn("unmapped native", result.stderr)
                self.payload["data"].pop()

    def test_invalid_native_capability_declarations_are_refused(self):
        for metadata in [
            {"responses_mode": "unrecognized"},
            {"responses_mode": "native"},
            {"responses_mode": "native", "codex_model": 42},
            {"responses_mode": "native", "codex_model": ""},
            {"responses_mode": "native", "codex_model": "invalid\nslug"},
            {"responses_mode": "native", "codex_model": "unmapped", "context_window": 0},
            {"responses_mode": "native", "codex_model": "unmapped", "capabilities": "tools"},
            {"responses_mode": "bridge", "codex_model": "gpt-6-astra"},
        ]:
            with self.subTest(metadata=metadata):
                self.payload = copy.deepcopy(MODELS)
                self.payload["data"][0].update(metadata)
                self.assert_refused(self.run_launcher())

    def test_malformed_bundled_metadata_is_not_treated_as_an_unmapped_model(self):
        self.payload["data"][3].update({
            "responses_mode": "native", "codex_model": "gpt-6-astra",
        })
        bundled = self.directory / "bundled.json"
        self.env["FAKE_BUNDLED"] = str(bundled)
        for data in [
            b"not JSON", {}, {"models": None}, {"models": [{}]},
            {"models": [{"slug": None}]}, {"models": [{"slug": "invalid\nslug"}]},
        ]:
            with self.subTest(data=data):
                bundled.write_bytes(data if isinstance(data, bytes) else json.dumps(data).encode())
                self.assert_refused(self.run_launcher("-m", "test.grok"))

    def test_subcommand_overrides_and_separator_preserve_args_stdin_and_permissions(self):
        args = [
            "-c", 'model_provider="openai"', "exec", "resume", "--last",
            "-c", 'model_provider="wrong"', "-c", "model_context_window=1000000",
            "--sandbox", "read-only", "--model=test.grok", "--",
            "a prompt with spaces\nand quotes '\"", "-m", "literal-not-a-model",
        ]
        result = self.run_launcher(*args, stdin="piped task\n한글\n")
        self.assertEqual(result.returncode, 0, result.stderr)
        record = self.record()
        split = args.index("--")
        actual = record["args"]
        self.assertEqual(actual[:split], args[:split])
        self.assertEqual(actual[actual.index("--"):], args[split:])
        self.assertEqual(record["stdin"], "piped task\n한글\n")
        self.assertEqual(json.loads(record["config"]["model"]), "test.grok")
        self.assertEqual(json.loads(record["config"]["model_provider"]), "inferplane")
        provider = record["config"]["model_providers.inferplane"]
        self.assertIn('wire_api="responses"', provider)
        self.assertIn('env_key="INFERPLANE_API_KEY"', provider)
        self.assertIn("requires_openai_auth=false", provider)
        self.assertIn("supports_websockets=false", provider)
        self.assertIn('base_url="' + self.base_url + '"', provider)
        appended = actual[split:actual.index("--")]
        self.assertFalse(any("approval" in a or "sandbox" in a for a in appended))

    def test_model_flag_forms_and_config_model_are_checked_without_consuming_option_values(self):
        for args in [
            ["-mtest.grok"],
            ["--model", "test.grok"],
            ["-m=test.grok"],
            ["-c", 'model="test.grok"'],
            ["--config=model='test.grok'"],
            ["-cmodel=test.grok"],
            ["-c", 'model="not-authorized"', "-m", "test.grok"],
            ["--output-last-message", "-mfilename", "--model=test.grok"],
        ]:
            with self.subTest(args=args):
                result = self.run_launcher(*args)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(json.loads(self.record()["config"]["model"]), "test.grok")
                self.record_path.unlink()

    def test_missing_context_uses_conservative_fallback_with_a_warning(self):
        self.payload["data"] = [{"id": "test.unknown"}]
        result = self.run_launcher("-m", "test.unknown")
        self.assertEqual(result.returncode, 0, result.stderr)
        model = self.record()["catalog"]["models"][0]
        self.assertEqual(model["context_window"], 16384)
        self.assertEqual(model["max_context_window"], 16384)
        self.assertIn("16384", result.stderr)
        self.assertIn("context", result.stderr)

    def test_disagreeing_context_aliases_use_the_smaller_bound(self):
        self.payload["data"][1]["max_model_len"] = 32768
        result = self.run_launcher("-m", "test.grok")
        self.assertEqual(result.returncode, 0, result.stderr)
        model = next(m for m in self.record()["catalog"]["models"] if m["slug"] == "test.grok")
        self.assertEqual(model["max_context_window"], 32768)

    def test_missing_or_unsafe_credentials_do_not_contact_gateway(self):
        for key in [None, "", " ", "offline-placeholder\ninjected"]:
            with self.subTest(key=key is None):
                if key is None:
                    self.env.pop("INFERPLANE_API_KEY", None)
                else:
                    self.env["INFERPLANE_API_KEY"] = key
                self.assert_refused(self.run_launcher())
        self.assertEqual(self.requests, [])

    def test_missing_default_or_unknown_explicit_model_refuses_before_codex(self):
        self.assert_refused(self.run_launcher("-m", "missing"))
        self.payload["data"] = [{"id": "test.grok", "context_window": 524288}]
        self.assert_refused(self.run_launcher())

    def test_bad_metadata_is_never_replaced_by_native_or_cached_models(self):
        for payload in [
            b"offline-placeholder not JSON", {}, {"data": []}, {"data": "models"},
            {"data": [{}]}, {"data": [{"id": "bad\nid"}]},
            {"data": [MODELS["data"][0], MODELS["data"][0]]},
            *[
                {"data": [{"id": "global.openai.gpt-6-astra", "context_window": bound}]}
                for bound in [0, -1, True, "524288", 1.5, 2 ** 63]
            ],
        ]:
            with self.subTest(payload=type(payload).__name__):
                self.payload = payload
                self.assert_refused(self.run_launcher())

    def test_reflected_credentials_in_metadata_are_not_printed(self):
        self.payload["data"] = [{"id": "offline-placeholder"}]
        self.assert_refused(self.run_launcher())

    def test_excessive_catalog_is_rejected_before_starting_codex(self):
        self.payload = b" " * (4 * 1024 * 1024 + 1)
        self.assert_refused(self.run_launcher())

    def test_errors_and_redirects_fail_closed_without_echoing_server_body(self):
        self.payload = b"offline-placeholder"
        for status in [401, 403, 500, 302]:
            with self.subTest(status=status):
                self.status = status
                self.requests.clear()
                self.assert_refused(self.run_launcher())
                self.assertEqual(len(self.requests), 1)
                self.assertEqual(self.requests[0][0], "/v1/models")
        self.server.shutdown()
        self.server.server_close()
        self.assert_refused(self.run_launcher())

    def test_proxy_environment_does_not_divert_gateway_credentials(self):
        self.env.update({
            "http_proxy": "http://127.0.0.1:1", "HTTP_PROXY": "http://127.0.0.1:1",
            "ALL_PROXY": "http://127.0.0.1:1", "NO_PROXY": "", "no_proxy": "",
        })
        result = self.run_launcher()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(self.requests), 1)

    def test_child_proxy_bypass_preserves_exclusions_and_parent_environment(self):
        proxies = {
            "HTTP_PROXY": "http://upper-http.invalid:8100",
            "HTTPS_PROXY": "http://upper-https.invalid:8101",
            "ALL_PROXY": "socks5://upper-all.invalid:8102",
            "http_proxy": "http://lower-http.invalid:8200",
            "https_proxy": "http://lower-https.invalid:8201",
            "all_proxy": "socks5://lower-all.invalid:8202",
        }
        self.env.update(proxies)
        for upper, lower, expected_existing in [
            (None, None, set()),
            ("", "", set()),
            ("internal.example,10.0.0.0/8", None, {"internal.example", "10.0.0.0/8"}),
            (None, ".svc.cluster.local,::1", {".svc.cluster.local", "::1"}),
            ("internal.example, localhost", ".svc.cluster.local,127.0.0.1",
             {"internal.example", "localhost", ".svc.cluster.local", "127.0.0.1"}),
            ("*", None, {"*"}),
        ]:
            with self.subTest(upper=upper, lower=lower):
                for key, value in [("NO_PROXY", upper), ("no_proxy", lower)]:
                    if value is None:
                        self.env.pop(key, None)
                    else:
                        self.env[key] = value
                result = self.run_launcher()
                # BOOTSTRAP also checks the launcher's own process environment
                # after main returns; checking this outer test process alone
                # could not catch a child-env implementation mutating os.environ.
                self.assertEqual(result.returncode, 0, result.stderr)
                child_env = self.record()["proxy_env"]
                for key, value in proxies.items():
                    self.assertEqual(child_env[key], value)
                for key in ("NO_PROXY", "no_proxy"):
                    self.assertIsInstance(child_env[key], str)
                    self.assertEqual(
                        set(child_env[key].split(",")),
                        expected_existing | {"127.0.0.1", "localhost", "::1"},
                    )
                self.assertEqual(child_env["NO_PROXY"], child_env["no_proxy"])
                self.record_path.unlink()

    def test_direct_provider_flags_fail_closed_but_separator_literals_are_preserved(self):
        for args in [["--oss"], ["exec", "--local-provider=ollama"], ["--remote", "ws://remote"]]:
            with self.subTest(args=args):
                self.assert_refused(self.run_launcher(*args))
        result = self.run_launcher("--", "--oss")
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_cli_exit_and_launch_failure_clean_up_catalog(self):
        self.env["FAKE_EXIT"] = "23"
        result = self.run_launcher()
        self.assertEqual(result.returncode, 23, result.stderr)
        self.assertFalse(Path(self.record()["catalog_path"]).parent.exists())
        self.record_path.unlink()
        (self.directory / "codex").unlink()
        self.assert_refused(self.run_launcher())

    def test_sigterm_is_forwarded_and_catalog_is_removed(self):
        self.env["FAKE_WAIT"] = "1"
        process = subprocess.Popen(
            self.command(), env=self.env, cwd=self.directory,
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        try:
            deadline = time.monotonic() + 5
            while not self.record_path.exists() and process.poll() is None and time.monotonic() < deadline:
                time.sleep(0.01)
            self.assertTrue(self.record_path.exists(), "mock Codex did not start")
            catalog_path = Path(self.record()["catalog_path"])
            process.send_signal(signal.SIGTERM)
            _, stderr = process.communicate(timeout=5)
            self.assertEqual(process.returncode, 128 + signal.SIGTERM, stderr)
            self.assertFalse(catalog_path.parent.exists())
        finally:
            if process.poll() is None:
                process.kill()
            process.communicate()

    def test_terminal_interrupt_is_not_sent_to_codex_twice(self):
        self.env.update({"FAKE_WAIT": "1", "FAKE_HANDLE_SIGINT": "1"})
        master, slave = os.openpty()
        self.addCleanup(os.close, master)
        self.addCleanup(os.close, slave)
        process = subprocess.Popen(
            self.command(), env=self.env, cwd=self.directory, stdin=slave,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True,
        )
        try:
            deadline = time.monotonic() + 5
            while not self.record_path.exists() and process.poll() is None and time.monotonic() < deadline:
                time.sleep(0.01)
            self.assertTrue(self.record_path.exists(), "mock Codex did not start")
            count_path = Path(str(self.record_path) + ".signals")
            # A terminal delivers SIGINT to both processes in the foreground
            # group. Deliver in sequence so POSIX signal coalescing cannot hide
            # a duplicate introduced by the wrapper.
            os.kill(self.record()["pid"], signal.SIGINT)
            while not count_path.exists() and time.monotonic() < deadline:
                time.sleep(0.01)
            self.assertTrue(count_path.exists())
            process.send_signal(signal.SIGINT)
            time.sleep(0.1)
            self.assertEqual(count_path.read_text(), "1")
            self.assertIsNone(process.poll(), "interrupt must leave a cancelling TUI alive")
        finally:
            if process.poll() is None:
                process.terminate()
            process.communicate(timeout=5)


@unittest.skipUnless(
    os.environ.get("INFERPLANE_TEST_CODEX"),
    "set INFERPLANE_TEST_CODEX to opt into installed-CLI tests against loopback fakes",
)
class InstalledCodexTests(unittest.TestCase):
    """No real credentials, user config, upstream APIs, or generated tool execution."""

    def setUp(self):
        self.fixture = LauncherTests()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        cli = self.fixture.directory / "codex"
        cli.unlink()
        cli.symlink_to(Path(os.environ["INFERPLANE_TEST_CODEX"]).resolve())
        self.home = Path(self.fixture.env["CODEX_HOME"])
        self.home.mkdir()
        self.fixture.env["RUST_LOG"] = "off"

    def run_cli(self, config="", extra=(), model="test.grok"):
        (self.home / "config.toml").write_text(config)
        model_args = ["-m", model] if model is not None else []
        result = self.fixture.run_launcher(
            # Exercise both CLI levels: provider config before exec would lose
            # to the subcommand's intentionally invalid provider override.
            "-c", 'model_provider="wrong-root"',
            "exec", "--skip-git-repo-check", "--ephemeral",
            "--sandbox", "read-only", *model_args,
            "-c", 'model_provider="wrong-subcommand"',
            *extra, "Respond offline ok without running any tools",
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(self.fixture.response_requests), 1)
        path, authorization, body = self.fixture.response_requests[0]
        self.assertEqual(path, "/v1/responses")
        self.assertEqual(authorization, "Bearer offline-placeholder")
        self.assertEqual(body["model"], model if model is not None else "global.openai.gpt-6-astra")
        self.assertEqual(list(self.fixture.directory.glob("inferplane-codex-*")), [])
        return body

    def test_installed_cli_accepts_catalog_and_portable_model_sends_neutral_effort(self):
        body = self.run_cli()
        self.assertEqual(body.get("reasoning", {}).get("effort"), "none")
        self.assertFalse(any(
            item.get("type") in {"reasoning", "additional_tools", "context_compaction"}
            for item in body.get("input", [])
        ))

    def test_bridge_overrides_inherited_xhigh_in_codex_0154(self):
        self.fixture.payload["data"][1].update({
            "responses_mode": "bridge", "capabilities": ["tools"],
        })
        body = self.run_cli('model_reasoning_effort="xhigh"\n')
        self.assertEqual(body.get("reasoning", {}).get("effort"), "none")
        self.assertIn("reasoning.encrypted_content", body.get("include", []))

    def test_bridge_overrides_explicit_non_neutral_cli_effort(self):
        body = self.run_cli(extra=("-c", 'model_reasoning_effort="high"'))
        self.assertEqual(body.get("reasoning", {}).get("effort"), "none")

    def test_native_mapping_keeps_actual_bundled_capabilities(self):
        self.fixture.payload["data"][1].update({
            "id": "openai/gpt-6-astra",
            "responses_mode": "native", "codex_model": "gpt-6-astra",
            "capabilities": ["tools", "reasoning"],
        })
        body = self.run_cli('model_reasoning_effort="xhigh"\n', model="openai/gpt-6-astra")
        self.assertEqual(body.get("reasoning", {}).get("effort"), "xhigh")
        # The installed Astra template enables Responses Lite, unlike the
        # portable catalog. This assertion catches accidental generic cloning.
        self.assertTrue(any(
            item.get("type") == "additional_tools" for item in body.get("input", [])
        ))

    def test_explicit_native_astra_keeps_user_effort(self):
        self.fixture.payload["data"][3].update({
            "responses_mode": "native", "codex_model": "gpt-6-astra",
            "capabilities": ["tools", "reasoning"],
        })
        body = self.run_cli('model_reasoning_effort="xhigh"\n', model="openai.gpt-6-astra")
        self.assertEqual(body.get("reasoning", {}).get("effort"), "xhigh")

    def test_unsupported_route_does_not_block_an_installed_cli_bridge_launch(self):
        self.fixture.payload["data"].append({
            "id": "unsupported.route", "responses_mode": "unsupported",
            "capabilities": ["tools"],
        })
        body = self.run_cli('model_reasoning_effort="xhigh"\n')
        self.assertEqual(body.get("reasoning", {}).get("effort"), "none")

    def test_unmapped_native_alias_does_not_block_an_installed_cli_bridge_launch(self):
        self.fixture.payload["data"][3].update({
            "responses_mode": "native", "codex_model": "gpt-6-astra",
            "capabilities": ["tools", "reasoning"],
        })
        self.fixture.payload["data"].append({
            "id": "openai.unmapped-native-model", "responses_mode": "native",
            "codex_model": "unmapped-native-model", "context_window": 32768,
            "capabilities": ["tools"],
        })
        body = self.run_cli('model_reasoning_effort="xhigh"\n')
        self.assertEqual(body.get("reasoning", {}).get("effort"), "none")

    def test_effective_context_is_capped_despite_inherited_fixed_window(self):
        (self.home / "config.toml").write_text(
            'model_context_window=1048576\nmodel_reasoning_effort="xhigh"\n'
        )
        client = AppServerClient(self.fixture, "test.grok")
        self.addCleanup(client.close)
        client.initialize()
        config = client.call("config/read", {"includeLayers": False})["config"]
        self.assertEqual(config["model_context_window"], 1048576)
        thread = client.call("thread/start", {
            "cwd": str(self.fixture.directory), "ephemeral": True, "sandbox": "read-only",
        })
        self.assertEqual(thread["model"], "test.grok")
        thread_id = thread["thread"]["id"]
        usage = client.turn(thread_id, "First offline context test; do not use tools.")
        # Grok: 524288 declared tokens, 80% usable. The raw global setting
        # deliberately remains 1048576, so this checks resolved runtime limits.
        self.assertEqual(usage["modelContextWindow"], 419430)
        self.assertEqual(self.fixture.response_requests[0][2]["model"], "test.grok")

    def test_default_portable_astra_switches_to_grok_with_history_and_capped_context(self):
        self.fixture.payload["data"][0].update({
            "responses_mode": "bridge", "capabilities": ["tools"],
        })
        self.fixture.payload["data"][3].update({
            "responses_mode": "native", "codex_model": "gpt-6-astra",
            "capabilities": ["tools", "reasoning"],
        })
        (self.home / "config.toml").write_text(
            'model_context_window=1048576\nmodel_reasoning_effort="xhigh"\n'
        )
        client = AppServerClient(self.fixture, None)
        self.addCleanup(client.close)
        client.initialize()
        config = client.call("config/read", {"includeLayers": False})["config"]
        self.assertEqual(config["model_context_window"], 1048576)
        thread = client.call("thread/start", {
            "cwd": str(self.fixture.directory), "ephemeral": True, "sandbox": "read-only",
        })
        self.assertEqual(thread["model"], "global.openai.gpt-6-astra")
        thread_id = thread["thread"]["id"]
        first = client.turn(thread_id, "Remember this portable-first-turn marker; do not use tools.")
        second = client.turn(
            thread_id, "Continue the existing conversation without tools.", model="test.grok"
        )
        self.assertEqual(first["modelContextWindow"], 800000)
        self.assertEqual(second["modelContextWindow"], 419430)
        bodies = [request[2] for request in self.fixture.response_requests]
        self.assertEqual([body["model"] for body in bodies], ["global.openai.gpt-6-astra", "test.grok"])
        for body in bodies:
            self.assertEqual(body.get("reasoning", {}).get("effort"), "none")
            self.assertNotIn("previous_response_id", body)
            self.assertFalse(any(
                item.get("type") in {"reasoning", "additional_tools", "context_compaction"}
                or "encrypted_content" in item
                for item in body.get("input", [])
            ))
        replayed = json.dumps(bodies[1]["input"])
        self.assertIn("portable-first-turn", replayed)
        self.assertIn("offline ok", replayed)


if __name__ == "__main__":
    unittest.main()
