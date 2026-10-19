# KDPII results for the Korean PII recipe

Metrics only. The dataset, the generated rows and the predictions stay out of
git (`examples/pii-classifier-ko/training/data/` is ignored).

**Dataset.** KDPII: A New Korean Dialogic Dataset for the Deidentification of
Personally Identifiable Information (KDPII DATASET REVISED, Zenodo, 2024,
doi:10.5281/zenodo.16759166), CC BY 4.0. `PII_dataset_V3.json`, md5
`419921a5e153e8346b6d320d5f084a4a`: 4,981 dialogues, 53,778 sentences.

**Rows.** `examples/pii-classifier-ko/training/datagen/kdpii.py`:
- splits dialogues 8:1:1 by `sha256(dialogue id) % 10` (train 0–7, val 8, test 9);
- drops val and test rows whose text also occurs in train;
- maps KDPII entity labels onto the recipe's label policy:
  - direct identifiers count on their own;
  - switchboard numbers (`15xx` to `19xx`) do not count;
  - an address counts on its own only with a number;
  - the policy's personal attributes (birth date or age, health or body measurements, religion) count only next to `PS_NAME`;
  - a name with only an employer, team, job title or school does not count, matching the synthetic negatives;
- writes one row per sentence and one row per window of consecutive sentences of at most 1,000 characters.

The test split has 492 dialogues, 5,207 sentence rows (447 positive) and 492 window rows (286 positive). The labels have not been audited yet (plan Task 6).

**Files.**

| File | What |
|---|---|
| `groups_v3s1.json` | `eval/groups.py` on the test sentence rows for the reference checkpoint `laya-ko-pii-v3s1` |

**v3s1 on the test split (2026-10-19, CPU, recipe image, threshold 0.5).**

| Rows | n | Accuracy | Recall | FPR | Precision | F1 |
|---|---|---|---|---|---|---|
| Sentences | 5,207 | 0.9359 | 0.6309 | 0.0355 | 0.6253 | 0.6281 |
| Windows | 492 | 0.7886 | 0.8671 | 0.3204 | 0.7898 | 0.8267 |

On sentences:
- anonymous direct identifiers: recall 0.605 (n 392);
- named positives: recall 0.818 (n 55);
- name-only negatives: flagged 0.158 (n 184);
- sentences without any entity: flagged 0.007 (n 3,353).

Probes (`eval/probes.py`): 12 of 18 pass. All six failures are direct identifiers without a name.
