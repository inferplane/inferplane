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
