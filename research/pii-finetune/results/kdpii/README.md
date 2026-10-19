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
  - attributes count only next to `PS_NAME`;
- writes one row per sentence and one row per window of consecutive sentences of at most 1,000 characters.

The test split has 492 dialogues, 5,207 sentence rows (485 positive) and 492 window rows (299 positive). The labels have not been audited yet (plan Task 6).

**Files.**

| File | What |
|---|---|
| `groups_v3s1.json` | `eval/groups.py` on the test sentence rows for the reference checkpoint `laya-ko-pii-v3s1` |

**v3s1 on the test split (2026-10-19, CPU, recipe image, threshold 0.5).**

| Rows | n | Accuracy | Recall | FPR | Precision | F1 |
|---|---|---|---|---|---|---|
| Sentences | 5,207 | 0.9312 | 0.5959 | 0.0343 | 0.6408 | 0.6175 |
| Windows | 492 | 0.7785 | 0.8428 | 0.3212 | 0.8025 | 0.8222 |

On sentences:
- anonymous direct identifiers: recall 0.605 (n 392);
- named positives: recall 0.559 (n 93), with name plus workplace or position at 0.091 (n 33);
- name-only negatives: flagged 0.158 (n 184);
- sentences without any entity: flagged 0.007 (n 3,353).

Probes (`eval/probes.py`): 12 of 18 pass. All six failures are direct identifiers without a name.
