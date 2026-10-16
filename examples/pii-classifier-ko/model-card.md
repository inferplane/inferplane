---
license: apache-2.0
language: [ko, en]
base_model: convaiinnovations/laya
library_name: laya
pipeline_tag: text-classification
tags: [pii, korean, privacy, typed-decision, inferplane]
---

# laya-ko-pii (revision laya-ko-pii-v3s1)

A fine-tune of the Laya multilingual typed-decision checkpoint that answers one
yes/no question: does this text contain personal information about a specific
person, or a secret credential? It is tuned for Korean coding-assistant
traffic, and it is the routing signal of inferplane's opt-in `systemone`
classifier sample.

## Intended use

- A gateway-side signal that keeps a request on an internal model (or blocks
  it) when personal data is likely present.
- Not a guarantee of PII detection, not a redaction tool (it outputs no
  spans), and not a basis for decisions about individuals.

## How to use

Serve it with `laya-serve` (Laya 0.4.6):
`LAYA_MODELS=multilingual LAYA_DEFAULT_MODEL=multilingual LAYA_EXTRA_MODELS='{"multilingual": "<dir>"}' laya-serve`.
Every request must carry `"model": "multilingual"` and the trained statement:

    POST /v1/systemone
    {"model": "multilingual", "state": "<text>",
     "questions": {"pii": {"type": "noul", "instructions": "Does this text contain personal information about a specific person, or a secret credential?"}}}

The answer is `answers.pii.noul`, the probability of "yes".

Without `"model": "multilingual"`, Laya routes Latin-script text to its stock
English checkpoint. The image `ghcr.io/inferplane/laya-ko-pii` contains this
revision and the right settings.

## Label policy

See the training recipe in the inferplane repository
(`examples/pii-classifier-ko/training/README.md`), including rule B: an
employee number or vehicle plate together with a health or salary fact about
the same person is personal data.

## Training

- Base: `convaiinnovations/laya` multilingual (mmBERT-base encoder), Laya
  0.4.6, commit `c73908d`.
- Data: 8,168 synthetic training rows (42% positive, 1,512 masked minimal
  pairs). Claude Sonnet 5.5 on Amazon Bedrock wrote slot templates; values were
  generated locally. The data contains no real person's data and is not
  released.
- Instructions: the trained statement plus four paraphrases (two Korean) as
  augmentation.
- Recipe: soft cross-entropy, 8 epochs, effective batch 16, seed 1, about 43
  minutes on one NVIDIA L4 (`ml.g6.2xlarge`).

## Evaluation

An independent synthetic set of 2,236 rows (after excluding 36 disputed
labels), written by four non-Anthropic generator models in 14 genres absent
from training, with half of the values in unseen formats. Threshold 0.5:

| Model | F1 | Precision | Recall | FPR | AUROC | ECE |
|---|---|---|---|---|---|---|
| This checkpoint | 0.953 | 0.953 | 0.953 | 0.036 | 0.993 | 0.031 |
| Laya multilingual, zero-shot (v1 set) | 0.535 | 0.463 | 0.633 | 0.617 | 0.550 | 0.454 |
| inferplane regex detectors | 0.478 | 0.504 | 0.454 | 0.345 | 0.555 | 0.432 |

The zero-shot row was measured on the earlier v1 independent set (1,679 rows),
whose labels predate rule B.

- Abstain rate with a 0.1 < p < 0.9 band: 2.3%.
- Known errors:
  - one-sided rule-B documents flagged: 14.5%;
  - name-only documents flagged: 3.3%;
  - name-plus-attribute recall: 0.87.
- Seed variance: two other seeds of the same recipe scored F1 0.934 and 0.939.
  This seed was selected on the same set, so 0.953 is optimistic.
- Latency, one request: about 24 ms on an A10G. On CPU (16 vCPU), about
  450 ms per 1,000-character Korean window.

All evaluation data is synthetic and document-level. Accuracy on real traffic
is unknown until measured. inferplane's rollout gates require a Shadow
measurement on labeled samples of real traffic before enforcement.

## Limitations

- **Windowed use.** inferplane reads long requests in windows of about 1,000
  characters. A name and an attribute in different windows, or in different
  turns of a conversation, are not read together.
- **Calibration.** The model is calibrated only on synthetic data (ECE 0.031).
- **Abbreviation styles.** Korean abbreviations and mixed-script identifiers
  outside the training formats may be missed.

## License and lineage

- Apache-2.0.
- Derived from `convaiinnovations/laya` (Apache-2.0), whose multilingual
  encoder is `jhu-clsp/mmBERT-base` (MIT). See NOTICE.
