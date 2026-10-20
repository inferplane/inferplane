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
            out = fill.obfuscate("010-5821-3307", random.Random(seed))
            seen.add(out)
            self.assertNotEqual(out, "010-5821-3307")
        self.assertIn("０１０－５８２１－３３０７", seen)
        self.assertIn("0 1 0 - 5 8 2 1 - 3 3 0 7", seen)
        self.assertIn("공일공-오팔이일-삼삼공칠", seen)

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
