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
