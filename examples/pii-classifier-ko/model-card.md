# laya-ko-pii (revision laya-ko-pii-v4s2)

This card describes the reference checkpoint that the recipe in `training/`
produced. The checkpoint is not distributed. A checkpoint you train with the
recipe needs its own revision, evaluation and card; start from this one.

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

The answer is `answers.pii.noul`, the probability of "yes". The response's
`usage.truncated` and `usage.state_tokens_dropped` report whether the whole
text was read (see Limitations).

Without `"model": "multilingual"`, Laya routes Latin-script text to its stock
English checkpoint. An image built from `laya-serve/` has the matching server
settings.

## Label policy

See the training recipe in the inferplane repository
(`examples/pii-classifier-ko/training/README.md`), including rule B: an
employee number or vehicle plate together with a health or salary fact about
the same person is personal data.

## Training

- Base: `convaiinnovations/laya` multilingual (mmBERT-base encoder), Laya
  0.4.6, commit `c73908d`.
- Data: 29,110 training rows.
  - 14,555 synthetic rows (46% positive, 3,094 of them masked minimal-pair
    rows). Claude Sonnet 5.5 on Amazon Bedrock wrote 7,629 slot templates:
    the 4,464 of v3, 653 with direct identifiers and no name, and 2,512 short
    conversational texts. Values were generated locally, and 15% of phone,
    registration, account and card values were written as fullwidth digits,
    spaced characters or Korean digit words. These rows contain no real
    person's data and are not released.
  - 14,555 KDPII rows (13% positive; 13,316 sentences and 1,239 windows),
    sampled from the training dialogues of KDPII (CC BY 4.0, see NOTICE) and
    capped at half of the training set. The labels are KDPII's entity labels
    mapped onto the recipe's label policy. Validation and test dialogues are
    never trained on.
- Instructions: the trained statement plus four paraphrases (two Korean) as
  augmentation.
- Recipe: soft cross-entropy, 6 epochs, effective batch 16, seed 2, about 98
  minutes on one NVIDIA L4 (`ml.g6.2xlarge`).
- Selection: three seeds, chosen on KDPII validation F1 (0.989, 0.995,
  0.988) before any test or independent-set number was read.

## Evaluation

Two test sets, threshold 0.5, against the previous reference checkpoint
`laya-ko-pii-v3s1`. Rows whose labels a second model (Claude Sonnet 5.5)
disputed are left out of both.

- **KDPII test.** 492 held-out dialogues: 5,152 sentence rows (407 positive)
  and 447 windows of up to 1,000 characters (260 positive). They come from the
  same dataset as the KDPII training rows.
- **Independent set v3.** 2,236 synthetic rows written by four non-Anthropic
  generator models in 14 genres absent from training, with half of the values
  in unseen formats.

| Gate | Set | Threshold | v3s1 | v4s2 |
|---|---|---|---|---|
| G1 | KDPII test sentences: recall, FPR | ≥ 0.90, ≤ 0.02 | 0.661, 0.034 | 0.988, 0.001 |
| G2 | KDPII test sentences: direct identifier without a name recall, name-only flagged | ≥ 0.90, ≤ 0.10 | 0.638, 0.153 | 0.997, 0.005 |
| G3 | Independent set: F1, FPR | ≥ 0.94, ≤ 0.05 | 0.953, 0.036 | 0.962, 0.048 |
| G4 | Independent set, four code genres: FPR | ≤ 0.02 | 0.011 | 0.014 |
| G5 | 18 fixed probes | all pass | 16 | 18 |
| G6 | KDPII test windows: recall | ≥ 0.90 | 0.919 | 0.985 |

On the independent set:

| Model | F1 | Precision | Recall | FPR | AUROC | ECE |
|---|---|---|---|---|---|---|
| This checkpoint (v4s2) | 0.962 | 0.940 | 0.986 | 0.048 | 0.992 | 0.026 |
| laya-ko-pii-v3s1 | 0.953 | 0.953 | 0.953 | 0.036 | 0.993 | 0.031 |
| Laya multilingual, zero-shot (v1 set) | 0.535 | 0.463 | 0.633 | 0.617 | 0.550 | 0.454 |
| inferplane regex detectors | 0.478 | 0.504 | 0.454 | 0.345 | 0.555 | 0.432 |

The zero-shot row was measured on the earlier v1 independent set (1,679 rows),
whose labels predate rule B.

- KDPII test windows: FPR 0.016 (v3s1 0.305).
- Abstain rate with a 0.1 < p < 0.9 band: 1.8%.
- Known errors, on the independent set:
  - one-sided rule-B documents flagged: 20.6% (v3s1 14.5%);
  - documents with only lookalike tokens (order ids, UUIDs, hashes, masked
    values) flagged: 2.0%, 5 of 250 (v3s1 0.4%); two of them score above 0.99;
  - name-only documents flagged: 2.0%;
  - name-plus-attribute recall: 0.95;
  - FPR above 0.09 in two genres: civil complaints 0.105 and community posts
    0.095. It roughly doubled in delivery-app texts (0.033 to 0.078) and
    call-center transcripts (0.037 to 0.074).
- Known errors, on KDPII: a shop's or an office's landline is flagged. KDPII
  labels every landline as a phone number, and the audit judge disagreed on 25
  of the 66 test sentences that are positive only through a landline, reading
  them as not personal (for example a shop's or a department's number). Those
  rows are left out of the metrics. On the 41
  disputed test rows of this kind, v4s2 flags all 41 and v3s1 flagged 7.
- v4 trades precision for recall: independent-set FPR rose from 0.036 to
  0.048, close to the gate.
- v4s2 was selected on KDPII val, not on the independent set. v3s1 was
  selected on the independent set, so its 0.953 there is optimistic.
- Value formats on the independent set: standard formats recall 0.986, FPR
  0.043; unseen formats recall 0.985, FPR 0.054. KDPII brings spaced card
  numbers and lot-number addresses into training, so the second half is no
  longer fully unseen.
- KDPII numbers are in-distribution: test dialogues are unseen, but the
  dataset is the same one the checkpoint trained on. The independent set is
  the out-of-distribution check.
- Latency, one request: about 24 ms on an A10G. On CPU (16 vCPU), about
  450 ms per 1,000-character Korean window.

KDPII is real-style dialogue but not this gateway's traffic, and the
independent set is synthetic. Accuracy on real traffic is unknown until
measured. inferplane's rollout gates require a Shadow measurement on labeled
samples of real traffic before enforcement.

## Limitations

- **Windowed use.** inferplane reads long requests in windows of about 1,000
  characters. A name and an attribute in different windows, or in different
  turns of a conversation, are not read together.
- **Token budget.** The checkpoint's `max_len` is 1,024 tokens, which leaves
  about 980 for the text after the question. `laya-serve` cuts longer text
  without an error and reports the cut in `usage`; a caller must read it,
  because the score then covers only the beginning of the text. inferplane
  never treats a cut window as clear: it abstains unless the part that was
  read is flagged. 1,000 characters are about 600 tokens of Korean prose, 170
  of English prose and 370 of Go code, but up to 1,035 for phone or
  resident-number lists. Keep `LAYA_JEV_STRICT` off: it removes the truncation
  fields from `usage`.
- **Calibration.** Calibration was measured only on the synthetic independent set (ECE 0.026).
- **Abbreviation styles.** Korean abbreviations and mixed-script identifiers
  outside the training formats may be missed.

## License and lineage

- Apache-2.0.
- Derived from `convaiinnovations/laya` (Apache-2.0), whose multilingual
  encoder is `jhu-clsp/mmBERT-base` (MIT). See NOTICE.
