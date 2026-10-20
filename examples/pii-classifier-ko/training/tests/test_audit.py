import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "indep"))

import audit  # noqa: E402


class Record(unittest.TestCase):
    def test_kdpii_rows_have_no_generator(self):
        r = {"id": "a1", "label": 1, "kind": "kdpii_sentence", "source": "kdpii", "text": "010-5821-3307로 문자 주세요"}
        self.assertEqual(audit.record(r, 1, "phone"), {"id": "a1", "label": 1, "judge": 1, "reason": "phone",
                                                       "kind": "kdpii_sentence", "source": "kdpii", "generator": None})

    def test_independent_rows_keep_their_generator(self):
        r = {"id": "b2", "label": 0, "kind": "neg_name_only", "source": "llm", "generator": "gen-a", "text": "x"}
        self.assertEqual(audit.record(r, 0, "name only")["generator"], "gen-a")


class Policy(unittest.TestCase):
    def test_policy_matches_the_kdpii_mapping(self):
        self.assertIn("religion", audit.POLICY)
        self.assertIn("age", audit.POLICY)
        self.assertIn("employer, team, job title or school", audit.POLICY)


if __name__ == "__main__":
    unittest.main()
