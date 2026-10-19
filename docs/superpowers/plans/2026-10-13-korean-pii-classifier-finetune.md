# Korean PII classifier fine-tuning (Laya and strands-decider)

Status: experiment plan, approved by the user on 2026-10-13 ("작업계획서를 만들어서
별도의 워크트리에서 finetuning까지 데이터생성부터 진행해 laya, strands-decider 모두 해보자.
aws resource를 사용해서 진행해"). This is research tooling. It changes no
gateway code and does not implement the `systemone` classifier plugin from
[the pluggable PII classifier spec](../specs/2026-10-07-pii-classifiers-design.md).

> **Published form (2026-10-18).** The research scripts ship, without account
> IDs, profiles or bucket names, as `examples/pii-classifier-ko/training/`.
> `research/pii-finetune/` keeps only its README and the result files. The
> reference checkpoint is Laya v3 seed 1, which trained on `ml.g6.2xlarge`
> (one NVIDIA L4) in about 43 minutes; see `examples/pii-classifier-ko/model-card.md`.
> It is not distributed (2026-10-19): the sample shares the recipe only.

## Goal

Measure whether fine-tuning a self-hosted typed decision model on synthetic
Korean PII data produces a usable `Flagged | Clear | Abstain` signal for the
`systemone` classifier type, for both backends the spec names:

- **Laya** (Convai Innovations, Apache-2.0): multilingual mmBERT-based checkpoint.
- **strands-decider** (Strands Labs, Apache-2.0): Qwen3.5-2B-Base torso with a
  pointer head.

The output is a measured comparison, not a rollout decision. The spec's rollout
gates (accuracy, false-positive cost, p95 latency in Shadow on real traffic)
still apply afterwards.

## Question and label policy

One `noul` (yes/no) question. Instruction:

> Does this text contain personal information about a specific person, or a
> secret credential?

The label is **true** when the text contains at least one of:

- a direct identifier on its own: resident registration number (주민등록번호),
  foreigner registration number, passport number, driver's license number,
  mobile or home phone number, personal email address, home address, bank
  account number, payment card number;
- a named person together with any attribute about them: birth date, health
  or diagnosis, salary or financial data, employee record, contact details;
- a secret credential: password, API key, access token or private key.

The label is **false** for:

- a person's name with no other attribute (commit authors, chat mentions);
- code, schemas, docs or questions that *handle* PII fields without values
  (`def mask_rrn(rrn: str)`, `ALTER TABLE users ADD phone varchar`);
- masked or placeholder values (`010-****-5678`, `user@example.com`,
  `000000-0000000`, `{{name}}`);
- organisation identifiers: business registration numbers, company
  switchboard numbers (`1588-xxxx`), order and invoice ids;
- lookalike tokens: UUIDs, hashes, base64, version strings, timestamps.

This policy mirrors how a gateway rule should behave on coding-assistant
traffic, where names and PII-handling code are common and a false positive
forces a request onto an internal model.

## Data generation (no real PII)

All personal values are synthetic and generated locally, so no dataset row and
no model weight contains a real person's data.

1. **Value generators** (`research/pii-finetune/datagen/kr_values.py`, stdlib
   only): Korean names, RRNs with a valid check digit, phone numbers, emails,
   road-name addresses, per-bank account formats, Luhn card numbers, passport
   and license numbers, diagnoses, salaries, credentials; plus the negative
   lookalikes listed above.
2. **Programmatic templates**: code (Python, Java, Go, TypeScript), SQL, JSON
   and CSV fixtures, logs and stack traces, git diffs, Korean chat and tickets.
3. **LLM slot templates** via Amazon Bedrock (Claude Sonnet 5.5 in the samples
   account): the model writes diverse documents per genre containing slot
   markers such as `⟦PHONE⟧`, never values. The generator fills slots locally,
   so labels are exact and the LLM never invents a real person's data. The same
   path writes hard negatives: PII-handling code and prose, and documents with
   only lookalike slots.
4. **English share**: about 10% English rows keep the question bilingual.
5. **Splits**: templates are disjoint across train, validation and test. Two
   genres are held out entirely for an out-of-distribution test.

Rows are stored once in a neutral JSONL (`text`, `label`, `categories`,
`genre`, `lang`, `source`, `template_id`) and converted per backend.

## Training (AWS, a dedicated sample account, us-east-1)

- **Storage**: one private S3 bucket (block public access, SSE-S3) for
  datasets, checkpoints and reports.
- **Compute**: SageMaker training jobs, so no instance, public IP or security
  group ingress is created. One IAM execution role scoped to that bucket.
  - strands-decider: `ml.g6e.xlarge` (L40S 48 GB). Trained from
    `Qwen/Qwen3.5-2B-Base` with the reference recipe (LoRA r=16, pointer head,
    `kl_frozen_weight: 0.3`), mixing the repository's committed Apache-2.0
    synthetic rows as replay. The repository has no continued fine-tune from
    a released checkpoint (`init_from` retrains the head only).
  - Laya: `ml.g5.2xlarge` (A10G 24 GB), the multilingual checkpoint, using
    Laya's own training and calibration entry points.
- **Calibration**: each backend's own temperature fit on the calibration split.

## Evaluation

On the in-distribution test split and the held-out-genre split, for each
backend's zero-shot base model and its fine-tuned model, plus inferplane's
existing regex detectors as a baseline:

- accuracy, precision, recall, F1, AUROC;
- Brier score and ECE;
- recall per category, false-positive rate on hard negatives;
- the Abstain rate for a candidate band (0.1 < p < 0.9);
- latency per request on the training GPU (indicative only).

## Deliverables

- `research/pii-finetune/`: generators, converters, SageMaker launchers,
  evaluation scripts, README.
- S3: datasets, checkpoints, calibrated models, evaluation reports.
- A results section appended to this plan, and a proposed update to the
  spec's Korean row and `systemone` wire table (not applied without review).

## Out of scope

- Implementing the `systemone`, `presidio` or `generic` gateway plugins.
- Real traffic or real PII of any kind.
- Publishing datasets or weights.
- Leaving GPU resources running: training jobs end on completion.

## Steps

1. Worktree, plan, AWS identity check. Done.
2. Value generators with the label policy. Done. Programmatic templates were
   dropped: the Bedrock slot templates covered every genre with exact labels.
3. Bedrock slot-template generation; fill; split; neutral JSONL. Done.
4. Converters to strands-decider and Laya formats; replay mix. Done.
5. S3 buckets (us-east-1 and us-west-2, for GPU capacity) and IAM role. Done.
6. Zero-shot baselines for both backends. Done, inside the training jobs.
7. Fine-tune both backends. Done.
8. Calibrate and evaluate. Done.
9. Results and clean-up. Done: no training job is running.

## Results (2026-10-13)

### Data

- 600 Bedrock calls (`us.anthropic.claude-sonnet-5-5`) produced 3,597 slot
  templates over 25 genres and 6 kinds. Only 5 templates contained a literal
  value, all of them placeholders (`010-1234-5678`, `0000-0000-0000-0000`).
- v1 splits: train 5,671 rows, val 400, test 397, and OOD 329 (the held-out
  `insurance_claim` and `terminal_output` genres). Positives are about 45%.
- v2 changes only the training split: a masked minimal pair for every eligible
  template (1,166 pairs instead of 416), and English rows oversampled 3x
  (7,487 rows). Val, test and OOD are the v1 files, so all rows below are
  scored on identical data.
- strands-decider training adds 4,000 replay rows from the repository's
  committed Apache-2.0 synthetic files.

### Scores

Probabilities come from each backend's `/v1/systemone` server, one request per
row: the same request a gateway `systemone` plugin would send. The threshold is
0.5, and Abstain means 0.1 < p < 0.9. Prediction files are in
`research/pii-finetune/results/preds/`, and full metrics (per category, per
negative kind, per language) are in `results/metrics_{test,ood}.json`.

| Model | Test F1 | Test FPR | Test AUROC | OOD F1 | OOD FPR | OOD AUROC | OOD ECE | OOD abstain |
|---|---|---|---|---|---|---|---|---|
| Gateway regex (`internal/sensitivity`) | 0.540 | 0.531 | 0.547 | 0.542 | 0.530 | 0.548 | 0.462 | 0 |
| Laya multilingual, zero-shot | 0.585 | 0.750 | 0.626 | 0.574 | 0.741 | 0.575 | 0.490 | 0.112 |
| Laya, v1 data, default (rlcd, 4 epochs, batch 64) | 0.916 | 0.116 | 0.977 | 0.868 | 0.195 | 0.960 | 0.098 | 0.052 |
| Laya, v1 data, soft-ce, 8 epochs, batch 16 | 0.988 | 0.009 | 1.000 | 0.947 | 0.081 | 0.994 | 0.046 | 0.030 |
| Laya, v2 data, default | 0.979 | 0.000 | 1.000 | 0.940 | 0.032 | 0.994 | 0.029 | 0.043 |
| Laya, v2 data, soft-ce, seed 2 | 0.986 | 0.018 | 1.000 | 0.966 | 0.054 | 0.999 | 0.033 | 0.036 |
| Laya, v2 data, soft-ce, seed 3 | 0.986 | 0.009 | 1.000 | 0.973 | 0.038 | 0.999 | 0.022 | 0.003 |
| strands-decider v1-2610, zero-shot | 0.278 | 0.112 | 0.715 | 0.374 | 0.092 | 0.736 | 0.145 | 0.903 |
| strands-decider, v1 data, request without `criteria` | 0.943 | 0.094 | 0.990 | 0.919 | 0.119 | 0.974 | 0.197 | 0.948 |
| strands-decider, v1 data | 0.991 | 0.013 | 1.000 | 0.969 | 0.038 | 0.987 | 0.025 | 0.012 |

The regex baseline is not a like-for-like competitor. It treats IPv4 addresses
and every email address, `example.com` included, as personal data, which this
label policy does not. Its per-category recall also counts documents flagged
for an unrelated match.

Latency per request, p50 / p95, on the serving path: Laya 23.8 / 24.7 ms on an
A10G (ml.g5.2xlarge) and 206 / 296 ms on a 16-vCPU Graviton host;
strands-decider 63.1 / 73.5 ms on an L4 (ml.g6.4xlarge) and 2.2 / 3.0 s on the
same CPU host; regex 0.2 ms. These are single-request figures with no batching.

### Findings

- **Fine-tuning is required for both backends.** Zero-shot, neither beats the
  regex baseline on this policy. Fine-tuned, both reach OOD F1 of 0.97 with a
  calibrated output (OOD ECE 0.02 to 0.03) and an Abstain rate near 1%.
- **Masked minimal pairs mattered more than training settings.** On v1 data the
  default Laya run flagged 51% (test) and 58% (OOD) of masked-pair negatives.
  The v2 pairs cut both to 0% under the same settings. strands-decider reached
  2% on v1 data.
- **Laya's default recipe can stall.** The default run sat on the ln 2 loss
  plateau for two epochs before leaving it. soft-ce with a smaller effective
  batch (2,640 updates instead of 332) converged in the first epoch and was
  stable across two seeds.
- **The request must match training.**
  - Laya routes Latin-script text to its stock English checkpoint unless the
    request names `"model": "multilingual"`. Without it, English rows were
    scored by an untrained model (English accuracy 0.61 instead of 0.97).
  - strands-decider renders a `noul` question with the server's default
    option descriptions unless the request sends the training `criteria`.
    Without them the ranking survives (AUROC 0.990) but probabilities collapse
    toward 0.5 (Abstain 92%, ECE 0.18).
- **Remaining error: name-only documents in unseen genres.** 12–15% of OOD
  `neg_name_only` rows are flagged by the best models. The masked-pair errors
  that remain sit on a label-policy boundary: a birth date with a diagnosis, or
  a vehicle plate, once the name is masked.
- **Cost and size.** Laya is 322M parameters and served about 2.7x faster on
  GPU than the 2B strands-decider; on CPU it is the only one of the two that
  fits a per-request budget. Total GPU time for every job was 4.1 h on
  ml.g6.4xlarge and 2.5 h on ml.g5.2xlarge.

### Limits

- All rows are synthetic, built from one generator model's templates and one
  value generator. The OOD split holds out genres, not the generator. These
  results do not establish accuracy on real traffic. The spec's Shadow-mode
  measurement on real requests is still the rollout gate.
- One seed for strands-decider (4 h per run on an L4). The strands-decider v2
  run waited 4 h for GPU capacity and was stopped unstarted.
- The label policy is this plan's. Quasi-identifiers without a name (birth
  date with diagnosis, vehicle plate) are negatives here; an operator may
  decide otherwise, which needs relabelled data.

### Independent evaluation set (2026-10-14)

The test and OOD splits came from the same generator as training, so this set changes the generator, the genres, and the prompt. Tooling is in `research/pii-finetune/indep/`.

- **Generators.** Four non-Anthropic model families on Bedrock:
  - `us.openai.gpt-6.1-sol`
  - `deepseek.v3.2`
  - `us.moonshotai.kimi-k3`
  - `mistral.mistral-large-3-675b-instruct`

  The prompt was written separately from `gen_templates.py`. The slot contract and label policy are unchanged.
- **Genres.** 14 genres that are not in training, among them real-estate contracts, resumes, call-center STT, civil complaints, school records, delivery apps, issue trackers, notebooks, CI pipelines, legal memos, community posts, and English postmortems and CRM notes. A code assertion rejects any overlap with the training genres.
- **Value formats.** Half the rows use formats absent from training:
  - phone numbers as `+82` or `(010)`;
  - resident registration numbers written with spaces or in words;
  - company email domains and `at … dot` spellings;
  - jibun-style street addresses;
  - compound surnames;
  - AWS keys and `glpat-` tokens.
- **Size.** 1,529 templates passed validation. 18 were dropped for writing a phone number or email outside a slot. The 5-gram Jaccard overlap with the nearest training template is p50 0.03, p95 0.08, max 0.24. The result is 1,702 rows: 46% positive, 191 masked pairs.
- **Label audit.** Claude Sonnet 5.5 relabelled every row against the written policy and disagreed on 23 rows (1.4%). I read all 23:
  - 10 are real label errors. The generator wrote a literal name, address, password, or token outside a slot, which mostly turned masked pairs into true positives. The leak regex does not catch Korean names or addresses.
  - 12 expose a limit of the slot rule. A name plus an attribute anywhere in the document counts as positive even when the attribute belongs to someone else or no one.
  - 1 is a misused slot: a version number in a salary position.

  The 23 rows are excluded from the clean set below. They are not relabelled.

Results on the clean set (1,679 rows). All models were scored on CPU with the same requests as before:

| Model | F1 (95% bootstrap) | Precision | Recall | FPR | AUROC | ECE |
|---|---|---|---|---|---|---|
| regex (`internal/sensitivity`) | 0.525 | 0.510 | 0.542 | 0.437 | 0.552 | 0.447 |
| Laya zero-shot | 0.535 | 0.463 | 0.633 | 0.617 | 0.550 | 0.454 |
| Laya v2 soft-ce seed 2 | 0.959 (0.948–0.968) | 0.951 | 0.966 | 0.042 | 0.994 | 0.030 |
| Laya v2 soft-ce seed 3 | 0.941 (0.929–0.953) | 0.961 | 0.923 | 0.032 | 0.990 | 0.048 |
| strands-decider v1 | 0.963 (0.952–0.972) | 0.966 | 0.960 | 0.028 | 0.992 | 0.027 |

On the full set (1,702 rows), F1 changes by at most 0.004.

- **Transfer.** Fine-tuning transfers to other generators and unseen genres. F1 falls 0.01–0.03 from the same-generator OOD split. The zero-shot models and regex stay near 0.53.
- **Laya seeds.** The seed-3 Laya checkpoint was the best on the same-generator OOD split (0.973) and is the worse of the two here (0.941). Seed 2 has higher recall on name-plus-attribute rows (0.93 vs 0.83) and on health (0.87 vs 0.78). Picking a checkpoint by one same-generator split overfits to that split. Use several seeds and this set.
- **strands-decider vs Laya seed 2.** The F1 difference is within noise: the bootstrap CI of strands minus Laya seed 2 is −0.007 to +0.016.
- **Value formats.** No model lost accuracy on unseen formats. Alt-format accuracy is equal to or above the standard formats for every model.
- **Generator spread.** Per-generator accuracy ranges from 0.94 to 0.98 for the fine-tuned models, lowest on Mistral and DeepSeek.
- **Remaining errors.** The same two classes as before:
  - name-only documents read as positive: FPR 0.08–0.12;
  - name-plus-attribute documents read as negative: recall 0.83–0.93.

  More data of the same kind will not fix either class. The label rule for these boundary cases has to be decided first, by an operator or the spec, and the data relabelled.

### Label rule B and v3 (2026-10-14/16)

**Rule B.** An internal identifier (employee number or vehicle plate) together with a health or salary fact about the same person in one document is personal data, even without a name. Either side alone is not: an identifier with no sensitive fact, or a diagnosis or salary stated as an aggregate, policy or range.

- **Training data v3.** New Bedrock slot templates for the two rule-B kinds (`pos_linked`, `neg_linked_partial`) were added to the v2 data. Train 8,168 rows (42% positive, 1,512 masked pairs, 120 of them rule-B pairs), val 625, test 626, OOD 525. strands-decider again adds 4,000 replay rows.
- **Independent set v3.** The same four non-Anthropic generators and prompt as above, extended with the rule-B kinds. 1,960 templates passed validation (19 dropped for a phone or email outside a slot), producing 2,272 rows: 44% positive, 308 masked pairs, 1,113 alternate-format rows. 5-gram overlap with training is unchanged (p95 0.078). The Claude Sonnet 5.5 relabel disagreed on 36 rows, which are excluded below (2,236 clean rows).
- **Runs.** Laya soft-ce, 8 epochs, seeds 1–3; strands-decider 2 epochs, seed 0. All on CPU with the same requests as before.

Results on the clean v3 set (`eval/metrics.py`, threshold 0.5, abstain band 0.1 < p < 0.9):

| Model | F1 | Precision | Recall | FPR | AUROC | ECE | Abstain |
|---|---|---|---|---|---|---|---|
| regex (`internal/sensitivity`) | 0.478 | 0.504 | 0.454 | 0.345 | 0.555 | 0.432 | 0 |
| Laya v3 seed 1 | 0.953 | 0.953 | 0.953 | 0.036 | 0.993 | 0.031 | 0.023 |
| Laya v3 seed 2 | 0.934 | 0.912 | 0.958 | 0.071 | 0.987 | 0.048 | 0.026 |
| Laya v3 seed 3 | 0.939 | 0.924 | 0.955 | 0.061 | 0.980 | 0.041 | 0.022 |
| strands-decider v3 | 0.961 | 0.952 | 0.970 | 0.038 | 0.993 | 0.026 | 0.025 |

Per kind (`indep/breakdown.py`):

| Kind | Laya v3 seed 1 | strands-decider v3 |
|---|---|---|
| `neg_linked_partial` FPR | 0.145 | 0.061 |
| `neg_name_only` FPR | 0.033 | 0.135 |
| `pos_attr` recall | 0.869 | 0.941 |
| `pos_linked` recall | 0.995 | 0.985 |

- **Rule B was learned.** `pos_linked` recall rose from 0.82 (v2 seed 2, scored on this set) to 0.97–1.00. The one-sided negatives remain the largest error: FPR 0.06–0.26, down from 0.54 for v2.
- **Seed variance is real for Laya.** A paired bootstrap of F1 (2,000 resamples) gives seed 1 minus seed 2 = +0.018 (95% CI +0.009 to +0.028). Seed 1 was chosen on this same set, so its 0.953 is optimistic.
- **strands-decider vs Laya seed 1 is within noise.** The difference is +0.008 (95% CI −0.003 to +0.020). Their errors run in opposite directions: Laya over-flags one-sided identifier documents, while strands-decider over-flags name-only documents.
- **Length.** With the Laya tokenizer, Korean is 0.70 tokens per character (p95 0.79) and English 0.27. Documents are p50 424 and p95 974 characters. Only 6 of 2,272 exceed the 1,024-token multilingual limit.
- **CPU latency.** On the same 16-vCPU host, the full-document requests above took Laya seed 1 p50 369 / p95 805 ms and strands-decider p50 2.2 / p95 3.9 s. `POST /v1/systemone/batch` with Korean windows of 800–1,000 characters scales almost linearly: p50 448 ms (1 window), 1.0 s (2), 2.4 s (4), 4.3 s (8) and 7.9 s (16). A CPU sidecar therefore fits short new input only; long tool results need a GPU (24 ms per request on an A10G, measured above).

### Proposed spec changes (not applied)

For [the pluggable PII classifier spec](../specs/2026-10-07-pii-classifiers-design.md):

1. **Korean row.** Replace "Not solved by Laya" with: fine-tuned Laya
   multilingual and fine-tuned strands-decider both detect synthetic Korean
   PII (OOD F1 0.97) and are candidates for Shadow evaluation; presidio and
   generic remain the alternatives.
2. **`systemone` wire table.**
   - strands-decider has only `POST /v1/systemone`; Laya also has
     `/v1/systemone/batch` (up to 64 states).
   - The plugin needs optional `model` and `criteria` settings that are sent
     verbatim with every question, because both backends answer a different
     question when they are absent.
   - Neither backend returns an abstention by default. Laya returns one only
     when the request sends `min_confidence`. The verdict rule needs its own
     band (for example 0.1 < p < 0.9 → Abstain).
3. **Revision label.** Record the checkpoint and the request settings
   (`model`, `criteria` digest) in `revision`, since a change to either
   changes the classifier.
