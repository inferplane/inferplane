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
