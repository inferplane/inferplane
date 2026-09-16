"""Exercise the CI gate as a CLI using only synthetic Go JSON events."""

import json
from pathlib import Path
import subprocess
import sys
import unittest


GATE = Path(__file__).with_name("require_db_tests.py")
AUTHORITY = "github.com/inferplane/inferplane/internal/authority/pgstore"
KEYSTORE = "github.com/inferplane/inferplane/internal/keystore"
# Independent inventory from hardening program section 5, not imported from gate.
REQUIRED = {
    AUTHORITY: (
        "TestConcurrentReplicasReserveAtMostLimit",
        "TestExpiryNeverRefundsOrExtendsReplay",
        "TestTwoJournalsTwoReplicasAndJournalRestart",
        "TestSharedPolicyMoneyCompetesWithLocalGrants",
        "TestSharedPolicyRateUserScopesAndStableIdentity",
        "TestSharedOriginalCalendarWindowsFinishAndCancel",
    ),
    KEYSTORE: ("TestPostgresResolveNeverTearsKeyAndTeamSnapshot",),
}


def event(action, package, test=None, **extra):
    result = {"Action": action, "Package": package, **extra}
    if test is not None:
        result["Test"] = test
    return result


def complete_results():
    results = []
    for package, tests in REQUIRED.items():
        results.append(event("start", package))
        for test in tests:
            results.extend(
                [
                    event("run", package, test),
                    event("output", package, test, Output="synthetic test output\n"),
                    event("pass", package, test),
                ]
            )
        results.append(event("pass", package))
    return results


def invoke(raw):
    return subprocess.run(
        [sys.executable, str(GATE)],
        input=raw,
        text=True,
        capture_output=True,
        check=False,
    )


def check_events(events):
    return invoke("".join(json.dumps(item) + "\n" for item in events))


class DatabaseGateTests(unittest.TestCase):
    def assert_rejected(self, result):
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("DB sentinel gate:", result.stderr)

    def test_complete_pass_with_subtests_and_unrelated_skips(self):
        events = complete_results()
        test = REQUIRED[AUTHORITY][4]
        index = events.index(event("pass", AUTHORITY, test))
        events[index:index] = [
            event("run", AUTHORITY, test + "/user"),
            event("pause", AUTHORITY, test + "/user"),
            event("cont", AUTHORITY, test + "/user"),
            event("pass", AUTHORITY, test + "/user"),
            event("skip", AUTHORITY, "TestUnrelatedOptionalIntegration"),
            event("skip", "example.invalid/optional"),
            {"Action": "build-output", "ImportPath": AUTHORITY, "Output": "warning\n"},
        ]
        result = check_events(events)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("7 required tests passed", result.stdout)

    def test_each_required_test_must_be_present_and_in_exact_package(self):
        for package, tests in REQUIRED.items():
            for test in tests:
                for mode in ("missing", "wrong package", "only subtest"):
                    with self.subTest(package=package, test=test, mode=mode):
                        events = complete_results()
                        if mode == "missing":
                            events = [
                                e for e in events
                                if (e.get("Package"), e.get("Test")) != (package, test)
                            ]
                        else:
                            for e in events:
                                if (e.get("Package"), e.get("Test")) == (package, test):
                                    if mode == "wrong package":
                                        e["Package"] = "example.invalid/wrong"
                                    else:
                                        e["Test"] += "/child"
                        self.assert_rejected(check_events(events))

    def test_each_required_skip_or_failure_is_sticky(self):
        for package, tests in REQUIRED.items():
            for test in tests:
                for action in ("skip", "fail"):
                    with self.subTest(package=package, test=test, action=action):
                        events = complete_results()
                        index = events.index(event("pass", package, test))
                        # A later pass must not erase an earlier skipped/failed run.
                        events.insert(index, event(action, package, test))
                        self.assert_rejected(check_events(events))

    def test_skipped_or_failed_subtest_cannot_hide_behind_parent_pass(self):
        test = REQUIRED[AUTHORITY][4]
        for action in ("skip", "fail"):
            with self.subTest(action=action):
                events = complete_results()
                index = events.index(event("pass", AUTHORITY, test))
                events[index:index] = [
                    event("run", AUTHORITY, test + "/user"),
                    event(action, AUTHORITY, test + "/user"),
                ]
                self.assert_rejected(check_events(events))

    def test_all_skip_is_rejected_even_with_successful_package_exit(self):
        events = complete_results()
        for e in events:
            if e["Action"] == "pass" and "Test" in e:
                e["Action"] = "skip"
        self.assert_rejected(check_events(events))

    def test_run_and_completed_package_are_required(self):
        for package in REQUIRED:
            for mode in ("no run", "no package completion", "package skipped"):
                with self.subTest(package=package, mode=mode):
                    events = complete_results()
                    if mode == "no run":
                        events = [
                            e for e in events
                            if not (e["Package"] == package and e["Action"] == "run")
                        ]
                    elif mode == "no package completion":
                        events.remove(event("pass", package))
                    else:
                        events[events.index(event("pass", package))]["Action"] = "skip"
                    self.assert_rejected(check_events(events))

    def test_package_or_build_failure_is_rejected(self):
        for failure in (
            event("fail", "example.invalid/other"),
            {"Action": "build-fail", "ImportPath": "example.invalid/broken"},
        ):
            with self.subTest(failure=failure):
                self.assert_rejected(check_events(complete_results() + [failure]))

    def test_invalid_or_truncated_input_is_rejected_without_echoing_payload(self):
        valid = "".join(json.dumps(e) + "\n" for e in complete_results())
        for bad in (
            "private-marker-not-json",
            '{"Action":',
            "null",
            "[]",
            "{}",
            '{"Action":"pass","Package":null}',
            '{"Action":"pass","Package":"pkg","Test":[]}',
            '{"Action":"unexpected","Package":"pkg"}',
            '{"Action":"output","Package":"pkg","Output":42}',
            '{"Action":"pass","Package":"pkg","Elapsed":NaN}',
            '{"Action":"skip","Action":"pass","Package":"pkg"}',
        ):
            with self.subTest(bad=bad):
                result = invoke(valid + bad + "\n")
                self.assert_rejected(result)
                self.assertNotIn("private-marker", result.stderr)
        self.assert_rejected(invoke(""))


if __name__ == "__main__":
    unittest.main()
