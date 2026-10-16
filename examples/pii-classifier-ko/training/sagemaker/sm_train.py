"""SageMaker training entry point: baseline score, fine-tune, calibrate, score.

Runs inside the AWS PyTorch training DLC (torch 2.7.1, Python 3.12). Hyperparameters:
  backend   laya | strands
  epochs    training epochs (backend default when omitted)
  max_steps strands only: hard step cap for smoke runs
  skip_baseline  "1" to skip the zero-shot baseline
  laya_args laya only: extra laya-train flags, space separated

Channel `data` holds neutral/{test,ood}.jsonl and backends/<backend>/*.jsonl.
Everything written to /opt/ml/model (checkpoint, predictions, reports) is
uploaded to S3 as model.tar.gz when the job ends.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
DATA = os.environ.get("SM_CHANNEL_DATA", "/opt/ml/input/data/data")
OUT = os.environ.get("SM_MODEL_DIR", "/opt/ml/model")
HP = json.loads(os.environ.get("SM_HPS", "{}"))
WORK = "/tmp/work"

LAYA_REPO, LAYA_COMMIT = "https://github.com/NandhaKishorM/laya.git", "c73908d"
SD_REPO, SD_COMMIT = "https://github.com/strands-labs/strands-decider.git", "b9fc836"
SD_RELEASED = "StrandsAgents/strands-decider-2B-qwen3.5-v1-2610"


def sh(cmd: list[str] | str, **kw) -> None:
    print("+", cmd if isinstance(cmd, str) else " ".join(cmd), flush=True)
    subprocess.run(cmd, check=True, shell=isinstance(cmd, str), **kw)


def clone(repo: str, commit: str, dest: str) -> None:
    sh(["git", "clone", "-q", repo, dest])
    sh(["git", "-C", dest, "checkout", "-q", commit])
    sh(["git", "-C", dest, "log", "-1", "--format=%H %cd"])


def serve_and_score(cmd: list[str], env: dict, tag: str, port: int, model: str | None = None,
                    criteria: dict | None = None) -> None:
    log = open(os.path.join(OUT, f"serve_{tag}.log"), "w")
    proc = subprocess.Popen(cmd, env={**os.environ, **env}, stdout=log, stderr=subprocess.STDOUT)
    try:
        for split in ("test", "ood"):
            sh([sys.executable, os.path.join(HERE, "score_http.py"), "--base", f"http://127.0.0.1:{port}",
                "--rows", os.path.join(DATA, "neutral", f"{split}.jsonl"),
                "--out", os.path.join(OUT, "preds", f"{tag}_{split}.jsonl")] + (["--model", model] if model else [])
           + (["--criteria", json.dumps(criteria)] if criteria else []))
    finally:
        proc.terminate()
        try:
            proc.wait(60)
        except subprocess.TimeoutExpired:
            proc.kill()
        log.close()


def run_laya() -> None:
    # Every request names "multilingual": Laya's router otherwise sends Latin-script text
    # (English rows) to the stock English checkpoint, not the fine-tuned one.
    src = os.path.join(WORK, "laya")
    clone(LAYA_REPO, LAYA_COMMIT, src)
    sh([sys.executable, "-m", "pip", "install", "-q", "-e", f"{src}[serve]"])
    d = os.path.join(DATA, "backends", "laya")
    serve_env = {"LAYA_DEVICE": "cuda", "LAYA_HOST": "127.0.0.1", "LAYA_PORT": "8000", "LAYA_MODELS": "multilingual",
                 "LAYA_DEFAULT_MODEL": "multilingual"}
    if str(HP.get("skip_baseline", "")) != "1":  # SM_HPS JSON-decodes "1" to 1
        serve_and_score(["laya-serve"], serve_env, "laya_base", 8000, model="multilingual")
    ckpt = os.path.join(OUT, "laya-ko-pii")
    cmd = ["laya-train", "--data", os.path.join(d, "train.jsonl"), "--eval", os.path.join(d, "val.jsonl"),
           "--base", "multilingual", "--out", ckpt, "--device", "cuda", "--seed", str(HP.get("seed", 0))]
    if HP.get("epochs"):
        cmd += ["--epochs", str(HP["epochs"])]
    if HP.get("laya_args"):  # e.g. "--loss soft-ce --grad-accum 2"
        cmd += str(HP["laya_args"]).split()
    sh(cmd)
    serve_and_score(["laya-serve"], {**serve_env, "LAYA_EXTRA_MODELS": json.dumps({"multilingual": ckpt})}, "laya_ft", 8000, model="multilingual")


def run_strands() -> None:
    src = os.path.join(WORK, "strands-decider")
    clone(SD_REPO, SD_COMMIT, src)
    sh([sys.executable, "-m", "pip", "install", "-q", "transformers==5.17.0", "peft==0.21.0"])
    sh([sys.executable, "-m", "pip", "install", "-q", "-e", f"{src}[train,cuda]"])
    d = os.path.join(DATA, "backends", "strands")
    if str(HP.get("skip_baseline", "")) != "1":  # SM_HPS JSON-decodes "1" to 1
        serve_and_score(["strands-decider", "serve", SD_RELEASED, "--device", "cuda", "--port", "8001"], {}, "strands_base", 8001)
    ckpt = os.path.join(OUT, "strands-ko-pii")
    cfg = {
        "train_files": [os.path.join(d, "train.jsonl")], "val_fraction": 0.03,
        "base_model": "Qwen/Qwen3.5-2B-Base", "num_slots": 24, "head_type": "pointer", "pointer_dim": 256,
        "head_init": "random", "kl_frozen_weight": 0.3, "head_dropout": 0.05, "max_length": 4096,
        "use_lora": True, "lora_r": 16, "lora_alpha": 32,
        "lora_targets": ["q_proj", "k_proj", "v_proj", "o_proj", "gate_proj", "up_proj", "down_proj",
                         "in_proj_qkv", "in_proj_z", "in_proj_a", "in_proj_b", "out_proj"],
        "freeze_torso": False, "epochs": int(HP.get("epochs", 2)), "micro_batch_size": 8, "grad_accum": 4,
        "lr": 0.0001, "head_lr": 0.001, "weight_decay": 0.01, "warmup_ratio": 0.03, "max_grad_norm": 1.0,
        "gradient_checkpointing": True, "shuffle_options": True, "group_by_length": True,
        "ordinal_smoothing": 0.1, "reverse_score_prob": 0.5, "output_dir": ckpt, "seed": int(HP.get("seed", 0)),
        "log_every": 20, "eval_every": 200,
    }
    import yaml  # PyYAML ships with the DLC

    cfg_path = os.path.join(OUT, "strands_train.yaml")
    with open(cfg_path, "w") as f:
        yaml.safe_dump(cfg, f)
    cmd = ["strands-decider", "train", "--config", cfg_path]
    if HP.get("max_steps"):
        cmd += ["--max-steps", str(HP["max_steps"])]
    sh(cmd)
    sh(["strands-decider", "calibrate", ckpt, "--data", os.path.join(d, "val.jsonl"), "--split", "all"])
    for split in ("test", "ood"):
        sh(["strands-decider", "eval", ckpt, "--data", os.path.join(d, f"{split}.jsonl"), "--split", "all",
            "--out", os.path.join(OUT, "reports", f"strands_ft_cli_{split}.json")])
    # The fine-tuned rows carry their own option descriptions; a request without the same
    # `criteria` is rendered with the server defaults and reads as a different question.
    crit = {"false": "no personal information about a specific person and no secret",
            "true": "contains personal information about a specific person or a secret credential"}
    serve_and_score(["strands-decider", "serve", ckpt, "--device", "cuda", "--port", "8001"], {}, "strands_ft", 8001,
                    criteria=crit)


def main() -> None:
    os.makedirs(os.path.join(OUT, "preds"), exist_ok=True)
    os.makedirs(os.path.join(OUT, "reports"), exist_ok=True)
    os.makedirs(WORK, exist_ok=True)
    os.environ.setdefault("HF_HOME", "/tmp/hf")
    t0 = time.time()
    sh("nvidia-smi; python --version; python -c 'import torch;print(torch.__version__, torch.cuda.is_available())'")
    {"laya": run_laya, "strands": run_strands}[HP["backend"]]()
    with open(os.path.join(OUT, "reports", "job.json"), "w") as f:
        json.dump({"backend": HP["backend"], "hyperparameters": HP, "seconds": round(time.time() - t0)}, f)
    shutil.copy(__file__, os.path.join(OUT, "sm_train.py"))


if __name__ == "__main__":
    main()
