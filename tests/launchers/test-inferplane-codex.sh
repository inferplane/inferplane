#!/bin/bash
# Sourced by tests/run-all.sh; Python's HTTP server binds loopback on a random port.
if python3 -B -m unittest discover -s tests/launchers -p 'test_inferplane_codex.py'; then
    pass "model-aware Codex launcher offline contracts"
else
    fail "model-aware Codex launcher offline contracts" "Python unittest failed"
fi

if python3 -B -m unittest discover -s tests/launchers -p 'test_codex_hud.py'; then
    pass "Codex HUD interactive selection"
else
    fail "Codex HUD interactive selection" "Python unittest failed"
fi
