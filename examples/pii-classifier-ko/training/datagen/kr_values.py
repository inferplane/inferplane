"""Synthetic Korean personal-data values and lookalike negatives.

Every value is generated from a seeded RNG. Nothing here is, or is looked up
from, a real person's data. Check digits are computed so values look valid to
format validators (the gateway's regex detectors included).
"""

from __future__ import annotations

import random
import string

SURNAMES = list("김이박최정강조윤장임한오서신권황안송류전홍고문양손배백허유남심노하곽성차주우구민진나지엄채원천방공현함변염여추도소석선설마길연위표명기반왕금옥육인맹제모탁국어은편용")
SURNAMES = [s for s in SURNAMES if s not in "홍"]  # 홍길동 is the canonical placeholder
GIVEN_SYLLABLES = list("민서지수현준예도하윤우진영은태연재아주호성원유경시채다희승혜나정동건소규리한상율온")

CITIES = [
    ("서울특별시", ["강남구", "서초구", "마포구", "송파구", "관악구", "노원구", "은평구", "성동구"]),
    ("부산광역시", ["해운대구", "수영구", "동래구", "부산진구"]),
    ("인천광역시", ["연수구", "남동구", "부평구"]),
    ("대구광역시", ["수성구", "달서구", "중구"]),
    ("대전광역시", ["유성구", "서구"]),
    ("광주광역시", ["북구", "광산구"]),
    ("경기도", ["성남시 분당구", "수원시 영통구", "용인시 수지구", "고양시 일산동구", "화성시"]),
    ("제주특별자치도", ["제주시", "서귀포시"]),
]
ROADS = ["테헤란로", "역삼로", "판교역로", "올림픽로", "월드컵북로", "해운대로", "중앙대로", "동탄대로", "한라대학로", "광교중앙로", "도산대로", "세종대로"]
APTS = ["래미안", "자이", "힐스테이트", "푸르지오", "e편한세상", "아이파크", "더샵", "롯데캐슬"]

BANKS = {  # bank: account digit groups
    "국민은행": (6, 2, 6),
    "신한은행": (3, 3, 6),
    "우리은행": (4, 3, 6),
    "하나은행": (3, 6, 5),
    "농협": (3, 4, 4, 2),
    "카카오뱅크": (4, 2, 7),
    "기업은행": (3, 6, 2, 3),
    "토스뱅크": (4, 4, 4),
}
EMAIL_DOMAINS = ["naver.com", "gmail.com", "daum.net", "kakao.com", "hanmail.net", "nate.com", "outlook.com"]
DIAGNOSES = ["제2형 당뇨병", "고혈압", "우울증", "공황장애", "갑상선기능저하증", "천식", "위염", "허리디스크", "ADHD", "B형 간염 보균", "불면증", "아토피 피부염"]
LICENSE_REGIONS = ["11", "12", "13", "14", "15", "16", "17", "18", "19", "20", "21", "22", "23", "24", "25", "26", "28"]
PLATE_HANGUL = list("가나다라마거너더러머버서어저고노도로모보소오조구누두루무부수우주하허호")

ROMAN = {
    "김": "kim", "이": "lee", "박": "park", "최": "choi", "정": "jung", "강": "kang", "조": "cho", "윤": "yoon",
    "장": "jang", "임": "lim", "한": "han", "오": "oh", "서": "seo", "신": "shin", "권": "kwon", "황": "hwang",
    "안": "ahn", "송": "song", "류": "ryu", "전": "jeon", "고": "ko", "문": "moon", "양": "yang", "손": "son",
}


class Values:
    def __init__(self, rng: random.Random):
        self.r = rng

    # ---- people ----
    def name(self) -> str:
        n = 1 if self.r.random() < 0.9 else 2
        return self.r.choice(SURNAMES) + "".join(self.r.choice(GIVEN_SYLLABLES) for _ in range(n))

    def birthdate(self) -> tuple[int, int, int]:
        return self.r.randint(1950, 2006), self.r.randint(1, 12), self.r.randint(1, 28)

    def rrn(self) -> str:
        y, m, d = self.birthdate()
        male = self.r.random() < 0.5
        g = (1 if male else 2) if y < 2000 else (3 if male else 4)
        digits = [int(c) for c in f"{y % 100:02d}{m:02d}{d:02d}{g}"] + [self.r.randint(0, 9) for _ in range(5)]
        w = [2, 3, 4, 5, 6, 7, 8, 9, 2, 3, 4, 5]
        check = (11 - sum(a * b for a, b in zip(digits, w)) % 11) % 10
        s = "".join(map(str, digits)) + str(check)
        return f"{s[:6]}-{s[6:]}" if self.r.random() < 0.85 else s

    def foreigner_rrn(self) -> str:
        y, m, d = self.birthdate()
        g = self.r.choice([5, 6]) if y < 2000 else self.r.choice([7, 8])
        return f"{y % 100:02d}{m:02d}{d:02d}-{g}{self.r.randint(0, 999999):06d}"

    def phone(self) -> str:
        a, b = self.r.randint(1000, 9999), self.r.randint(1000, 9999)
        sep = self.r.choice(["-", "-", "-", " ", "", "."])
        if self.r.random() < 0.85:
            return f"010{sep}{a}{sep}{b}"
        area = self.r.choice(["02", "031", "032", "051", "053", "042", "062", "064"])
        return f"{area}-{self.r.randint(200, 9999)}-{b}"

    def email(self, name: str | None = None) -> str:
        surname = ROMAN.get((name or self.name())[0], "kim")
        local = self.r.choice([
            surname + "".join(self.r.choice(string.ascii_lowercase) for _ in range(self.r.randint(2, 5))),
            "".join(self.r.choice(string.ascii_lowercase) for _ in range(self.r.randint(4, 8))) + str(self.r.randint(1, 9999)),
            surname + "." + "".join(self.r.choice(string.ascii_lowercase) for _ in range(self.r.randint(3, 6))),
        ])
        return f"{local}@{self.r.choice(EMAIL_DOMAINS)}"

    def address(self) -> str:
        city, districts = self.r.choice(CITIES)
        base = f"{city} {self.r.choice(districts)} {self.r.choice(ROADS)} {self.r.randint(1, 450)}"
        if self.r.random() < 0.6:
            base += f", {self.r.choice(APTS)} {self.r.randint(101, 125)}동 {self.r.randint(1, 30)}{self.r.randint(1, 4):02d}호"
        return base

    def account(self) -> str:
        bank, groups = self.r.choice(list(BANKS.items()))
        num = "-".join("".join(str(self.r.randint(0, 9)) for _ in range(g)) for g in groups)
        return f"{bank} {num}" if self.r.random() < 0.7 else num

    def card(self) -> str:
        prefix = self.r.choice(["4", "51", "52", "53", "9410", "3528", "6250"])
        digits = [int(c) for c in prefix] + [self.r.randint(0, 9) for _ in range(15 - len(prefix))]
        total = 0
        for i, dgt in enumerate(reversed(digits)):
            v = dgt * 2 if i % 2 == 0 else dgt
            total += v - 9 if v > 9 else v
        digits.append((10 - total % 10) % 10)
        s = "".join(map(str, digits))
        return "-".join(s[i:i + 4] for i in range(0, 16, 4)) if self.r.random() < 0.7 else s

    def passport(self) -> str:
        return self.r.choice("MSRGD") + f"{self.r.randint(0, 99999999):08d}"

    def driver_license(self) -> str:
        return f"{self.r.choice(LICENSE_REGIONS)}-{self.r.randint(0, 99):02d}-{self.r.randint(0, 999999):06d}-{self.r.randint(0, 99):02d}"

    def plate(self) -> str:
        return f"{self.r.randint(10, 399)}{self.r.choice(PLATE_HANGUL)} {self.r.randint(1000, 9999)}"

    def diagnosis(self) -> str:
        return self.r.choice(DIAGNOSES)

    def salary(self) -> str:
        return self.r.choice([f"연봉 {self.r.randint(28, 160) * 100}만원", f"월 {self.r.randint(220, 1200)}만원", f"{self.r.randint(30, 150) * 1_000_000:,}원"])

    def birth_str(self) -> str:
        y, m, d = self.birthdate()
        return self.r.choice([f"{y}-{m:02d}-{d:02d}", f"{y}년 {m}월 {d}일", f"{y}.{m:02d}.{d:02d}"])

    def employee_id(self) -> str:
        return f"{self.r.choice(['E', 'EMP', 'S'])}{self.r.randint(10000, 999999)}"

    def credential(self) -> str:
        kind = self.r.randint(0, 4)
        rand = lambda n, al=string.ascii_letters + string.digits: "".join(self.r.choice(al) for _ in range(n))
        if kind == 0:
            return "sk-" + rand(40)
        if kind == 1:
            return "ghp_" + rand(36)
        if kind == 2:
            return rand(self.r.randint(10, 16), string.ascii_letters + string.digits + "!@#$%^&*")
        if kind == 3:
            return "xoxb-" + "-".join(rand(12, string.digits) for _ in range(2)) + "-" + rand(24)
        return "eyJ" + rand(30) + "." + rand(40) + "." + rand(32)

    # ---- negatives: lookalikes and placeholders ----
    def uuid(self) -> str:
        h = "".join(self.r.choice("0123456789abcdef") for _ in range(32))
        return f"{h[:8]}-{h[8:12]}-4{h[13:16]}-{h[16:20]}-{h[20:]}"

    def sha(self) -> str:
        return "".join(self.r.choice("0123456789abcdef") for _ in range(self.r.choice([7, 12, 40, 64])))

    def order_id(self) -> str:
        return self.r.choice([f"ORD-2026{self.r.randint(101, 1231):04d}-{self.r.randint(1, 99999):05d}",
                              f"INV{self.r.randint(10**9, 10**10 - 1)}", f"#{self.r.randint(100000, 999999)}"])

    def business_no(self) -> str:
        return f"{self.r.randint(100, 899)}-{self.r.randint(10, 99)}-{self.r.randint(10000, 99999)}"

    def switchboard(self) -> str:
        return self.r.choice([f"1588-{self.r.randint(1000, 9999)}", f"1577-{self.r.randint(1000, 9999)}",
                              f"1544-{self.r.randint(1000, 9999)}", f"1661-{self.r.randint(1000, 9999)}"])

    def masked_phone(self) -> str:
        return self.r.choice([f"010-****-{self.r.randint(1000, 9999)}", f"010-{self.r.randint(1000, 9999)}-****", "010-XXXX-XXXX", "010-0000-0000"])

    def masked_rrn(self) -> str:
        return self.r.choice([f"{self.r.randint(500101, 991231)}-*******", "******-*******", "000000-0000000", f"{self.r.randint(500101, 991231)}-{self.r.randint(1, 4)}******"])

    def placeholder_email(self) -> str:
        return self.r.choice(["user@example.com", "test@test.com", "noreply@company.co.kr", "{{email}}", "admin@localhost", "support@example.org"])

    def version(self) -> str:
        return f"v{self.r.randint(0, 9)}.{self.r.randint(0, 40)}.{self.r.randint(0, 99)}"

    def timestamp(self) -> str:
        return f"2026-{self.r.randint(1, 12):02d}-{self.r.randint(1, 28):02d}T{self.r.randint(0, 23):02d}:{self.r.randint(0, 59):02d}:{self.r.randint(0, 59):02d}Z"

    def ip(self) -> str:
        return self.r.choice([f"10.{self.r.randint(0, 255)}.{self.r.randint(0, 255)}.{self.r.randint(1, 254)}",
                              f"192.168.{self.r.randint(0, 255)}.{self.r.randint(1, 254)}", "127.0.0.1"])

    def base64(self) -> str:
        return "".join(self.r.choice(string.ascii_letters + string.digits + "+/") for _ in range(self.r.randint(24, 60))) + "=="


# Slot name -> (method, category or None for negative, is_direct_identifier).
# Attribute slots (birth, diagnosis, salary, employee id, plate) are positive
# only together with a NAME slot in the same document; see label().
SLOTS = {
    "NAME": ("name", "name", False),
    "RRN": ("rrn", "rrn", True),
    "FRN": ("foreigner_rrn", "rrn", True),
    "PHONE": ("phone", "phone", True),
    "EMAIL": ("email", "email", True),
    "ADDRESS": ("address", "address", True),
    "ACCOUNT": ("account", "bank_account", True),
    "CARD": ("card", "card", True),
    "PASSPORT": ("passport", "passport", True),
    "LICENSE": ("driver_license", "driver_license", True),
    "CREDENTIAL": ("credential", "credential", True),
    "BIRTH": ("birth_str", "birthdate", False),
    "DIAGNOSIS": ("diagnosis", "health", False),
    "SALARY": ("salary", "financial", False),
    "EMPLOYEE_ID": ("employee_id", "employee", False),
    "PLATE": ("plate", "vehicle", False),
    # negatives
    "UUID": ("uuid", None, False),
    "SHA": ("sha", None, False),
    "ORDER_ID": ("order_id", None, False),
    "BUSINESS_NO": ("business_no", None, False),
    "SWITCHBOARD": ("switchboard", None, False),
    "MASKED_PHONE": ("masked_phone", None, False),
    "MASKED_RRN": ("masked_rrn", None, False),
    "PLACEHOLDER_EMAIL": ("placeholder_email", None, False),
    "VERSION": ("version", None, False),
    "TIMESTAMP": ("timestamp", None, False),
    "IP": ("ip", None, False),
    "BASE64": ("base64", None, False),
}
POSITIVE_SLOTS = [k for k, v in SLOTS.items() if v[1] is not None]
NEGATIVE_SLOTS = [k for k, v in SLOTS.items() if v[1] is None]


# Rule B (2026-10-14): a linking identifier together with a sensitive attribute
# identifies the person inside the organisation even when the name is absent or
# masked, so the pair is personal information on its own.
LINKING_SLOTS = {"EMPLOYEE_ID", "PLATE"}
SENSITIVE_SLOTS = {"DIAGNOSIS", "SALARY"}


def label(slots: list[str]) -> tuple[int, list[str]]:
    """Apply the plan's label policy to the slots present in one document."""
    present = set(slots)
    cats: set[str] = set()
    for s in present:
        method, cat, direct = SLOTS[s]
        if direct:
            cats.add(cat)
    if "NAME" in present:
        for s in present:
            method, cat, direct = SLOTS[s]
            if cat and not direct and s != "NAME":
                cats.add(cat)
    if present & LINKING_SLOTS and present & SENSITIVE_SLOTS:
        cats.update(SLOTS[s][1] for s in present & (LINKING_SLOTS | SENSITIVE_SLOTS))
    return (1 if cats else 0), sorted(cats)
