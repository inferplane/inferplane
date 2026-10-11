#!/bin/bash
# Sourced by tests/run-all.sh; no network, model, or credentials are required.
if python3 -B -m unittest discover -s tests/launchers -p 'test_context_p*.py'; then
    pass "per-model Codex context profile bounds"
else
    fail "per-model Codex context profile bounds" "Python unittest failed"
fi
