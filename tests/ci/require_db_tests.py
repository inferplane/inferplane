#!/usr/bin/env python3
"""Require executed DB sentinels in a complete `go test -json` stream on stdin."""

import json
import sys


REQUIRED = {
    "github.com/inferplane/inferplane/internal/authority/pgstore": {
        "TestConcurrentReplicasReserveAtMostLimit",
        "TestExpiryNeverRefundsOrExtendsReplay",
        "TestTwoJournalsTwoReplicasAndJournalRestart",
        "TestSharedPolicyMoneyCompetesWithLocalGrants",
        "TestSharedPolicyRateUserScopesAndStableIdentity",
        "TestSharedOriginalCalendarWindowsFinishAndCancel",
    },
    "github.com/inferplane/inferplane/internal/keystore": {
        "TestPostgresResolveNeverTearsKeyAndTeamSnapshot",
    },
}
ACTIONS = {"start", "run", "pause", "cont", "pass", "bench", "fail", "output", "skip"}


def unique_fields(pairs):
    fields = {}
    for key, value in pairs:
        if key in fields:
            raise ValueError
        fields[key] = value
    return fields


def reject_constant(_):
    raise ValueError


def verify(lines):
    ran, passed, rejected = set(), set(), set()
    packages = {}
    failed = False
    for number, line in enumerate(lines, 1):
        try:
            item = json.loads(line, object_pairs_hook=unique_fields, parse_constant=reject_constant)
            if not isinstance(item, dict):
                raise ValueError
            action = item.get("Action")
            if action in ("build-output", "build-fail"):
                if not isinstance(item.get("ImportPath"), str) or not item["ImportPath"]:
                    raise ValueError
                if action == "build-output" and not isinstance(item.get("Output"), str):
                    raise ValueError
                failed |= action == "build-fail"
                continue
            package, test = item.get("Package"), item.get("Test")
            if not isinstance(action, str) or action not in ACTIONS:
                raise ValueError
            if not isinstance(package, str) or not package:
                raise ValueError
            if "Test" in item and (not isinstance(test, str) or not test):
                raise ValueError
            if action == "output" and not isinstance(item.get("Output"), str):
                raise ValueError
        except (ValueError, TypeError):
            # Never echo raw output, which may contain test connection details.
            return [f"invalid Go JSON event at line {number}"]
        failed |= action == "fail"
        if package not in REQUIRED:
            continue
        if test is None:
            if action in ("pass", "skip", "fail"):
                packages[package] = action
            continue
        root = test.split("/", 1)[0]
        if root not in REQUIRED[package]:
            continue
        key = (package, root)
        # A parent can pass when every DB-backed child skipped.
        if action in ("skip", "fail"):
            rejected.add(key)
        elif test != root:
            continue
        elif action == "run":
            ran.add(key)
        elif action == "pass":
            passed.add(key)
    errors = ["Go reported a test or build failure"] if failed else []
    for package, tests in REQUIRED.items():
        for test in sorted(tests):
            key = (package, test)
            if key in rejected or key not in ran or key not in passed:
                errors.append(f"{package}/{test}: missing, skipped, failed or incomplete")
        if packages.get(package) != "pass":
            errors.append(f"{package}: missing successful package completion")
    return errors


def main():
    try:
        errors = verify(sys.stdin)
    except (OSError, UnicodeError):
        errors = ["cannot read a valid Go JSON results stream"]
    if errors:
        for error in errors:
            print(f"DB sentinel gate: {error}", file=sys.stderr)
        return 1
    print("DB sentinel gate: 7 required tests passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
