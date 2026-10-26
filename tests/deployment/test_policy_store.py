import importlib.util
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location(
    "prepare_policy_cp", ROOT / "scripts/prepare-policy-controlplane.py"
)
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)


class PolicyRoutingTests(unittest.TestCase):
    def fixture(self):
        return {
            "Parameters": {"Image": {"Type": "String"}},
            "Resources": {
                "TaskDef": {"Properties": {"ExecutionRoleArn": {"Fn::GetAtt": "Role.Arn"}}},
                "Distribution": {"Properties": {"DistributionConfig": {
                    "DefaultCacheBehavior": {
                        "TargetOriginId": "data", "CachePolicyId": "cache-disabled",
                        "OriginRequestPolicyId": "forward-auth", "AllowedMethods": [
                            "GET", "HEAD", "OPTIONS", "PUT", "PATCH", "POST", "DELETE"],
                    },
                    "Origins": [{
                        "Id": "data", "DomainName": {"Fn::GetAtt": "NLB.DNSName"},
                        "VpcOriginConfig": {"VpcOriginId": "vo_existing", "OriginReadTimeout": 60},
                    }],
                    "CacheBehaviors": [{"PathPattern": "/ui/app.js", "TargetOriginId": "assets"}],
                }}},
            },
        }

    def test_preserves_all_unrelated_resources_and_request_authorization(self):
        source = self.fixture()
        original = json.loads(json.dumps(source))
        result = helper.prepare(source)
        self.assertEqual(source, original)
        self.assertEqual(result["Resources"]["TaskDef"], source["Resources"]["TaskDef"])
        cfg = result["Resources"]["Distribution"]["Properties"]["DistributionConfig"]
        old = source["Resources"]["Distribution"]["Properties"]["DistributionConfig"]
        self.assertEqual(cfg["DefaultCacheBehavior"], old["DefaultCacheBehavior"])
        self.assertEqual(cfg["Origins"][:-1], old["Origins"])
        self.assertEqual(cfg["CacheBehaviors"][len(helper.PATHS):], old["CacheBehaviors"])
        self.assertEqual([b["PathPattern"] for b in cfg["CacheBehaviors"][:len(helper.PATHS)]], list(helper.PATHS))
        for behavior in cfg["CacheBehaviors"][:len(helper.PATHS)]:
            self.assertEqual(behavior["OriginRequestPolicyId"], "forward-auth")
            self.assertEqual(behavior["AllowedMethods"], old["DefaultCacheBehavior"]["AllowedMethods"])
        self.assertIn("/v1alpha1/config/*", helper.PATHS)
        self.assertNotIn("/v1alpha1/usage", helper.PATHS)
        self.assertNotIn("/ui/*", helper.PATHS)

    def test_refuses_overwriting_existing_route_or_origin(self):
        for collision in ("route", "origin"):
            source = self.fixture()
            cfg = source["Resources"]["Distribution"]["Properties"]["DistributionConfig"]
            if collision == "route":
                cfg["CacheBehaviors"].append({"PathPattern": "/v1alpha1/sync"})
            else:
                cfg["Origins"].append({"Id": "policy-cp"})
            with self.assertRaises(ValueError):
                helper.prepare(source)

    def test_refuses_unreviewed_non_vpc_origin(self):
        source = self.fixture()
        del source["Resources"]["Distribution"]["Properties"]["DistributionConfig"]["Origins"][0]["VpcOriginConfig"]
        with self.assertRaises(ValueError):
            helper.prepare(source)


class BootstrapTests(unittest.TestCase):
    def run_script(self, password="A" * 40, tls="verify-full", fail=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fake = root / "psql"
            fake.write_text(
                "#!/bin/sh\n"
                "printf '%s\\n' \"$PGUSER\" >> \"$CALL_FILE\"\n"
                "if [ \"$FAIL_PSQL\" = 1 ]; then echo \"$PGPASSWORD\" >&2; exit 1; fi\n"
                "cat >/dev/null\n"
            )
            fake.chmod(0o755)
            calls = root / "calls"
            env = {**os.environ, "PATH": directory + ":" + os.environ["PATH"],
                   "APP_DB_PASSWORD": password, "PGPASSWORD": "synthetic-master-only",
                   "PGHOST": "unused.invalid", "PGUSER": "inferplane_admin",
                   "PGSSLMODE": tls, "CALL_FILE": str(calls), "FAIL_PSQL": "1" if fail else "0"}
            result = subprocess.run(
                ["sh", str(ROOT / "deploy/ecs/bootstrap-policy-db.sh")],
                env=env, capture_output=True, text=True,
            )
            return result, calls.read_text().splitlines() if calls.exists() else []

    def test_verifies_separate_non_admin_login(self):
        result, calls = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls, ["inferplane_admin", "inferplane_cp"])
        self.assertNotIn("A" * 40, result.stdout + result.stderr)

    def test_refuses_sql_injection_before_connecting(self):
        result, calls = self.run_script(password="A" * 39 + "'")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls, [])

    def test_requires_full_tls_verification_before_connecting(self):
        result, calls = self.run_script(tls="require")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls, [])

    def test_redacts_failed_database_client_output(self):
        result, _ = self.run_script(fail=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("synthetic-master-only", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
