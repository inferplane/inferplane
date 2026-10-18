# Korean PII classifier fine-tuning (research)

Experiment tooling for [the fine-tuning plan](../../docs/superpowers/plans/2026-10-13-korean-pii-classifier-finetune.md).
It produces measurements for the `systemone` classifier type in
[the pluggable PII classifier spec](../../docs/superpowers/specs/2026-10-07-pii-classifiers-design.md).
Nothing here is wired into the gateway.

The scripts that produced these results are published, without account-specific
values, as [`examples/pii-classifier-ko/training/`](../../examples/pii-classifier-ko/training/README.md);
run them from there. This directory keeps only the result files.

| Path | Purpose |
|---|---|
| `results/metrics_{test,ood}.json`, `results/table_{test,ood}.md` | Same-generator test and held-out-genre (OOD) metrics for the v1 and v2 rounds. |
| `results/indep/metrics_{full,clean}.json`, `results/indep/disputed_ids.txt` | The v1 independent set, before and after excluding audited disputed labels. |

The v3 results, including the published checkpoint (Laya v3 seed 1) on the
2,236-row independent set v3, are in the plan's results section.

## Request settings that change the answer

- **Laya** routes Latin-script text to its stock English checkpoint unless the
  request names `"model": "multilingual"`.
- **strands-decider** renders a `noul` question with default option descriptions
  unless the request sends the training `criteria`:
  `{"false": "no personal information about a specific person and no secret",
  "true": "contains personal information about a specific person or a secret credential"}`.

The training job sends both. Jobs launched before the fix were re-scored on
CPU with the correct settings.

Generated data stays out of git (`.gitignore`); it lives in a private S3 bucket and is not published.
