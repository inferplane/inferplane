#!/usr/bin/env bash
# Score one checkpoint on one rows file on CPU, with the training-time request settings.
# Usage: indep/score_one.sh laya|strands <checkpoint-dir> <rows.jsonl> <out.jsonl> [port]
set -euo pipefail
H=$(cd "$(dirname "$0")/.." && pwd); D="$H/data"
backend=$1 ck=$2 rows=$3 out=$4 port=${5:-18010}
mkdir -p "$D/.tmp" "$(dirname "$out")"
export HF_HOME="$D/.hf" TMPDIR="$D/.tmp"
CRIT='{"false": "no personal information about a specific person and no secret", "true": "contains personal information about a specific person or a secret credential"}'
if [ "$backend" = laya ]; then
  ( . "$D/venv/bin/activate"
    LAYA_DEVICE=cpu LAYA_HOST=127.0.0.1 LAYA_PORT=$port LAYA_MODELS=multilingual LAYA_DEFAULT_MODEL=multilingual \
      LAYA_EXTRA_MODELS="{\"multilingual\": \"$ck\"}" exec laya-serve ) > "$out.serve.log" 2>&1 &
  args=(--model multilingual)
else
  ( . "$D/venv-sd/bin/activate"; exec strands-decider serve "$ck" --device cpu --port "$port" ) > "$out.serve.log" 2>&1 &
  args=(--criteria "$CRIT")
fi
pid=$!
trap 'kill $pid 2>/dev/null || true' EXIT
python3 "$H/sagemaker/score_http.py" --base "http://127.0.0.1:$port" --rows "$rows" --out "$out" "${args[@]}"
echo "scored $out"
