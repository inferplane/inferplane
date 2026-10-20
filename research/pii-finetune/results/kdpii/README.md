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

Both rates are 1.6%, the same as for the v3 independent set, but the disagreements cluster by entity type. 41 of the 93 test disagreements are rows positive only through a landline that the judge reads as a business or office number. Among test sentences positive only through a landline, it disagreed on 25 of 66; training holds 514 such sentences. Those rows are listed in `disputed_test.txt` and `disputed_val.txt` and are left out of every metric here. The most common disagreement is a business landline the judge reads as not personal (41 test rows). After exclusion, test has 5,152 sentence rows (407 positive) and 447 window rows (260 positive).

**Files.**

| File | What |
|---|---|
| `groups_v3s1.json` | `eval/groups.py` on the test sentence rows, disputes excluded, for the earlier reference checkpoint `laya-ko-pii-v3s1` |
| `groups_v4.json` | The same for the v4 reference checkpoint `laya-ko-pii-v4s2` |
| `../indep/metrics_v3set.json` | `eval/metrics.py` on independent set v3 (36 disputes excluded) for both checkpoints |
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

Probes (`eval/probes.py`): 16 of 18 pass; both failures are mobile numbers without a name. The probe values were regenerated on 2026-10-20 so that none occurs in KDPII. With the earlier values, which came from KDPII, 12 of 18 passed.

**v4s2 (2026-10-20, CPU, recipe image, threshold 0.5).** The v4 recipe trains on
14,555 KDPII training rows next to 14,555 synthetic rows. Three seeds scored
KDPII val sentence F1 0.9893, 0.9953 and 0.9881, and seed 2 was selected
before any test or independent-set number was read. Every gate then ran once:

| Rows | n | Accuracy | Recall | FPR | Precision | F1 |
|---|---|---|---|---|---|---|
| Sentences | 5,152 | 0.9981 | 0.9877 | 0.0011 | 0.9877 | 0.9877 |
| Windows | 447 | 0.9843 | 0.9846 | 0.0160 | 0.9884 | 0.9865 |

On sentences:
- anonymous direct identifiers: recall 0.997 (n 356);
- named positives: recall 0.922 (n 51);
- name-only negatives: flagged 0.005 (n 183);
- sentences without any entity: flagged 0.000 (n 3,351).

| Gate | Threshold | v3s1 | v4s2 |
|---|---|---|---|
| G1 KDPII test sentences: recall, FPR | ≥ 0.90, ≤ 0.02 | 0.661, 0.034 | 0.988, 0.001 |
| G2 anonymous direct recall, name-only flagged | ≥ 0.90, ≤ 0.10 | 0.638, 0.153 | 0.997, 0.005 |
| G3 independent set v3: F1, FPR | ≥ 0.94, ≤ 0.05 | 0.953, 0.036 | 0.962, 0.048 |
| G4 independent set code genres (CI pipeline, notebook output, issue tracker, incident postmortem): FPR | ≤ 0.02 | 0.011 (4/367) | 0.014 (5/367) |
| G5 probes | all 18 | 16 | 18 |
| G6 KDPII test windows: recall | ≥ 0.90 | 0.919 | 0.985 |

The test dialogues are unseen, but they come from the dataset v4 trains on, so
the KDPII numbers are in-distribution. On the independent set, v4s2 raises
recall from 0.953 to 0.986 and FPR from 0.036 to 0.048: one-sided rule-B
documents are flagged 0.206 (v3s1 0.145), lookalike-only documents 5 of 250
(v3s1 1 of 250), and civil-complaint and community-post texts 0.105 and 0.095.
On the 41 disputed landline rows, v4s2 flags all 41 and v3s1 flagged 7.
