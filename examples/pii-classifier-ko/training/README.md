# Training recipe: Korean PII classifier for `systemone`

This recipe produced the reference `laya-ko-pii-v4s2` checkpoint, which is
not distributed: train your own and bake it into the sidecar image with
`../laya-serve/` (see `../README.md`). It needs an AWS account with Amazon
Bedrock model access, one private S3 bucket and a SageMaker execution role
scoped to that bucket. It creates no instance, public IP or security group:
training runs as SageMaker training jobs that end on completion.

## Label policy

The single `noul` question is the trained statement: "Does this text contain
personal information about a specific person, or a secret credential?". A text
is labeled true when it contains at least one of:

- a direct identifier on its own: resident registration number, foreigner
  registration number, passport or driver's license number, phone number,
  personal email, home address, bank account or payment card number;
- a named person together with an attribute about them, such as a birth date,
  health, salary or employee record;
- an internal identifier (employee number or vehicle plate) together with a
  health or salary fact about the same person (rule B);
- a secret credential: password, API key, access token or private key.

A direct identifier counts on its own, with or without a name, in any length of text.

It is false for:

- a name with no attribute, or with only an employer, team, job title or school;
- code, schemas or documents that handle personal-data fields without values;
- masked or placeholder values;
- organisation identifiers;
- lookalike tokens such as UUIDs, hashes and version strings;
- either side of a rule-B pair on its own.

## Steps

1. `datagen/kdpii.py`: downloads KDPII (CC BY 4.0, see `../NOTICE`) and maps
   its entity labels onto the policy above. It splits by dialogue 8:1:1, so no
   dialogue spans two splits, and writes one row per sentence and one per window
   of at most 1,000 characters:
   `python3 datagen/kdpii.py fetch --out data/kdpii/PII_dataset_V3.json`, then
   `python3 datagen/kdpii.py build --src data/kdpii/PII_dataset_V3.json --out data/kdpii`.
2. `datagen/gen_templates.py`: Claude on Amazon Bedrock writes slot templates
   (`⟦PHONE⟧`, …), never values. v4 adds two runs to the v3 templates:
   - direct identifiers without a name at document length:
     `--kinds pos_direct_anon --calls 4 --per-call 6`;
   - short texts on conversational genres: `--length short
     --genres chat_turn,form_input,kakao_message,dev_chat,customer_ticket,english_chat
     --kinds pos_direct_anon,pos_direct,pos_attr,pos_cred,neg_name_only,neg_lookalike,neg_handling
     --calls 6 --per-call 10`.
3. `datagen/fill.py --pair-rate 1.0 --obf-rate 0.15`: fills the slots locally
   with synthetic values and adds masked minimal pairs. It writes 15% of phone,
   registration, account and card values as fullwidth digits, spaced characters
   or Korean digit words. It splits the data by template, with two genres held out.
4. `datagen/mix.py --kdpii-frac 0.6 --max-share 0.5`: adds KDPII train rows,
   capped at half of the training set, and KDPII val rows to val. KDPII test
   stays evaluation-only.
5. `datagen/convert.py`: writes the Laya and strands-decider training formats.
6. Upload `data/backends_v4/` to `s3://<bucket>/data_v4/backends/` and
   `data/mixed_v4/` without `kdpii_test.jsonl` to
   `s3://<bucket>/data_v4/neutral/`, then run, once per seed:
   `python3 sagemaker/launch.py --backend laya --instance ml.g6.2xlarge --bucket <bucket> --role-arn <role> --data-prefix data_v4/ --hp laya_args="--loss soft-ce --grad-accum 2" --hp epochs=6 --hp seed=<seed> --hp skip_baseline=1 --wait`.
   Each v4 seed trained in about 98 minutes on `ml.g6.2xlarge` (one NVIDIA L4).
   Each job writes its source bundle and outputs under `s3://<bucket>/jobs/`;
   the launcher prints the model artifact, whose `laya-ko-pii/` directory is
   the checkpoint to bake into the image.
7. `indep/gen.py`, `indep/build.py` and `indep/audit.py`: build an independent
   evaluation set with other generator models, unseen genres and value formats,
   and audit its labels (`audit.py` also audits KDPII val and test). Score it with
   `indep/score_one.sh` and `eval/metrics.py`, and break it down with
   `indep/breakdown.py`.

Train several seeds and select one on KDPII val F1 (sentence rows, audit
disputes left out). Then run every gate once, on that seed only:

| Gate | Set | Threshold | v3s1 | v4s2 |
|---|---|---|---|---|
| G1 | KDPII test sentences | recall ≥ 0.90 and FPR ≤ 0.02 | 0.661, 0.034 | 0.988, 0.001 |
| G2 | KDPII test sentences | anonymous direct recall ≥ 0.90; name-only flagged ≤ 0.10 | 0.638, 0.153 | 0.997, 0.005 |
| G3 | Independent set v3 | F1 ≥ 0.94 and FPR ≤ 0.05 | 0.953, 0.036 | 0.962, 0.048 |
| G4 | Independent set, code genres | FPR ≤ 0.02 | 0.011 | 0.014 |
| G5 | `eval/probes.py` | all pass | 12 of 18 | 18 of 18 |
| G6 | KDPII test windows | recall ≥ 0.90 | 0.919 | 0.985 |

`eval/groups.py` scores KDPII rows by group (anonymous direct identifiers,
named positives, name-only negatives, categories). `eval/probes.py` sends 18
fixed texts to a running sidecar and exits 1 on any miss. On 2026-10-20, KDPII
val F1 was 0.989, 0.995 and 0.988 across three v4 seeds; seed 2 was selected.

The unit tests cover the data tools: `python3 -m unittest discover -s tests`.

## Request settings the checkpoint needs

- Laya: `"model": "multilingual"` in every request, with `LAYA_EXTRA_MODELS`
  mapping `multilingual` to the checkpoint directory.
- strands-decider: the training `criteria`
  `{"false": "no personal information about a specific person and no secret", "true": "contains personal information about a specific person or a secret credential"}`.

Generated data stays out of git (`data/` is ignored). It contains no real
person's data and is not published.
