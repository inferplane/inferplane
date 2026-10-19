# Korean PII Recipe v4 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Retrain the sample's Laya checkpoint so that it detects direct identifiers that appear without a person's name, including in short conversational text, without losing its low false-positive rate on coding traffic. Measure the result on real Korean dialogue (KDPII).

**Architecture:** The recipe under `examples/pii-classifier-ko/training/` gains five things:
- a KDPII importer that splits by dialogue and maps the dataset's entity labels onto the recipe's label policy;
- template kinds and a short-text length profile for anonymous identifiers;
- digit obfuscation when filling templates;
- a mixer that adds KDPII training dialogues to the synthetic splits;
- evaluation tools (per-group KDPII metrics, a probe gate, per-genre breakdown).

The gateway code does not change. The classifier keeps the same instruction string and `model: multilingual`, so only the sample's `revision` moves.

**Tech Stack:** Python 3.9+ standard library for all new data and evaluation code (tests use `unittest`). The existing boto3 scripts keep generating templates (Amazon Bedrock), auditing labels and launching SageMaker jobs. Laya 0.4.6 (commit `c73908d`) trains on SageMaker, and the recipe image (`examples/pii-classifier-ko/laya-serve/Dockerfile`) is used for scoring.

**Spec:** No separate spec exists for v4. The authority is `docs/superpowers/specs/2026-10-16-systemone-korean-pii-sample-design.md` plus the decisions table below, which the user made on 2026-10-19. The evidence behind this plan is the comparison report https://claude.ai/artifact/4msABkjo4fF2jfgwkqCMDy, which is private to its owner; its numbers are restated under "Evidence".

## Decisions (user, 2026-10-19)

| Question | Decision |
|---|---|
| Use the Notion experiment's model 3 or its code | Not available. Strengthen this recipe using the methods the Notion page describes: iterative error analysis, KDPII mixing, obfuscated values, a selection rule on a real-dialogue split. |
| Label policy | Keep the recipe's policy (`training/README.md`, "Label policy"). Make direct identifiers without a name reliably positive. A name alone stays negative. |
| KDPII | Mix it into training through a dialogue-level 8:1:1 split. Test dialogues are evaluation-only. Attribute it in `NOTICE` (CC BY 4.0). |
| Publication | Unchanged: recipe only. No weights, image or data are published (2026-10-19). |

## Evidence (from the report)

- KDPII V3, random 10,000 sentences (seed 20261019), labels reconstructed to match the Notion set's 16.7% positive rate:
  - v3s1: accuracy 88.58%, recall 40.8%, FPR 1.95%;
  - regex plus v3s1: recall 52.6%;
  - Notion model 3: 98.45% accuracy, recall 94.4%, FPR 0.74%, on its own 4,891-sentence split.
- Under a mapping that approximates this recipe's own policy, v3s1 recall is 53.1%, so most of the gap is the model, not the policy.
- Probes show name dependence:
  - `김민수 010-4056-6702` scores 0.990;
  - `010-4056-6702로 문자 하나만 넣어주세요.` scores 0.073;
  - `고객 연락처: 010-4056-6702` scores 0.116;
  - a bare resident number scores 0.107;
  - `제 이메일은 laze1@naver.com 입니다.` scores 0.073.
- Root cause in the recipe: `gen_templates.py` fixes document length at 40 to 300 words, and `pos_direct` templates may use `⟦NAME⟧`. In the v3 independent set, 288 of the 321 `pos_direct` rows have a name slot, and the shortest row is 150 characters. `kr_values.label` already makes a direct identifier positive on its own; the data never taught it.
- What v3s1 does well, and must keep: FPR 1.1% (4/367) on the independent set's code genres, and 0.8% on KDPII sentences without any entity.

## Global Constraints

- Recipe only: commit no weights, images, generated data, KDPII copies or predictions. Commit metrics only. Everything generated stays under `examples/pii-classifier-ko/training/data/`, which is git-ignored.
- Keep the instruction string `Does this text contain personal information about a specific person, or a secret credential?` and `"model": "multilingual"`. These are the plugin's defaults; changing them changes gateway config.
- Laya 0.4.6, commit `c73908d`, `max_len` 1024. The gateway window is `window_runes: 1000`.
- KDPII: `PII_dataset_V3.json` from Zenodo record 16759166, md5 `419921a5e153e8346b6d320d5f084a4a`, CC BY 4.0.
  - Split by `sha256(dialogue id) % 10` into train (0–7), val (8) and test (9).
  - A val or test row whose text also occurs in train is dropped.
- The independent set v3 (`data/indep/rows_v3.jsonl`, 36 disputed ids excluded) is a gate, never a selection set.
- Committed files contain no account IDs, bucket names, role ARNs or profile names. Commands use `<bucket>`, `<role-arn>` and `--profile <profile>`.
- New Python code uses only the standard library and runs on Python 3.9. No `match` statements and no runtime `X | Y` unions.
- Template generation on Amazon Bedrock, label auditing, and SageMaker training each need the user's explicit approval before launch. Each costs money.
- Every commit is signed off with `git commit -s`.

## Review Focus

1. **KDPII leakage across splits.** The same sentence occurs in several dialogues (1,115 sentences, 322 distinct texts). A test row whose text is also in train must not be scored. Pinned by `test_build_drops_cross_split_duplicates` (Task 1).
2. **Lookalike over-flagging.** Training on anonymous identifiers can teach "any number is personal". Normal tokens must stay negative: order ids, UUIDs, `1588`/`1661` switchboard numbers, masked values, masking code. Pinned by the negative probes (Task 5) and gate G4 (Task 7).
3. **Mapping edge cases.** A district without a house number (`종로구`) is not a home address. A `15xx`/`16xx`/`18xx` switchboard number is not a personal phone. A name with an attribute in another sentence of the same window is positive in the window row and negative in the sentence rows. Pinned by the mapping and window tests (Task 1).
4. **Window length.** Every KDPII window row stays within 1,000 runes. Otherwise the server cuts the window, and the training label would describe text the model never read. Pinned by `test_windows_stay_within_runes` (Task 1).
5. **Selecting on test.** Seed selection uses KDPII val only. KDPII test and the independent set are scored once, for the selected seed. Pinned by the selection procedure in Task 7, Step 6, which names the files it reads.

---

## File Structure

All paths are relative to `examples/pii-classifier-ko/training/` unless they start with the repo root.

| File | Responsibility |
|---|---|
| `datagen/kdpii.py` (create) | Download and verify KDPII; map entity labels onto the label policy; split by dialogue; write sentence and window rows |
| `datagen/mix.py` (create) | Add a capped share of KDPII train rows to the synthetic train split, and KDPII val to val; keep KDPII test separate |
| `datagen/gen_templates.py` (modify) | New kind `pos_direct_anon`, two short-text genres, a `--length short` profile, prompt construction as a testable function, lazy boto3 import |
| `datagen/fill.py` (modify) | Optional digit obfuscation (`--obf-rate`) for phone, resident, foreigner, account and card values |
| `eval/groups.py` (create) | Overall metrics and per-group recall/FPR for KDPII prediction files |
| `eval/probes.jsonl`, `eval/probes.py` (create) | A fixed probe set and a gate that exits 1 when any probe fails |
| `indep/breakdown.py`, `eval/metrics.py` (modify) | Add a per-genre group; let metrics leave out disputed ids |
| `tests/test_*.py` (create) | `unittest` tests for the new code |
| `README.md`, `../NOTICE`, `../model-card.md`, `../README.md`, `../README.ko.md` (modify) | v4 steps, KDPII attribution, v4 card and evidence |
| repo `examples/config.pii-classifier-ko.json`, `examples/helm.pii-classifier-ko.yaml`, `cmd/mayu/pii_classifier_example_test.go`, `CHANGELOG.md` (modify) | Revision bump to the selected v4 seed |
| repo `research/pii-finetune/results/kdpii/` (create) | Committed metrics only (v3s1 baseline and v4) |

Run the tests from `examples/pii-classifier-ko/training/` with:

```bash
python3 -m unittest discover -s tests -v
```

---

### Task 1: KDPII importer

**Files:**
- Create: `datagen/kdpii.py`
- Test: `tests/test_kdpii.py`

**Interfaces:**
- Produces:
  - `pne_label(spans: list[tuple[str, str]]) -> tuple[int, list[str]]`;
  - `split_of(dialogue_id: str) -> str`;
  - `dialogue_rows(dlg: dict) -> list[dict]`;
  - `build(dialogues: list[dict]) -> dict[str, list[dict]]`;
  - constants `DIRECT_CATS`, `NAMEISH` and `WINDOW_RUNES = 1000`.
- Row schema: the neutral format plus three fields.
  - Neutral fields: `id`, `text`, `label`, `categories`, `genre`, `kind`, `lang`, `source`, `template_id`, `split`.
  - Added: `pne` (sorted KDPII entity labels), `anon_direct` (bool) and `pos` (sentence index).
  - `kind` is `kdpii_sentence` or `kdpii_window`, `genre` is `kdpii_dialogue`, `source` is `kdpii`, and `template_id` is the dialogue id (for example `Data1.1`).

- [ ] **Step 1: Write the failing tests**

```python
# tests/test_kdpii.py
import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "datagen"))

import kdpii  # noqa: E402


def sent(i, form, *spans):
    return {"id": f"{i}", "form": form, "PNE": [{"label": l, "form": f} for l, f in spans]}


def dialogue(did, *sentences):
    return {"sentence": [dict(s, id=f"{did}.{n + 1}") for n, s in enumerate(sentences)]}


def ids_for(split):
    """Dialogue ids that hash into the given split, for building fixtures."""
    out, i = [], 1
    while len(out) < 3:
        did = f"Data{i}.1"
        if kdpii.split_of(did) == split:
            out.append(did)
        i += 1
    return out


class PneLabel(unittest.TestCase):
    def test_direct_identifier_alone_is_positive(self):
        self.assertEqual(kdpii.pne_label([("QT_MOBILE", "010-4056-6702")]), (1, ["phone"]))
        self.assertEqual(kdpii.pne_label([("TMI_EMAIL", "laze1@naver.com")]), (1, ["email"]))
        self.assertEqual(kdpii.pne_label([("QT_RESIDENT_NUMBER", "900101-1234567")]), (1, ["rrn"]))

    def test_switchboard_number_is_negative(self):
        self.assertEqual(kdpii.pne_label([("QT_PHONE", "1661-6420")]), (0, []))
        self.assertEqual(kdpii.pne_label([("QT_PHONE", "063-5093-4840")]), (1, ["phone"]))

    def test_address_needs_a_number_to_be_direct(self):
        self.assertEqual(kdpii.pne_label([("LC_ADDRESS", "종로구")]), (0, []))
        self.assertEqual(kdpii.pne_label([("LC_ADDRESS", "수당동 123-4")]), (1, ["address"]))
        self.assertEqual(kdpii.pne_label([("PS_NAME", "김민수"), ("LC_ADDRESS", "종로구")]), (1, ["address"]))

    def test_name_alone_and_attribute_alone_are_negative(self):
        self.assertEqual(kdpii.pne_label([("PS_NAME", "에디")]), (0, []))
        self.assertEqual(kdpii.pne_label([("OGG_RELIGION", "교회")]), (0, []))
        self.assertEqual(kdpii.pne_label([("PS_NAME", "김민수"), ("OGG_RELIGION", "교회")]), (1, ["religion"]))

    def test_unmapped_labels_are_negative(self):
        for lab in ("PS_ID", "QT_IP", "QT_PLATE_NUMBER", "TMI_SITE", "LC_PLACE", "OGG_CLUB", "CV_SEX"):
            self.assertEqual(kdpii.pne_label([(lab, "x")]), (0, []), lab)


class Rows(unittest.TestCase):
    def test_anon_direct_flag(self):
        rows = kdpii.dialogue_rows(dialogue("Data1.1", sent(1, "010-4056-6702로 문자 주세요", ("QT_MOBILE", "010-4056-6702")),
                                            sent(2, "김민수 010-1111-2222", ("PS_NAME", "김민수"), ("QT_MOBILE", "010-1111-2222"))))
        s = [r for r in rows if r["kind"] == "kdpii_sentence"]
        self.assertTrue(s[0]["anon_direct"])
        self.assertFalse(s[1]["anon_direct"])

    def test_window_reads_name_and_attribute_together(self):
        rows = kdpii.dialogue_rows(dialogue("Data1.1", sent(1, "민수 알지?", ("PS_NAME", "민수")),
                                            sent(2, "걔 교회 다니잖아", ("OGG_RELIGION", "교회"))))
        self.assertEqual([r["label"] for r in rows if r["kind"] == "kdpii_sentence"], [0, 0])
        windows = [r for r in rows if r["kind"] == "kdpii_window"]
        self.assertEqual(len(windows), 1)
        self.assertEqual((windows[0]["label"], windows[0]["categories"]), (1, ["religion"]))

    def test_windows_stay_within_runes(self):
        long = [sent(i, "가" * 300) for i in range(10)]
        rows = kdpii.dialogue_rows(dialogue("Data1.1", *long))
        windows = [r for r in rows if r["kind"] == "kdpii_window"]
        self.assertGreater(len(windows), 1)
        self.assertTrue(all(len(r["text"]) <= kdpii.WINDOW_RUNES for r in windows))

    def test_split_is_per_dialogue_and_stable(self):
        did = "Data7.3"
        rows = kdpii.dialogue_rows(dialogue(did, sent(1, "a"), sent(2, "b")))
        self.assertEqual({r["split"] for r in rows}, {kdpii.split_of(did)})
        self.assertEqual(kdpii.split_of(did), kdpii.split_of(did))

    def test_build_drops_cross_split_duplicates(self):
        train_id, test_id = ids_for("train")[0], ids_for("test")[0]
        shared = "제 번호 010-4056-6702요"
        out = kdpii.build([
            dialogue(train_id, sent(1, shared, ("QT_MOBILE", "010-4056-6702")), sent(2, "네")),
            dialogue(test_id, sent(1, shared, ("QT_MOBILE", "010-4056-6702")), sent(2, "다른 문장")),
        ])
        self.assertIn(shared, {r["text"] for r in out["train"]})
        self.assertNotIn(shared, {r["text"] for r in out["test"]})
        self.assertIn("다른 문장", {r["text"] for r in out["test"]})


if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `python3 -m unittest tests.test_kdpii -v`
Expected: ERROR, `ModuleNotFoundError: No module named 'kdpii'`.

- [ ] **Step 3: Write the implementation**

```python
# datagen/kdpii.py
"""Turn KDPII (Zenodo record 16759166, CC BY 4.0) into neutral rows under this recipe's label policy.

Usage:
  python3 kdpii.py fetch --out ../data/kdpii/PII_dataset_V3.json
  python3 kdpii.py build --src ../data/kdpii/PII_dataset_V3.json --out ../data/kdpii

build writes {train,val,test}.jsonl. Dialogues are split 8:1:1 by a hash of
the dialogue id, so no dialogue spans two splits, and a val or test row whose
text also occurs in train is dropped. Each dialogue yields one row per
sentence (kdpii_sentence) and one row per window of consecutive sentences of
at most WINDOW_RUNES characters (kdpii_window), the shape the gateway reads.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import urllib.request

URL = "https://zenodo.org/api/records/16759166/files/PII_dataset_V3.json/content"
MD5 = "419921a5e153e8346b6d320d5f084a4a"
WINDOW_RUNES = 1000

# Positive on their own (the policy's direct identifiers).
DIRECT = {
    "QT_RESIDENT_NUMBER": "rrn", "QT_ALIEN_NUMBER": "rrn", "QT_PASSPORT_NUMBER": "passport",
    "QT_DRIVER_NUMBER": "driver_license", "QT_MOBILE": "phone", "TMI_EMAIL": "email",
    "QT_ACCOUNT_NUMBER": "bank_account", "QT_CARD_NUMBER": "card",
}
# Positive only next to a person's name in the same text.
ATTR = {
    "DT_BIRTH": "birthdate", "QT_AGE": "age", "TM_BLOOD_TYPE": "health", "QT_WEIGHT": "health",
    "QT_LENGTH": "health", "OGG_RELIGION": "religion", "OG_WORKPLACE": "employee", "OG_DEPARTMENT": "employee",
    "CV_POSITION": "employee", "OGG_EDUCATION": "education", "FD_MAJOR": "education", "QT_GRADE": "education",
    "CV_MILITARY_CAMP": "military",
}
NAMEISH = {"PS_NAME", "PS_NICKNAME"}
DIRECT_CATS = set(DIRECT.values()) | {"phone", "address"}
SWITCHBOARD = re.compile(r"^\s*1[5-9]\d{2}[-. ]?\d{4}\s*$")
HAS_DIGIT = re.compile(r"\d")
# Everything else (PS_ID, PS_NICKNAME, QT_IP, QT_PLATE_NUMBER, TMI_SITE, LC_PLACE,
# LCP_COUNTRY, OGG_CLUB, CV_SEX) is negative on its own and is not an attribute.


def pne_label(spans: list[tuple[str, str]]) -> tuple[int, list[str]]:
    """spans are (KDPII label, surface form) pairs from one text."""
    cats: set[str] = set()
    attrs: set[str] = set()
    named = False
    for lab, form in spans:
        if lab in DIRECT:
            cats.add(DIRECT[lab])
        elif lab == "QT_PHONE":
            if not SWITCHBOARD.match(form):
                cats.add("phone")
        elif lab == "LC_ADDRESS":
            (cats if HAS_DIGIT.search(form) else attrs).add("address")
        elif lab in ATTR:
            attrs.add(ATTR[lab])
        elif lab == "PS_NAME":
            named = True
    if named:
        cats |= attrs
    return (1 if cats else 0), sorted(cats)


def split_of(dialogue_id: str) -> str:
    h = int(hashlib.sha256(dialogue_id.encode()).hexdigest(), 16) % 10
    return "train" if h < 8 else ("val" if h == 8 else "test")


def _row(kind: str, text: str, spans: list[tuple[str, str]], did: str, split: str, pos: int) -> dict:
    label, cats = pne_label(spans)
    labels = sorted({l for l, _ in spans})
    return {
        "id": hashlib.sha256(f"{kind}:{text}".encode()).hexdigest()[:16], "text": text, "label": label,
        "categories": cats, "genre": "kdpii_dialogue", "kind": kind, "lang": "ko", "source": "kdpii",
        "template_id": did, "split": split, "pne": labels,
        "anon_direct": bool(set(cats) & DIRECT_CATS) and not NAMEISH & set(labels), "pos": pos,
    }


def dialogue_rows(dlg: dict) -> list[dict]:
    sents = [s for s in dlg["sentence"] if 0 < len(s["form"]) <= WINDOW_RUNES]
    if not sents:
        return []
    did = ".".join(dlg["sentence"][0]["id"].split(".")[:2])
    split = split_of(did)
    spans_of = [[(p["label"], p["form"]) for p in s["PNE"]] for s in sents]
    rows = [_row("kdpii_sentence", s["form"], sp, did, split, i) for i, (s, sp) in enumerate(zip(sents, spans_of))]
    buf: list[str] = []
    spans: list[tuple[str, str]] = []
    start = 0
    for i, (s, sp) in enumerate(zip(sents, spans_of)):
        if buf and len("\n".join(buf + [s["form"]])) > WINDOW_RUNES:
            rows.append(_row("kdpii_window", "\n".join(buf), spans, did, split, start))
            buf, spans, start = [], [], i
        buf.append(s["form"])
        spans += sp
    rows.append(_row("kdpii_window", "\n".join(buf), spans, did, split, start))
    return rows


def build(dialogues: list[dict]) -> dict[str, list[dict]]:
    out: dict[str, list[dict]] = {"train": [], "val": [], "test": []}
    for dlg in dialogues:
        for r in dialogue_rows(dlg):
            out[r["split"]].append(r)
    train_texts = {r["text"] for r in out["train"]}
    for sp in out:
        seen: set[str] = set()
        kept = []
        for r in out[sp]:
            if r["id"] in seen or (sp != "train" and r["text"] in train_texts):
                continue
            seen.add(r["id"])
            kept.append(r)
        out[sp] = kept
    return out


def fetch(out: str) -> None:
    os.makedirs(os.path.dirname(out) or ".", exist_ok=True)
    tmp = out + ".part"
    with urllib.request.urlopen(URL, timeout=300) as resp, open(tmp, "wb") as f:
        f.write(resp.read())
    with open(tmp, "rb") as f:
        digest = hashlib.md5(f.read()).hexdigest()  # noqa: S324 - matches the checksum Zenodo publishes
    if digest != MD5:
        os.remove(tmp)
        raise SystemExit(f"md5 {digest} does not match {MD5}")
    os.replace(tmp, out)


def main() -> None:
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    f = sub.add_parser("fetch")
    f.add_argument("--out", required=True)
    b = sub.add_parser("build")
    b.add_argument("--src", required=True)
    b.add_argument("--out", required=True)
    a = ap.parse_args()
    if a.cmd == "fetch":
        fetch(a.out)
        return
    with open(a.src, encoding="utf-8") as fh:
        splits = build(json.load(fh))
    os.makedirs(a.out, exist_ok=True)
    for sp, rows in splits.items():
        with open(os.path.join(a.out, f"{sp}.jsonl"), "w", encoding="utf-8") as fh:
            for r in rows:
                fh.write(json.dumps(r, ensure_ascii=False) + "\n")
        pos = sum(r["label"] for r in rows)
        anon = sum(r["anon_direct"] for r in rows)
        print(f"{sp}: {len(rows)} rows, positive {pos} ({pos / max(len(rows), 1):.1%}), anonymous direct {anon}")


if __name__ == "__main__":
    main()
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `python3 -m unittest tests.test_kdpii -v`
Expected: 10 tests, OK.

- [ ] **Step 5: Build the real splits once and check the counts**

```bash
python3 datagen/kdpii.py fetch --out data/kdpii/PII_dataset_V3.json
python3 datagen/kdpii.py build --src data/kdpii/PII_dataset_V3.json --out data/kdpii
```

Expected: three lines for train, val and test. Sentence and window rows together come to about 58,000 (about 53,000 sentences after de-duplication plus about one window per dialogue); roughly 80% are train. Each split's positive share is roughly 8–11%. The report's looser own-policy mapping gave 10.6% on sentences, and this mapping drops switchboard numbers and districts without house numbers. Record the three lines in the Task 1 commit message.

- [ ] **Step 6: Commit**

```bash
git add examples/pii-classifier-ko/training/datagen/kdpii.py examples/pii-classifier-ko/training/tests/test_kdpii.py
git commit -s -m "feat(examples): import KDPII into the PII recipe with a dialogue-level split"
```

---

### Task 2: Anonymous-identifier and short-text templates

**Files:**
- Modify: `datagen/gen_templates.py`
- Test: `tests/test_gen_templates.py`

**Interfaces:**
- Produces:
  - `KINDS["pos_direct_anon"]`;
  - `GENRES["chat_turn"]` and `GENRES["form_input"]`;
  - `LENGTHS = {"doc": ..., "short": ...}`;
  - `prompt_for(genre: str, kind: str, idx: int, per_call: int, length: str) -> tuple[str, str]`, which returns the system and user text;
  - `job_id(genre: str, kind: str, idx: int, length: str) -> str`.
- Records written by `main()` gain `"length"`. Job and template ids for `short` end in `/short`, so resuming never confuses the two profiles. `doc` ids keep the v3 form, so existing template files still resume.

- [ ] **Step 1: Write the failing tests**

```python
# tests/test_gen_templates.py
import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "datagen"))

import gen_templates as g  # noqa: E402


class Validate(unittest.TestCase):
    def test_anon_kind_needs_a_direct_identifier_and_no_name(self):
        self.assertTrue(g.validate("pos_direct_anon", "제 번호는 ⟦PHONE⟧입니다"))
        self.assertTrue(g.validate("pos_direct_anon", "⟦RRN⟧"))
        self.assertFalse(g.validate("pos_direct_anon", "⟦NAME⟧ 님 번호 ⟦PHONE⟧"))
        self.assertFalse(g.validate("pos_direct_anon", "주문번호 ⟦ORDER_ID⟧"))

    def test_existing_kinds_unchanged(self):
        self.assertTrue(g.validate("pos_direct", "⟦NAME⟧ ⟦PHONE⟧"))
        self.assertTrue(g.validate("neg_name_only", "리뷰어 ⟦NAME⟧"))


class Prompt(unittest.TestCase):
    def test_short_profile_sets_short_length(self):
        system, user = g.prompt_for("chat_turn", "pos_direct_anon", 0, 10, "short")
        self.assertIn("5 to 40 words", system)
        self.assertNotIn("40 to 300 words", system)
        self.assertIn(g.KINDS["pos_direct_anon"], user)

    def test_doc_profile_keeps_v3_length(self):
        system, _ = g.prompt_for("python_code", "pos_direct", 0, 6, "doc")
        self.assertIn("40 to 300 words", system)

    def test_job_ids_keep_profiles_apart(self):
        self.assertEqual(g.job_id("dev_chat", "pos_direct", 2, "doc"), "dev_chat/pos_direct/2")
        self.assertEqual(g.job_id("dev_chat", "pos_direct", 2, "short"), "dev_chat/pos_direct/2/short")


if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `python3 -m unittest tests.test_gen_templates -v`
Expected: FAIL or ERROR. `pos_direct_anon` is not a kind, so `validate` falls through to the negative branch and the first assertion fails. `prompt_for` and `job_id` do not exist.

- [ ] **Step 3: Write the implementation**

Make these edits in `datagen/gen_templates.py`:

1. Move `import boto3` and `from botocore.config import Config` from the top of the file into `main()`, so that the module imports without boto3 in tests.
2. Add two genres to `GENRES`, after `"kakao_message"`:

```python
    "chat_turn": ("ko", "single turns from Korean messenger chats, customer chat and community comment threads, casual spoken style"),
    "form_input": ("ko", "values typed into Korean web forms, sign-up fields, delivery requests and one-line support requests"),
```

3. Add the kind to `KINDS`, after `"pos_direct"`:

```python
    "pos_direct_anon": f"Each document must contain at least one direct-identifier slot from {DIRECT} and must NOT use ⟦NAME⟧. Refer to people only as 저, 제, 고객님, 담당자, 기사님, 친구 or not at all: a value on its own, a value the writer gives as their own, or a value in a form field. Lookalike slots {NEGATIVE_SLOTS} are allowed.",
```

4. Replace the length sentence in `SYSTEM` with a placeholder, and add `LENGTHS`:

```python
SYSTEM = """You write realistic synthetic documents for training a personal-data detector.
Never write any concrete personal value (no names, phone numbers, emails, addresses, ID numbers, account or card numbers, secrets).
Wherever a value would appear, write a slot marker from the allowed list instead, exactly like ⟦PHONE⟧ (U+27E6 and U+27E7 brackets).
Allowed slot markers: {slots}.
{length} Make documents look like real {lang} working material, not tutorials about slots.
Return ONLY a JSON array of strings, one string per document, with no commentary."""

LENGTHS = {
    "doc": "Vary length (40 to 300 words), structure, tone and domain.",
    "short": "Write short texts: one to three sentences, 5 to 40 words each, like a single chat turn, a form field or a one-line request. Vary tone and domain.",
}
```

5. Add the `validate` branch, before the `pos_attr` branch:

```python
    if kind == "pos_direct_anon":
        return "NAME" not in present and bool(present & set(DIRECT))
```

6. Add the two helpers above `main()`:

```python
def job_id(genre: str, kind: str, idx: int, length: str) -> str:
    base = f"{genre}/{kind}/{idx}"
    return base if length == "doc" else f"{base}/{length}"


def prompt_for(genre: str, kind: str, idx: int, per_call: int, length: str) -> tuple[str, str]:
    lang, desc = GENRES[genre]
    allowed = ", ".join(f"⟦{s}⟧" for s in SLOTS)
    system = SYSTEM.format(slots=allowed, lang="Korean" if lang == "ko" else "English", length=LENGTHS[length])
    user = (f"Genre: {desc}.\nRequirement: {KINDS[kind]}\n"
            f"Write {per_call} different documents in {'Korean (English technical terms are fine)' if lang == 'ko' else 'English'}. "
            f"Variation seed {idx}: pick a different company, product and situation for each document.")
    return system, user
```

7. In `main()`, add `ap.add_argument("--length", choices=sorted(LENGTHS), default="doc")` and `ap.add_argument("--genres", help="comma-separated subset of GENRES")`. Build jobs with `job_id(g, k, i, args.length)` over `args.genres.split(",") if args.genres else list(GENRES)`. In `run()`:
   - call `system, prompt = prompt_for(genre, kind, idx, args.per_call, args.length)` in place of the inline construction;
   - write `"job": job_id(genre, kind, idx, args.length)`;
   - write `"template_id": f"{job_id(genre, kind, idx, args.length)}/{j}"`;
   - add `"length": args.length` to both record shapes.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `python3 -m unittest tests.test_gen_templates -v`
Expected: 5 tests, OK.

- [ ] **Step 5: Commit**

```bash
git add examples/pii-classifier-ko/training/datagen/gen_templates.py examples/pii-classifier-ko/training/tests/test_gen_templates.py
git commit -s -m "feat(examples): anonymous-identifier kind and short-text profile for PII templates"
```

---

### Task 3: Digit obfuscation when filling

**Files:**
- Modify: `datagen/fill.py`
- Test: `tests/test_fill.py`

**Interfaces:**
- Produces:
  - `OBFUSCATE = ("PHONE", "RRN", "FRN", "ACCOUNT", "CARD")`;
  - `obfuscate(value: str, rng: random.Random) -> str`;
  - `fill(text, v, masked=False, obf_rate=0.0)`, whose return is unchanged.
- The default `obf_rate=0.0` keeps v3 output and `indep/build.py` byte-for-byte unchanged.
- The styles are full-width digits, one space between characters, and Korean digit words. None of them appears in the independent set's `AltValues`: `+82`, spaced RRN halves, 4-digit spaced cards, jibun addresses, `at gmail dot com`.

- [ ] **Step 1: Write the failing tests**

```python
# tests/test_fill.py
import os
import random
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "datagen"))

import fill  # noqa: E402
from kr_values import Values  # noqa: E402


class Obfuscate(unittest.TestCase):
    def test_every_style_keeps_the_digits_recoverable(self):
        seen = set()
        for seed in range(30):
            out = fill.obfuscate("010-4056-6702", random.Random(seed))
            seen.add(out)
            self.assertNotEqual(out, "010-4056-6702")
        self.assertIn("０１０－４０５６－６７０２", seen)
        self.assertIn("0 1 0 - 4 0 5 6 - 6 7 0 2", seen)
        self.assertIn("공일공-사공오육-육칠공이", seen)

    def test_rate_one_obfuscates_and_keeps_the_label(self):
        text, present = fill.fill("연락처 ⟦PHONE⟧", Values(random.Random(3)), obf_rate=1.0)
        self.assertEqual(present, ["PHONE"])
        self.assertNotRegex(text, r"010[-. ]?\d{4}[-. ]?\d{4}")

    def test_rate_zero_is_the_default(self):
        a = fill.fill("⟦NAME⟧ ⟦PHONE⟧ ⟦RRN⟧", Values(random.Random(5)))
        b = fill.fill("⟦NAME⟧ ⟦PHONE⟧ ⟦RRN⟧", Values(random.Random(5)), obf_rate=0.0)
        self.assertEqual(a, b)


if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `python3 -m unittest tests.test_fill -v`
Expected: ERROR, `AttributeError: module 'fill' has no attribute 'obfuscate'`.

- [ ] **Step 3: Write the implementation**

Add these to `datagen/fill.py`, below `MASKS`:

```python
OBFUSCATE = ("PHONE", "RRN", "FRN", "ACCOUNT", "CARD")
FULLWIDTH = str.maketrans("0123456789-", "０１２３４５６７８９－")
KO_DIGITS = dict(zip("0123456789", "공일이삼사오육칠팔구"))


def obfuscate(value: str, rng: random.Random) -> str:
    """Write a value the way people dodge filters: full-width, spaced out, or as Korean digit words."""
    style = rng.choice(("fullwidth", "spaced", "korean"))
    if style == "fullwidth":
        return value.translate(FULLWIDTH)
    if style == "spaced":
        return " ".join(value.replace(" ", ""))
    return "".join(KO_DIGITS.get(c, c) for c in value)
```

Then change `fill` so that the obfuscation applies on output only and the cache keeps the plain value:

```python
def fill(text: str, v: Values, masked: bool | str = False, obf_rate: float = 0.0) -> tuple[str, list[str]]:
    """masked: False (no masking), True or "all" (every slot in MASKS), "name" (NAME only)."""
    mask = set(MASKS) if masked in (True, "all") else ({"NAME"} if masked == "name" else set())
    cache: dict[str, str] = {}
    present: list[str] = []
    first_name: list[str] = []

    def sub(m: re.Match) -> str:
        slot = m.group(1)
        if slot in mask:
            return MASKS[slot](v)
        present.append(slot)
        if slot in cache and v.r.random() < 0.5:
            val = cache[slot]
        else:
            method = getattr(v, SLOTS[slot][0])
            val = method(first_name[0]) if slot == "EMAIL" and first_name and v.r.random() < 0.6 else method()
            if slot == "NAME" and not first_name:
                first_name.append(val)
            cache[slot] = val
        if obf_rate and slot in OBFUSCATE and v.r.random() < obf_rate:
            return obfuscate(val, v.r)
        return val

    return SLOT_RE.sub(sub, text), present
```

With `obf_rate=0.0`, the `if obf_rate and ...` guard short-circuits before it draws a random number, so v3 fills keep their random sequence. Finally, add `ap.add_argument("--obf-rate", type=float, default=0.0)` to `main()` and pass `obf_rate=args.obf_rate` to `fill`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `python3 -m unittest tests.test_fill -v`
Expected: 3 tests, OK.

- [ ] **Step 5: Confirm that the independent set and v3 fills are unchanged**

The unit test above compares the new code with itself. This step compares it with the code before the task. With the v3 inputs under `data/`, run `indep/build.py` and `datagen/fill.py` at the parent commit and again with the change, using the same arguments, and compare:

```bash
git stash push -- examples/pii-classifier-ko/training/datagen/fill.py
python3 indep/build.py --templates data/indep/templates_v3.jsonl --train-templates data/templates.jsonl --out data/.tmp/indep_before.jsonl
python3 datagen/fill.py --templates data/templates.jsonl --out data/.tmp/fill_before
git stash pop
python3 indep/build.py --templates data/indep/templates_v3.jsonl --train-templates data/templates.jsonl --out data/.tmp/indep_after.jsonl
python3 datagen/fill.py --templates data/templates.jsonl --out data/.tmp/fill_after
cmp data/.tmp/indep_before.jsonl data/.tmp/indep_after.jsonl && diff -r data/.tmp/fill_before data/.tmp/fill_after && echo identical
```

Expected: `identical`. A difference means the change has moved `fill`'s random sequence. Fix it before you commit.

- [ ] **Step 6: Commit**

```bash
git add examples/pii-classifier-ko/training/datagen/fill.py examples/pii-classifier-ko/training/tests/test_fill.py
git commit -s -m "feat(examples): optional digit obfuscation when filling PII templates"
```

---

### Task 4: Mixer

**Files:**
- Create: `datagen/mix.py`
- Test: `tests/test_mix.py`

**Interfaces:**
- Consumes: Task 1 row files (`data/kdpii/{train,val,test}.jsonl`) and `fill.py` output (`data/neutral_v4/{train,val,test,ood}.jsonl`).
- Produces: `mix(synth: dict[str, list[dict]], kd: dict[str, list[dict]], frac: float, max_share: float, rng: random.Random) -> dict[str, list[dict]]`. Its keys are `train`, `val`, `test`, `ood` and `kdpii_test`. `convert.py` reads only the first four, so the KDPII test split never reaches a training format.

- [ ] **Step 1: Write the failing tests**

```python
# tests/test_mix.py
import os
import random
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "datagen"))

import mix  # noqa: E402


def rows(n, split, source, did_prefix):
    return [{"id": f"{source}{split}{i}", "text": f"{source} {split} {i}", "split": split, "source": source,
             "template_id": f"{did_prefix}.{i}"} for i in range(n)]


class Mix(unittest.TestCase):
    def setUp(self):
        self.synth = {sp: rows(100, sp, "llm", "t") for sp in ("train", "val", "test", "ood")}
        self.kd = {sp: rows(n, sp, "kdpii", f"D{sp}") for sp, n in (("train", 400), ("val", 50), ("test", 50))}

    def test_kdpii_test_stays_out_of_training_splits(self):
        out = mix.mix(self.synth, self.kd, 0.6, 0.5, random.Random(1))
        test_dialogues = {r["template_id"] for r in self.kd["test"]}
        for sp in ("train", "val", "test", "ood"):
            self.assertFalse(test_dialogues & {r["template_id"] for r in out[sp]}, sp)
        self.assertEqual(len(out["kdpii_test"]), 50)

    def test_share_cap_limits_kdpii_rows(self):
        out = mix.mix(self.synth, self.kd, 1.0, 0.5, random.Random(1))
        n_kd = sum(r["source"] == "kdpii" for r in out["train"])
        self.assertEqual(n_kd, 100)  # capped at 50% of train: as many as the 100 synthetic rows

    def test_fraction_applies_below_the_cap(self):
        out = mix.mix(self.synth, self.kd, 0.1, 0.5, random.Random(1))
        self.assertEqual(sum(r["source"] == "kdpii" for r in out["train"]), 40)
        self.assertEqual(sum(r["source"] == "kdpii" for r in out["val"]), 50)


if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `python3 -m unittest tests.test_mix -v`
Expected: ERROR, `ModuleNotFoundError: No module named 'mix'`.

- [ ] **Step 3: Write the implementation**

```python
# datagen/mix.py
"""Add KDPII rows to the synthetic neutral splits.

Usage: python3 mix.py --synthetic ../data/neutral_v4 --kdpii ../data/kdpii --out ../data/mixed_v4 \
         [--kdpii-frac 0.6] [--max-share 0.5] [--seed 13]

Writes train/val/test/ood.jsonl for convert.py and kdpii_test.jsonl, which
convert.py never reads: KDPII test dialogues stay evaluation-only. At most
--max-share of the train rows come from KDPII, so the coding-traffic genres
keep their weight.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import random


def read(path: str) -> list[dict]:
    with open(path, encoding="utf-8") as f:
        return [json.loads(l) for l in f]


def mix(synth: dict[str, list[dict]], kd: dict[str, list[dict]], frac: float, max_share: float,
        rng: random.Random) -> dict[str, list[dict]]:
    cap = math.floor(max_share / (1 - max_share) * len(synth["train"])) if max_share < 1 else len(kd["train"])
    n = min(round(len(kd["train"]) * frac), cap)
    out = {
        "train": synth["train"] + rng.sample(kd["train"], n),
        "val": synth["val"] + kd["val"],
        "test": list(synth["test"]),
        "ood": list(synth["ood"]),
        "kdpii_test": list(kd["test"]),
    }
    for v in out.values():
        rng.shuffle(v)
    return out


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--synthetic", required=True)
    ap.add_argument("--kdpii", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--kdpii-frac", type=float, default=0.6)
    ap.add_argument("--max-share", type=float, default=0.5)
    ap.add_argument("--seed", type=int, default=13)
    a = ap.parse_args()
    synth = {sp: read(os.path.join(a.synthetic, f"{sp}.jsonl")) for sp in ("train", "val", "test", "ood")}
    kd = {sp: read(os.path.join(a.kdpii, f"{sp}.jsonl")) for sp in ("train", "val", "test")}
    out = mix(synth, kd, a.kdpii_frac, a.max_share, random.Random(a.seed))
    os.makedirs(a.out, exist_ok=True)
    for sp, rs in out.items():
        with open(os.path.join(a.out, f"{sp}.jsonl"), "w", encoding="utf-8") as f:
            for r in rs:
                f.write(json.dumps(r, ensure_ascii=False) + "\n")
        print(f"{sp}: {len(rs)} rows, kdpii {sum(r.get('source') == 'kdpii' for r in rs)}, positive {sum(r['label'] for r in rs)}")


if __name__ == "__main__":
    main()
```

`convert.py` reads `train`, `val`, `test` and `ood` only, and its Laya rows carry `tags.source`, so KDPII rows remain identifiable in training logs. `convert.py` itself does not change.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `python3 -m unittest tests.test_mix -v`
Expected: 3 tests, OK. The fixtures in the tests carry no `label` field, and `mix()` does not read one; only `main()`'s summary line does.

- [ ] **Step 5: Commit**

```bash
git add examples/pii-classifier-ko/training/datagen/mix.py examples/pii-classifier-ko/training/tests/test_mix.py
git commit -s -m "feat(examples): mix KDPII training dialogues into the PII recipe with a share cap"
```

---

### Task 5: Evaluation tools and the v3s1 baseline

**Files:**
- Create: `eval/groups.py`, `eval/probes.py`, `eval/probes.jsonl`
- Modify: `indep/breakdown.py:36`, `eval/metrics.py` (`main`)
- Test: `tests/test_eval.py`
- Create (results): repo `research/pii-finetune/results/kdpii/groups_v3s1.json`, `research/pii-finetune/results/kdpii/README.md`
- Modify (docs): `../README.md`, `../README.ko.md`, `../model-card.md`, which gain the v3s1 KDPII limitation

**Interfaces:**
- Consumes: Task 1 row files.
- Produces:
  - `groups_of(row: dict) -> list[str]`;
  - `summarize(rows: list[dict], preds: dict[str, float], threshold: float = 0.5, skip: set[str] = frozenset()) -> dict`, returning `{"overall": {n, acc, recall, fpr, precision, f1}, "groups": {name: {n, rate}}}`. `rate` is recall for groups of positive rows and the flag rate (FPR) for groups of negative rows;
  - `check(probes: list[dict], scores: list[float], threshold: float) -> list[dict]`, returning the failing probes.

- [ ] **Step 1: Write the failing tests**

```python
# tests/test_eval.py
import json
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "eval"))

import groups  # noqa: E402
import probes  # noqa: E402


def row(i, label, pne, anon=False, cats=()):
    return {"id": str(i), "label": label, "pne": list(pne), "anon_direct": anon, "categories": list(cats)}


class Groups(unittest.TestCase):
    def test_groups_of(self):
        self.assertEqual(groups.groups_of(row(1, 1, ["QT_MOBILE"], True, ["phone"])), ["positive", "anon_direct", "cat:phone"])
        self.assertEqual(groups.groups_of(row(2, 1, ["PS_NAME", "QT_MOBILE"], False, ["phone"])), ["positive", "named", "cat:phone"])
        self.assertEqual(groups.groups_of(row(3, 0, ["PS_NAME"])), ["negative", "neg_name_only"])
        self.assertEqual(groups.groups_of(row(4, 0, [])), ["negative", "neg_no_pne"])

    def test_summarize(self):
        rows = [row(1, 1, ["QT_MOBILE"], True, ["phone"]), row(2, 1, ["QT_MOBILE"], True, ["phone"]),
                row(3, 0, []), row(4, 0, ["PS_NAME"])]
        s = groups.summarize(rows, {"1": 0.9, "2": 0.2, "3": 0.1, "4": 0.7})
        self.assertEqual(s["overall"]["recall"], 0.5)
        self.assertEqual(s["overall"]["fpr"], 0.5)
        self.assertEqual(s["groups"]["anon_direct"], {"n": 2, "rate": 0.5})
        self.assertEqual(s["groups"]["neg_name_only"], {"n": 1, "rate": 1.0})

    def test_summarize_skips_excluded_and_unscored(self):
        rows = [row(1, 1, ["QT_MOBILE"], True), row(2, 0, [])]
        s = groups.summarize(rows, {"1": 0.9}, skip={"2"})
        self.assertEqual(s["overall"]["n"], 1)


class Probes(unittest.TestCase):
    def test_check(self):
        ps = [{"text": "a", "expect": 1, "why": "x"}, {"text": "b", "expect": 0, "why": "y"}]
        self.assertEqual(probes.check(ps, [0.9, 0.1], 0.5), [])
        self.assertEqual([f["text"] for f in probes.check(ps, [0.4, 0.6], 0.5)], ["a", "b"])

    def test_probe_file_is_well_formed(self):
        with open(os.path.join(HERE, "..", "eval", "probes.jsonl"), encoding="utf-8") as f:
            ps = [json.loads(l) for l in f if l.strip()]
        self.assertGreaterEqual(len(ps), 16)
        self.assertEqual({p["expect"] for p in ps}, {0, 1})
        self.assertTrue(all(p["text"] and p["why"] for p in ps))


if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `python3 -m unittest tests.test_eval -v`
Expected: ERROR, `ModuleNotFoundError: No module named 'groups'`.

- [ ] **Step 3: Write `eval/groups.py`**

```python
# eval/groups.py
"""Overall metrics and per-group recall/FPR for KDPII prediction files.

Usage: python3 groups.py --rows ../data/kdpii/test.jsonl name=preds.jsonl [...] \
         [--kind kdpii_sentence] [--exclude ids.txt] [--json out.json]
Positive groups report recall; negative groups report the flag rate (FPR).
"""

from __future__ import annotations

import argparse
import collections
import json

NAMEISH = {"PS_NAME", "PS_NICKNAME"}


def groups_of(row: dict) -> list[str]:
    if row["label"]:
        kind = "anon_direct" if row["anon_direct"] else ("named" if NAMEISH & set(row["pne"]) else "other_positive")
        return ["positive", kind] + [f"cat:{c}" for c in row["categories"]]
    out = ["negative"]
    if row["pne"] and set(row["pne"]) <= NAMEISH:
        out.append("neg_name_only")
    if not row["pne"]:
        out.append("neg_no_pne")
    return out


def summarize(rows: list[dict], preds: dict[str, float], threshold: float = 0.5, skip: set[str] = frozenset()) -> dict:
    tally = collections.defaultdict(lambda: [0, 0])
    tp = fp = fn = tn = 0
    for r in rows:
        if r["id"] in skip or r["id"] not in preds:
            continue
        flag = preds[r["id"]] >= threshold
        tp += r["label"] and flag
        fn += r["label"] and not flag
        fp += (not r["label"]) and flag
        tn += (not r["label"]) and not flag
        for g in groups_of(r):
            tally[g][0] += 1
            tally[g][1] += flag
    n = tp + fp + fn + tn
    precision = tp / (tp + fp) if tp + fp else 0.0
    recall = tp / (tp + fn) if tp + fn else 0.0
    overall = {"n": n, "acc": (tp + tn) / n if n else 0.0, "recall": recall, "fpr": fp / (fp + tn) if fp + tn else 0.0,
               "precision": precision, "f1": 2 * precision * recall / (precision + recall) if precision + recall else 0.0}
    return {"overall": overall, "groups": {g: {"n": c, "rate": k / c} for g, (c, k) in sorted(tally.items())}}


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--rows", required=True)
    ap.add_argument("--kind", help="kdpii_sentence or kdpii_window; default both")
    ap.add_argument("--exclude", help="file of row ids to leave out (label disputes)")
    ap.add_argument("--threshold", type=float, default=0.5)
    ap.add_argument("--json")
    ap.add_argument("preds", nargs="+", help="name=path")
    a = ap.parse_args()
    with open(a.rows, encoding="utf-8") as f:
        rows = [r for r in map(json.loads, f) if not a.kind or r["kind"] == a.kind]
    skip = set(open(a.exclude).read().split()) if a.exclude else set()
    report = {}
    for spec in a.preds:
        name, path = spec.split("=", 1)
        with open(path, encoding="utf-8") as f:
            preds = {p["id"]: p["p"] for p in map(json.loads, f) if p.get("p") is not None}
        s = summarize(rows, preds, a.threshold, skip)
        report[name] = s
        o = s["overall"]
        print(f"\n{name}: n {o['n']} acc {o['acc']:.4f} recall {o['recall']:.4f} fpr {o['fpr']:.4f} "
              f"precision {o['precision']:.4f} f1 {o['f1']:.4f}")
        for g, v in s["groups"].items():
            print(f"  {g:<24} n {v['n']:>6} rate {v['rate']:.3f}")
    if a.json:
        with open(a.json, "w", encoding="utf-8") as f:
            json.dump(report, f, indent=2, ensure_ascii=False)
            f.write("\n")


if __name__ == "__main__":
    main()
```

- [ ] **Step 4: Write `eval/probes.jsonl` and `eval/probes.py`**

All values below are synthetic.

```jsonl
{"text": "010-4056-6702로 문자 하나만 넣어주세요.", "expect": 1, "why": "mobile without a name"}
{"text": "고객 연락처: 010-4056-6702", "expect": 1, "why": "mobile in a field, no name"}
{"text": "900101-1234567", "expect": 1, "why": "bare resident number"}
{"text": "제 이메일은 laze1@naver.com 입니다.", "expect": 1, "why": "own email, no name"}
{"text": "응, laze1@naver.com 이걸로 보낸다?", "expect": 1, "why": "email in casual chat"}
{"text": "기업은행 223-583353-76-703로 입금해 주세요.", "expect": 1, "why": "bank account without a name"}
{"text": "카드 4721-7567-9276-9051 결제가 안 돼요", "expect": 1, "why": "card number without a name"}
{"text": "여권번호 M12345678 맞는지 봐주세요", "expect": 1, "why": "passport number"}
{"text": "서울 마포구 월드컵북로 123, 래미안 104동 1203호로 보내 주세요", "expect": 1, "why": "home address with a number"}
{"text": "김민수 010-4056-6702", "expect": 1, "why": "named mobile (v3 already passes)"}
{"text": "고객센터 1661-6420으로 전화 주세요.", "expect": 0, "why": "switchboard number"}
{"text": "주문번호 INV5321895252 배송 조회 부탁드려요", "expect": 0, "why": "order id"}
{"text": "phone = '010-****-****'", "expect": 0, "why": "masked value"}
{"text": "def mask_phone(s): return re.sub(r'\\d{4}$', '****', s)", "expect": 0, "why": "masking code"}
{"text": "담당자 김민수님께 전달했습니다.", "expect": 0, "why": "name only"}
{"text": "request_id = '550e8400-e29b-41d4-a716-446655440000'", "expect": 0, "why": "uuid"}
{"text": "api_key = os.environ['API_KEY']", "expect": 0, "why": "secret reference, no value"}
{"text": "종로구 접수되었고, 하나는요?", "expect": 0, "why": "district without a house number"}
```

```python
# eval/probes.py
"""A fixed probe set the checkpoint must answer as expected (a release gate).

Usage: python3 probes.py --base http://127.0.0.1:8095 [--probes probes.jsonl] [--threshold 0.5]
Exits 1 when any probe fails. Values in probes.jsonl are synthetic.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.request

INSTRUCTIONS = "Does this text contain personal information about a specific person, or a secret credential?"


def load(path: str) -> list[dict]:
    with open(path, encoding="utf-8") as f:
        return [json.loads(l) for l in f if l.strip()]


def check(probes: list[dict], scores: list[float], threshold: float) -> list[dict]:
    """A probe expecting 1 needs p >= threshold; a probe expecting 0 needs p < threshold."""
    return [dict(p, p=s) for p, s in zip(probes, scores) if (s >= threshold) != bool(p["expect"])]


def score(base: str, texts: list[str]) -> list[float]:
    body = json.dumps({"model": "multilingual", "states": texts,
                       "questions": {"pii": {"type": "noul", "instructions": INSTRUCTIONS}}}).encode()
    req = urllib.request.Request(base + "/v1/systemone/batch", data=body, headers={"content-type": "application/json"})
    with urllib.request.urlopen(req, timeout=300) as resp:
        return [r["answers"]["pii"]["noul"] for r in json.load(resp)["results"]]


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", required=True)
    ap.add_argument("--probes", default=os.path.join(os.path.dirname(os.path.abspath(__file__)), "probes.jsonl"))
    ap.add_argument("--threshold", type=float, default=0.5)
    a = ap.parse_args()
    ps = load(a.probes)
    fails = check(ps, score(a.base, [p["text"] for p in ps]), a.threshold)
    for f in fails:
        print(f"FAIL expect {f['expect']} p {f['p']:.3f} ({f['why']}): {f['text']}")
    print(f"{len(ps) - len(fails)}/{len(ps)} probes pass")
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(main())
```

- [ ] **Step 5: Add the genre group to `indep/breakdown.py` and `--exclude` to `eval/metrics.py`**

In `indep/breakdown.py`, add `f"genre:{m['genre']}"` to the key tuple at line 36, after `f"lang:{m['lang']}"`. Also change the docstring's first line to "split by generator, value format, kind, language and genre."

In `eval/metrics.py`, let gate G3 leave out the 36 disputed rows the way v3's clean numbers did. In `main()`, add the argument after `--band`:

```python
    ap.add_argument("--exclude", help="file of row ids to leave out (label disputes)")
```

Then add `skip = set(open(a.exclude).read().split()) if a.exclude else set()` after `band = ...`, and read each file as `summarize([r for r in map(json.loads, f) if r["id"] not in skip], a.threshold, band)`. Update the usage line to `[--exclude ids.txt]`.

Check that it reproduces v3: `python3 eval/metrics.py --exclude data/indep/disputed_ids_v3.txt s1=data/indep/preds_v3_newckpt/laya_v3_s1.jsonl` prints f1 0.953 and fpr 0.036, as in the fine-tune plan's v3 table.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `python3 -m unittest discover -s tests -v`
Expected: every test from Tasks 1–5 passes (26 tests).

- [ ] **Step 7: Score the v3s1 baseline on KDPII test**

Build the v3s1 image from the reference checkpoint with the recipe's Dockerfile (`../README.md`, "Build the classifier image"), then:

```bash
docker run -d --rm --name laya-v3 -p 127.0.0.1:18096:8095 -e LAYA_HOST=0.0.0.0 laya-ko-pii:v3s1
python3 sagemaker/score_http.py --base http://127.0.0.1:18096 --rows data/kdpii/test.jsonl --out data/preds/kdpii_test_v3s1.jsonl --model multilingual
python3 eval/groups.py --rows data/kdpii/test.jsonl v3s1=data/preds/kdpii_test_v3s1.jsonl --kind kdpii_sentence --json ../../../research/pii-finetune/results/kdpii/groups_v3s1.json
python3 eval/probes.py --base http://127.0.0.1:18096 > data/preds/probes_v3s1.txt; echo "probes exit $?"
docker stop laya-v3
```

Expected:
- **Sentence recall** is well below 0.90. The report measured 0.531 under an approximate own-policy mapping.
- **`anon_direct` recall** is the lowest positive group.
- **`neg_no_pne` flag rate** is about 0.01.
- **Probes:** `probes exit 1`, with most anonymous-identifier probes failing. This is the baseline, not a regression.

Write `research/pii-finetune/results/kdpii/README.md`. It should say what the files are, cite the dataset (title, Zenodo DOI 10.5281/zenodo.16759166, CC BY 4.0), restate the split rule, and copy the printed overall line for v3s1.

- [ ] **Step 8: Record the v3s1 limitation in the sample docs**

In `../README.md` "Evidence", add a bullet after the two document-level bullets:

```markdown
- **Real Korean dialogue (KDPII test dialogues, scored 2026-10-19).** Recall <recall from Step 7> and FPR <fpr from Step 7>: the reference checkpoint misses most direct identifiers that appear without a person's name, such as a phone number in a chat turn. The regex detectors catch some of them. See `research/pii-finetune/results/kdpii/`.
```

Replace both angle-bracket values with the numbers that Step 7 printed, rounded to three decimals. Add the same bullet in Korean to `../README.ko.md`:

```markdown
- **실제 한국어 대화(KDPII 테스트 대화, 2026-10-19 측정).** 재현율 <값>, 오탐률 <값>. 기준 체크포인트는 채팅 속 전화번호처럼 사람 이름 없이 나오는 직접 식별자를 대부분 놓칩니다. 정규식 탐지기가 일부를 잡습니다. `research/pii-finetune/results/kdpii/`를 보세요.
```

Then add a matching line to `../model-card.md` under "Known errors": `- direct identifiers without a name in short dialogue: KDPII anonymous-direct recall <value>.`

- [ ] **Step 9: Commit**

```bash
git add examples/pii-classifier-ko/training/eval examples/pii-classifier-ko/training/indep/breakdown.py \
  examples/pii-classifier-ko/training/tests/test_eval.py research/pii-finetune/results/kdpii \
  examples/pii-classifier-ko/README.md examples/pii-classifier-ko/README.ko.md examples/pii-classifier-ko/model-card.md
git commit -s -m "feat(examples): KDPII group metrics and probe gate; record the v3s1 dialogue baseline"
```

---

### Task 6: Audit the KDPII evaluation labels

The entity-to-policy mapping is a rule. Audit it the way the v3 independent set was audited, before any gate reads it.

**Files:**
- Uses: `indep/audit.py` (no change)
- Create (results): repo `research/pii-finetune/results/kdpii/disputed_test.txt`, `research/pii-finetune/results/kdpii/disputed_val.txt`

- [ ] **Step 1: Ask the user to approve the audit run**

The audit sends each val and test row to Claude on Amazon Bedrock. Together the two splits hold about 11,000 rows. Ask with `AskUserQuestion` before you run anything.

- [ ] **Step 2: Run the audit on test and val**

```bash
python3 indep/audit.py --rows data/kdpii/test.jsonl --out data/kdpii/audit_test.jsonl --profile <profile>
python3 indep/audit.py --rows data/kdpii/val.jsonl --out data/kdpii/audit_val.jsonl --profile <profile>
```

Expected: the script prints the rows where the judge disagrees with the mapped label. In v3, 36 of 2,272 rows (1.6%) disagreed. A rate several times higher means the mapping is wrong for some entity type. In that case:
1. Fix `pne_label` and add a test for that case to `tests/test_kdpii.py`.
2. Rerun Task 1, Step 5.
3. Re-audit.

- [ ] **Step 3: Review the disagreements and write the exclusion lists**

Read each disagreement. Where the judge is right about the policy, the mapping has a case to fix (see Step 2). Where the row is ambiguous, put its id in `disputed_test.txt` or `disputed_val.txt`. Every later `groups.py` call passes `--exclude` with the matching file.

- [ ] **Step 4: Commit**

```bash
git add research/pii-finetune/results/kdpii/disputed_test.txt research/pii-finetune/results/kdpii/disputed_val.txt
git commit -s -m "docs(research): audited KDPII label disputes for the PII recipe"
```

---

### Task 7: Build v4 data, train and select

Every step here runs locally or in the user's AWS account. Steps 1 and 4 cost money and need the user's explicit approval.

**Files:** none committed except metrics in Step 7.

- [ ] **Step 1: Ask the user to approve template generation and training**

Show the call counts from Step 2 and the training estimate from Step 4. Ask with `AskUserQuestion`.

- [ ] **Step 2: Generate the v4 templates**

```bash
# Anonymous identifiers at document length, every genre: 27 genres x 4 calls x 6 docs.
python3 datagen/gen_templates.py --out data/templates_v4.jsonl --kinds pos_direct_anon --calls 4 --per-call 6 --profile <profile>
# Short texts on conversational genres: 6 genres x 7 kinds x 6 calls x 10 docs.
python3 datagen/gen_templates.py --out data/templates_v4.jsonl --length short \
  --genres chat_turn,form_input,kakao_message,dev_chat,customer_ticket,english_chat \
  --kinds pos_direct_anon,pos_direct,pos_attr,pos_cred,neg_name_only,neg_lookalike,neg_handling --calls 6 --per-call 10 --profile <profile>
cat data/templates.jsonl data/templates_v4.jsonl > data/templates_all_v4.jsonl
```

Expected: about 108 plus 252 jobs, with most documents kept. A kept ratio below 70% for `pos_direct_anon` means the model keeps writing `⟦NAME⟧`. In that case, strengthen the kind text before you continue.

- [ ] **Step 3: Fill, mix and convert**

```bash
python3 datagen/fill.py --templates data/templates_all_v4.jsonl --out data/neutral_v4 --obf-rate 0.15
python3 datagen/mix.py --synthetic data/neutral_v4 --kdpii data/kdpii --out data/mixed_v4 --kdpii-frac 0.6 --max-share 0.5
python3 datagen/convert.py --neutral data/mixed_v4 --out data/backends_v4
aws s3 sync data/backends_v4 s3://<bucket>/data_v4/ --profile <profile>
```

Expected:
- `fill.py` prints train rows above the v3 8,168, with `pos_direct_anon` present.
- `mix.py` prints KDPII train rows at most equal to the synthetic train rows.
- `kdpii_test` keeps all of `data/kdpii/test.jsonl`.

- [ ] **Step 4: Train three seeds**

```bash
for s in 1 2 3; do
  python3 sagemaker/launch.py --backend laya --instance ml.g6.2xlarge --bucket <bucket> --role-arn <role-arn> \
    --data-prefix data_v4/ --max-hours 6 --hp laya_args="--loss soft-ce --grad-accum 2" --hp epochs=6 --hp seed=$s --profile <profile>
done
```

Estimate: v3 trained 8,168 rows for 8 epochs in about 43 minutes on one L4. v4 has roughly three times the rows, so 6 epochs come to about 1.5 to 2 hours per seed. Expected: three job names are printed, and each job ends `Completed`. Each job's model artifact holds `laya-ko-pii/`.

- [ ] **Step 5: Score every seed on the selection set only**

For each seed, download the artifact and build an image as in Task 5, Step 7 (`laya-ko-pii:v4s<seed>`). Then:

```bash
python3 sagemaker/score_http.py --base http://127.0.0.1:18096 --rows data/kdpii/val.jsonl --out data/preds/kdpii_val_v4s<seed>.jsonl --model multilingual
python3 eval/groups.py --rows data/kdpii/val.jsonl --kind kdpii_sentence --exclude ../../../research/pii-finetune/results/kdpii/disputed_val.txt \
  v4s1=data/preds/kdpii_val_v4s1.jsonl v4s2=data/preds/kdpii_val_v4s2.jsonl v4s3=data/preds/kdpii_val_v4s3.jsonl
```

- [ ] **Step 6: Select one seed, then run the gates on it once**

Select the seed with the highest KDPII val F1 (sentence rows, disputes excluded). Do not look at test or independent-set numbers before you choose. Then run every gate on the selected seed only:

| Gate | Set | Threshold (proposed) | v3s1 |
|---|---|---|---|
| G1 | KDPII test sentences, disputes excluded | recall ≥ 0.90 and FPR ≤ 0.02 | from Task 5, Step 7 |
| G2 | KDPII test sentences | `anon_direct` recall ≥ 0.90; `neg_name_only` rate ≤ 0.10 | from Task 5, Step 7 |
| G3 | Independent set v3, 36 disputes excluded | F1 ≥ 0.94 and FPR ≤ 0.05 | F1 0.953, FPR 0.036 |
| G4 | Independent set code genres (`genre:ci_pipeline`, `notebook_output`, `issue_tracker`, `incident_postmortem`) | FPR ≤ 0.02 | 0.011 |
| G5 | `eval/probes.py` | all pass | fails the anonymous probes |
| G6 | KDPII test windows | recall ≥ 0.90 | from Task 5, Step 7 |

Start the selected seed's image on port 18096 as in Step 5, then:

```bash
python3 sagemaker/score_http.py --base http://127.0.0.1:18096 --rows data/kdpii/test.jsonl --out data/preds/kdpii_test_v4.jsonl --model multilingual
python3 sagemaker/score_http.py --base http://127.0.0.1:18096 --rows data/indep/rows_v3.jsonl --out data/preds/indep_v3_v4.jsonl --model multilingual
python3 eval/groups.py --rows data/kdpii/test.jsonl --kind kdpii_sentence --exclude ../../../research/pii-finetune/results/kdpii/disputed_test.txt v4=data/preds/kdpii_test_v4.jsonl --json ../../../research/pii-finetune/results/kdpii/groups_v4.json
python3 eval/groups.py --rows data/kdpii/test.jsonl --kind kdpii_window v4=data/preds/kdpii_test_v4.jsonl
python3 indep/breakdown.py --rows data/indep/rows_v3.jsonl --exclude data/indep/disputed_ids_v3.txt v4=data/preds/indep_v3_v4.jsonl
python3 eval/metrics.py --exclude data/indep/disputed_ids_v3.txt v4=data/preds/indep_v3_v4.jsonl
python3 eval/probes.py --base http://127.0.0.1:18096
```

If any gate fails, stop and report the gate results to the user. Do not try another seed against the test sets. Iterate the way the Notion experiment did:
1. Read the KDPII **val** errors.
2. Add templates aimed at those error types.
3. Retrain, and select again on val.

The thresholds are proposals. The user may move them, but only before the gates run.

The independent set's `fmt:alt` group is no longer fully unseen: KDPII brings spaced card numbers and jibun-style addresses into training. Report `fmt:std` and `fmt:alt` separately.

- [ ] **Step 7: Commit the metrics**

```bash
git add research/pii-finetune/results/kdpii/groups_v4.json
git commit -s -m "docs(research): v4 KDPII and gate metrics for the PII recipe"
```

---

### Task 8: Ship v4 in the sample docs and config

Run this task only after every gate in Task 7, Step 6 has passed. Every number below comes from the Task 7 outputs.

**Files:**
- Modify: `README.md` (training), `../NOTICE`, `../model-card.md`, `../README.md`, `../README.ko.md`
- Modify: repo `examples/config.pii-classifier-ko.json:41`, `examples/helm.pii-classifier-ko.yaml:32`, `cmd/mayu/pii_classifier_example_test.go:35`, `CHANGELOG.md`

- [ ] **Step 1: Bump the revision**

Let `laya-ko-pii-v4s<N>` be the selected seed's revision. Replace `laya-ko-pii-v3s1` with it in `examples/config.pii-classifier-ko.json`, `examples/helm.pii-classifier-ko.yaml` and `cmd/mayu/pii_classifier_example_test.go`. Then run:

```bash
go test ./cmd/mayu/ -run PIIClassifier -count=1
go test ./internal/config/ -run TestChart -count=1
```

Expected: PASS. The revision is opaque to the gateway, and Shadow evidence does not carry over between revisions (`README.md`, "Rollout", step 5).

- [ ] **Step 2: Attribute KDPII in `NOTICE`**

Append:

```text
The training recipe mixes in, and evaluates on, KDPII: A New Korean Dialogic
Dataset for the Deidentification of Personally Identifiable Information
(KDPII DATASET REVISED, Zenodo, 2024, doi:10.5281/zenodo.16759166), by Yonsei
University HamSaeM Kim's Lab, TSCIENTIFIC Co.,Ltd, Hansaem Kim, Fei Li,
Yejee Kang, Seoyoon Park and Yeonji. It is licensed under the Creative
Commons Attribution 4.0 International License
(https://creativecommons.org/licenses/by/4.0/). The recipe downloads it at
build time and maps its entity labels onto this recipe's label policy
(training/datagen/kdpii.py); no copy is redistributed here.
```

- [ ] **Step 3: Update the training README**

In `README.md` (training), change "Steps" so that it adds:
- KDPII fetch and build (Task 1, Step 5);
- the two generator runs (Task 7, Step 2);
- `fill.py --obf-rate 0.15`, `mix.py` and `convert.py` (Task 7, Step 3);
- the selection rule (KDPII val F1, then the gates once on test);
- the gate table.

Add one line to the label policy: "A direct identifier counts on its own, with or without a name, in any length of text." Mention `eval/probes.py`, `eval/groups.py` and `tests/` (`python3 -m unittest discover -s tests`).

- [ ] **Step 4: Update the model card and the sample READMEs**

- **Model card.** Retitle `../model-card.md` to the v4 revision. In "Training", give the data composition: synthetic rows, KDPII rows, obfuscation rate, epochs and time. In "Evaluation", add a table with v3s1 and v4 rows over G1–G6. Keep the v3s1 row for comparison.
- **READMEs.** In `../README.md` and `../README.ko.md`, change "Evidence" to the v4 numbers. Keep the KDPII line from Task 5, with v3s1 and v4 side by side.

- [ ] **Step 5: CHANGELOG**

Under `## [Unreleased]`, `### Changed`, add one entry. It names the revision, the recipe changes (anonymous-identifier and short-text templates, obfuscation, KDPII dialogue split), and the KDPII and independent-set numbers for v3s1 and v4, quoted from the committed JSON files.

- [ ] **Step 6: Run the repo gates**

```bash
gofmt -l . ; go vet ./...
go test ./... -race -count=1 > .local/v4-test.log 2>&1; echo "exit $?"; grep -E '^(FAIL|---)' .local/v4-test.log
bash tests/run-all.sh
(cd examples/pii-classifier-ko/training && python3 -m unittest discover -s tests -v)
```

Expected: no gofmt output, vet clean, test exit 0 with no FAIL line, the harness passes and all Python tests pass.

- [ ] **Step 7: Commit**

```bash
git add examples/pii-classifier-ko examples/config.pii-classifier-ko.json examples/helm.pii-classifier-ko.yaml \
  cmd/mayu/pii_classifier_example_test.go CHANGELOG.md
git commit -s -m "feat(examples): Korean PII recipe v4 reference checkpoint and evidence"
```
