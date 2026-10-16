# Training recipe: Korean PII classifier for `systemone`

This recipe produced the `laya-ko-pii-v3s1` checkpoint that the sample image
serves. It needs an AWS account with Amazon Bedrock model access, one private
S3 bucket and a SageMaker execution role scoped to that bucket. It creates no
instance, public IP or security group: training runs as SageMaker training jobs
that end on completion.

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

It is false for:

- a name with no attribute;
- code, schemas or documents that handle personal-data fields without values;
- masked or placeholder values;
- organisation identifiers;
- lookalike tokens such as UUIDs, hashes and version strings;
- either side of a rule-B pair on its own.

## Steps

1. `datagen/gen_templates.py`: Claude on Amazon Bedrock writes slot templates
   (`⟦PHONE⟧`, …), never values.
2. `datagen/fill.py`: fills the slots locally with synthetic values, adds masked
   minimal pairs, and splits the data by template, with two genres held out.
3. `datagen/convert.py`: writes the Laya and strands-decider training formats.
4. Upload `data/` to `s3://<bucket>/data/`, then run:
   `python3 sagemaker/launch.py --backend laya --bucket <bucket> --role-arn <role> --hp laya_args="--loss soft-ce --grad-accum 2" --hp epochs=8 --hp seed=1`.
   Laya seed 1 trained in about 43 minutes on `ml.g6.2xlarge` (one NVIDIA L4).
5. `indep/gen.py`, `indep/build.py` and `indep/audit.py`: build an independent
   evaluation set with other generator models, unseen genres and value formats,
   and audit its labels. Score it with `indep/score_one.sh` and
   `eval/metrics.py`, and break it down with `indep/breakdown.py`.

Train several seeds and select on the independent set, not on the
same-generator test split. On 2026-10-14, independent-set F1 varied from 0.934
to 0.953 across three Laya seeds.

## Request settings the checkpoint needs

- Laya: `"model": "multilingual"` in every request, with `LAYA_EXTRA_MODELS`
  mapping `multilingual` to the checkpoint directory.
- strands-decider: the training `criteria`
  `{"false": "no personal information about a specific person and no secret", "true": "contains personal information about a specific person or a secret credential"}`.

Generated data stays out of git (`data/` is ignored). It contains no real
person's data and is not published.
