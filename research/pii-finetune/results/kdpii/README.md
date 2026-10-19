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

The test split has 492 dialogues, 5,207 sentence rows and 492 window rows.

**Audit.** `indep/audit.py` asked Claude Sonnet 5.5 (Amazon Bedrock) for a second label on every val and test row under the same written policy.
- Test: 93 disagreements and 7 unparsable answers out of 5,699 rows.
- Val: 93 and 6 out of 5,674.

Both rates are 1.6%, the same as for the v3 independent set. Those rows are listed in `disputed_test.txt` and `disputed_val.txt` and are left out of every metric here. The most common disagreement is a business landline the judge reads as not personal (41 test rows). After exclusion, test has 5,152 sentence rows (407 positive) and 447 window rows (260 positive).

**Files.**

| File | What |
|---|---|
| `groups_v3s1.json` | `eval/groups.py` on the test sentence rows, disputes excluded, for the reference checkpoint `laya-ko-pii-v3s1` |
| `disputed_test.txt`, `disputed_val.txt` | Row ids left out after the audit |

**v3s1 on the test split (2026-10-19, CPU, recipe image, threshold 0.5).**

| Rows | n | Accuracy | Recall | FPR | Precision | F1 |
|---|---|---|---|---|---|---|
| Sentences | 5,152 | 0.9420 | 0.6609 | 0.0339 | 0.6256 | 0.6428 |
| Windows | 447 | 0.8255 | 0.9192 | 0.3048 | 0.8074 | 0.8597 |

On sentences:
- anonymous direct identifiers: recall 0.638 (n 356);
- named positives: recall 0.824 (n 51);
- name-only negatives: flagged 0.153 (n 183);
- sentences without any entity: flagged 0.007 (n 3,351).

Probes (`eval/probes.py`): 12 of 18 pass. All six failures are direct identifiers without a name.
