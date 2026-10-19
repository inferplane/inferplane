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
