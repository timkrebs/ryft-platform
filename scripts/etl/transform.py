"""Normalize projected provider rows into the existing RYFT catalog model."""

from __future__ import annotations

import csv
import math
import re
import unicodedata
from collections import Counter
from dataclasses import asdict, dataclass, field, replace
from datetime import datetime, timezone
from decimal import Decimal
from fractions import Fraction
from hashlib import sha256
from json import dumps, loads
from pathlib import Path
from typing import NamedTuple

from categorizer import Category, classify, normalize

OFF_MARKETS = {
    "en:germany": "DE", "en:austria": "AT", "en:switzerland": "CH",
    "en:european-union": "EU",
}
ALCOHOL = {"en:alcoholic-beverages", "en:wines", "en:beers", "en:spirits"}
VOLUME = {"ml", "cl", "dl", "l", "fl oz"}
MASS = {"g", "mg", "kg", "oz", "lb"}
LANG = re.compile(r"^[a-z]{2,8}(?:-[a-z0-9]{2,8})*$")
DECLARED_SERVING = re.compile(r"\s*([0-9]+(?:[.,][0-9]+)?)\s*(g|ml)\s*", re.IGNORECASE)
DECLARED_SERVING_SUFFIX = re.compile(r"[^()]+\(([^()]+)\)\s*")
FIBER_EXCEPTION_G = 15
ATWATER_FRACTION = 0.30
BLS_FIBER_KCAL_PER_G = 2  # EU 1169/2011 Annex XIV; BLS only (D313)
BLS_KCAL_ROUNDING = 2  # BLS rounds kcal to whole numbers; BLS only (D313)
MG_PER_G = 1_000
SODIUM_MG_PER_G_SALT = 400
ETHANOL_G_PER_ML = 0.789

OFF_NUTRIENTS = {
    "energy-kcal": ("energy_kcal", "kcal"), "energy-kj": ("energy_kj", "kJ"),
    "proteins": ("protein", "g"), "fat": ("fat", "g"),
    "carbohydrates": ("carbohydrates", "g"), "carbohydrates-total": ("carbohydrates_total", "g"),
    "fiber": ("fiber", "g"), "sugars": ("total_sugar", "g"), "added-sugars": ("added_sugar", "g"),
    "added-sugar": ("added_sugar", "g"),
    "saturated-fat": ("saturated_fat", "g"), "monounsaturated-fat": ("monounsaturated_fat", "g"),
    "polyunsaturated-fat": ("polyunsaturated_fat", "g"), "trans-fat": ("trans_fat", "g"),
    "omega-3-fat": ("omega_3", "g"), "omega-6-fat": ("omega_6", "g"),
    "eicosapentaenoic-acid": ("epa", "g"), "docosahexaenoic-acid": ("dha", "g"),
    "sodium": ("sodium", "mg"), "potassium": ("potassium", "mg"), "calcium": ("calcium", "mg"),
    "magnesium": ("magnesium", "mg"), "iron": ("iron", "mg"), "zinc": ("zinc", "mg"),
    "phosphorus": ("phosphorus", "mg"), "selenium": ("selenium", "ug"),
    "vitamin-c": ("vitamin_c", "mg"), "vitamin-d": ("vitamin_d", "ug"),
    "vitamin-b1": ("vitamin_b1", "mg"), "vitamin-b2": ("vitamin_b2", "mg"),
    "vitamin-b6": ("vitamin_b6", "mg"), "vitamin-b12": ("vitamin_b12", "ug"),
    "caffeine": ("caffeine", "mg"), "water": ("water", "g"), "cholesterol": ("cholesterol", "mg"),
}

# Match the platform nutrient registry, preserving the USDA net/gross distinction.
USDA_NUTRIENTS = {
    "1008": ("energy_kcal", "kcal"),
    "2047": ("energy_kcal", "kcal"),
    "2048": ("energy_kcal", "kcal"),
    "1062": ("energy_kj", "kJ"),
    "1003": ("protein", "g"),
    "1004": ("fat", "g"),
    "1005": ("carbohydrates_total", "g"),
    "1079": ("fiber", "g"),
    "2000": ("total_sugar", "g"),
    "1235": ("added_sugar", "g"),
    "1258": ("saturated_fat", "g"),
    "1292": ("monounsaturated_fat", "g"),
    "1293": ("polyunsaturated_fat", "g"),
    "1257": ("trans_fat", "g"),
    "1278": ("epa", "g"),
    "1272": ("dha", "g"),
    "1093": ("sodium", "mg"),
    "1092": ("potassium", "mg"),
    "1087": ("calcium", "mg"),
    "1090": ("magnesium", "mg"),
    "1089": ("iron", "mg"),
    "1095": ("zinc", "mg"),
    "1091": ("phosphorus", "mg"),
    "1103": ("selenium", "ug"),
    "1106": ("vitamin_a", "ug"),
    "1162": ("vitamin_c", "mg"),
    "1114": ("vitamin_d", "ug"),
    "1109": ("vitamin_e", "mg"),
    "1185": ("vitamin_k", "ug"),
    "1165": ("vitamin_b1", "mg"),
    "1166": ("vitamin_b2", "mg"),
    "1167": ("vitamin_b3", "mg"),
    "1175": ("vitamin_b6", "mg"),
    "1178": ("vitamin_b12", "ug"),
    "1190": ("folate", "ug"),
    "1057": ("caffeine", "mg"),
    "1018": ("alcohol", "g"),
    "1051": ("water", "g"),
    "1253": ("cholesterol", "mg"),
}

USDA_ENERGY_PRIORITY = {"2048": 3, "1008": 2, "2047": 1}
UNIT_MASS_G = {"g": 1, "mg": 0.001, "ug": 0.000001}
# The publisher's citation, verbatim from blsdb.de/download (D296).
BLS_ATTRIBUTION = ("Max Rubner-Institut (2025): Bundeslebensmittelschlüssel (BLS), Version 4.0 — Deutsche "
                   "Nährstoffdatenbank. Karlsruhe. DOI: 10.25826/Data20251217-134202-0")
USDA_PORTION_ID = re.compile(r"[1-9][0-9]{0,19}")
UNKNOWN_MEASURES = {"undetermined", "unspecified", "unknown", "not specified", "n/a", "none"}
STAPLES_PATH = Path(__file__).with_name("staples.v1.json")
STAPLES = loads(STAPLES_PATH.read_text(encoding="utf-8"))

# G4 (D296): the full BLS 4.0 import. The CSV is the reviewed extract that scripts/etl/bls_convert.py writes.
BLS_METHOD = "bls:4.0"
# bls_convert.BLS_CODE and the nutrition_bls_attribution constraint (migration 0062): "M111300", "M5B1600", "U57B100".
BLS_ID = re.compile(r"[A-Z][0-9][0-9A-Z]{2}[0-9]{3}")
BLS_CSV_FIXED = ("bls_code", "name_de", "name_en")
# The registry codes a BLS row can carry. BLS CHO is the available carbohydrate: `carbohydrates`, never the USDA total.
BLS_NUTRIENT_CODES = frozenset({code for code, _ in USDA_NUTRIENTS.values()} - {"carbohydrates_total", "added_sugar"}
                               | {"carbohydrates"})
BLS_CSV_PATH = Path(__file__).with_name("data") / "bls_4_0.csv"
BLS_CURATION_PATH = Path(__file__).with_name("bls_curation.v1.json")
# Part of the BLS dataset version (bls_input_digest). Bump it when bls_row_food, the forms, the main groups, the alias
# logic or the categorizer's output for BLS changes: the same CSV then gives `updated`, never `conflict`.
BLS_TRANSFORM_VERSION = "bls2"  # bls2 (D313): the BLS energy rule in validate()
BLS_ALIAS_LIMIT = 6
STAPLE_DRIFT_FRACTION = 0.01
# The BLS main group (first letter of the code) as a category hint for classify().
BLS_MAIN_GROUPS = {
    "B": "Brot und Kleingebäck", "C": "Getreide und Getreideerzeugnisse", "D": "Dauerbackwaren und Kuchen",
    "E": "Eier und Teigwaren", "F": "Obst und Obsterzeugnisse", "G": "Gemüse und Gemüseerzeugnisse",
    "H": "Hülsenfrüchte, Ölsamen und Schalenfrüchte", "K": "Kartoffeln und Pilze", "M": "Milch und Milcherzeugnisse",
    "N": "Alkoholfreie Getränke", "P": "Alkoholische Getränke", "Q": "Fette und Öle",
    "R": "Gewürze und Würzmittel", "S": "Zucker und Süßwaren", "T": "Fische und Meeresfrüchte", "U": "Fleisch",
    "V": "Wild, Geflügel und Innereien", "W": "Wurstwaren",
}
# German state words of a food name -> food_form. Whole words, any case. The first entry that matches wins.
BLS_FORMS = tuple(
    (re.compile(rf"(?<!\w)(?:{word})(?!\w)", re.IGNORECASE), form) for word, form in (
        ("roh", "raw"), ("gekocht", "boiled"), ("gegart", "cooked"), ("gebraten", "fried"),
        ("gebacken", "baked"), ("getrocknet", "dried"), ("tiefgefroren|tiefgekühlt", "frozen")))


# G1 (D288): clean USDA portion labels. Everything below is table-driven; the tests pin these constants.
class Size(NamedTuple):
    key: str
    words: tuple[str, ...]  # source spellings, matched at the start of the text
    en: str  # "<amount> <en>"
    en_short: str
    de_one: str  # "<amount> <de_one>" for an amount of 1
    de_adj: str  # "<amount> Stück, <de_adj>" for other amounts
    de_short: str
    order: int


class Unit(NamedTuple):
    key: str
    words: tuple[str, ...]
    en: tuple[str, str]  # singular, plural
    de: tuple[str, str]
    order: int
    kind: str = "count"  # count | volume | nlea | mass


SIZES = (
    Size("extra_small", ("extra small", "extra-small"), "extra small", "Extra small", "sehr kleines Stück", "sehr klein", "Sehr klein", 5),
    Size("small", ("small",), "small", "Small", "kleines Stück", "klein", "Klein", 10),
    Size("medium", ("medium",), "medium", "Medium", "mittelgroßes Stück", "mittel", "Mittel", 20),
    Size("large", ("large",), "large", "Large", "großes Stück", "groß", "Groß", 30),
    Size("extra_large", ("extra large", "extra-large"), "extra large", "Extra large", "sehr großes Stück", "sehr groß", "Sehr groß", 35),
    Size("jumbo", ("jumbo",), "jumbo", "Jumbo", "Stück (Jumbo)", "Jumbo", "Jumbo", 38),
)
UNITS = (
    Unit("whole", ("whole",), ("whole", "whole"), ("Stück", "Stück"), 40),
    Unit("piece", ("piece", "pieces", "unit", "units"), ("piece", "pieces"), ("Stück", "Stück"), 41),
    Unit("slice", ("slice", "slices"), ("slice", "slices"), ("Scheibe", "Scheiben"), 42),
    Unit("fillet", ("fillet", "fillets"), ("fillet", "fillets"), ("Filet", "Filets"), 44),
    Unit("breast", ("breast", "breasts"), ("breast", "breasts"), ("Bruststück", "Bruststücke"), 46),
    Unit("link", ("link", "links"), ("link", "links"), ("Würstchen", "Würstchen"), 48),
    Unit("patty", ("patty", "patties"), ("patty", "patties"), ("Patty", "Pattys"), 50),
    Unit("bar", ("bar", "bars"), ("bar", "bars"), ("Riegel", "Riegel"), 52),
    Unit("scoop", ("scoop", "scoops"), ("scoop", "scoops"), ("Kugel", "Kugeln"), 54),
    Unit("can", ("can", "cans"), ("can", "cans"), ("Dose", "Dosen"), 56),
    Unit("bottle", ("bottle", "bottles"), ("bottle", "bottles"), ("Flasche", "Flaschen"), 58),
    Unit("container", ("container", "containers"), ("container", "containers"), ("Becher", "Becher"), 60),
    Unit("packet", ("packet", "packets"), ("packet", "packets"), ("Päckchen", "Päckchen"), 62),
    Unit("serving", ("serving", "servings"), ("serving", "servings"), ("Portion", "Portionen"), 64),
    Unit("tbsp", ("tbsp", "tbsps", "tablespoon", "tablespoons"), ("tbsp", "tbsp"), ("EL", "EL"), 70),
    Unit("tsp", ("tsp", "tsps", "teaspoon", "teaspoons"), ("tsp", "tsp"), ("TL", "TL"), 72),
    Unit("cup", ("cup", "cups"), ("US cup", "US cups"), ("US-Tasse", "US-Tassen"), 80),
    Unit("fl_oz", ("fl oz", "fl. oz", "fluid ounce", "fluid ounces"), ("fl oz", "fl oz"), ("fl oz", "fl oz"), 85, "volume"),
    Unit("nlea_serving", ("nlea serving", "nlea servings"), ("serving", "servings"), ("Portion", "Portionen"), 88, "nlea"),
    Unit("oz", ("oz", "ounce", "ounces"), ("oz", "oz"), ("oz", "oz"), 90, "mass"),
    Unit("lb", ("lb", "lbs", "pound", "pounds"), ("lb", "lb"), ("lb", "lb"), 92, "mass"),
    # D313: the SR Legacy count words that were unmapped (3,375 English-only labels). Orders sit in the gaps of the
    # table above, below 100 where unknown heads start. A German word shared by two heads ("Stück", "Glas") is no
    # problem: a collision with different grams gets " (<g> g)".
    Unit("item", ("item", "items"), ("item", "items"), ("Stück", "Stück"), 41),
    Unit("fruit", ("fruit", "fruits"), ("fruit", "fruits"), ("Frucht", "Früchte"), 43),
    Unit("wedge", ("wedge", "wedges"), ("wedge", "wedges"), ("Spalte", "Spalten"), 45),
    Unit("stalk", ("stalk", "stalks"), ("stalk", "stalks"), ("Stange", "Stangen"), 47),
    Unit("spear", ("spear", "spears"), ("spear", "spears"), ("Stange", "Stangen"), 49),
    Unit("stick", ("stick", "sticks"), ("stick", "sticks"), ("Stange", "Stangen"), 51),
    Unit("leaf", ("leaf", "leaves"), ("leaf", "leaves"), ("Blatt", "Blätter"), 53),
    Unit("clove", ("clove", "cloves"), ("clove", "cloves"), ("Zehe", "Zehen"), 55),
    Unit("head", ("head", "heads"), ("head", "heads"), ("Kopf", "Köpfe"), 57),
    Unit("bunch", ("bunch", "bunches"), ("bunch", "bunches"), ("Bund", "Bund"), 59),
    Unit("sprig", ("sprig", "sprigs"), ("sprig", "sprigs"), ("Zweig", "Zweige"), 61),
    Unit("pod", ("pod", "pods"), ("pod", "pods"), ("Schote", "Schoten"), 63),
    Unit("strip", ("strip", "strips"), ("strip", "strips"), ("Streifen", "Streifen"), 65),
    Unit("ring", ("ring", "rings"), ("ring", "rings"), ("Ring", "Ringe"), 66),
    Unit("pat", ("pat", "pats"), ("pat", "pats"), ("Stück", "Stück"), 67),
    Unit("steak", ("steak", "steaks"), ("steak", "steaks"), ("Steak", "Steaks"), 68),
    Unit("chop", ("chop", "chops"), ("chop", "chops"), ("Kotelett", "Koteletts"), 69),
    Unit("pinch", ("pinch", "pinches"), ("pinch", "pinches"), ("Prise", "Prisen"), 71),
    Unit("drumstick", ("drumstick", "drumsticks"), ("drumstick", "drumsticks"), ("Unterkeule", "Unterkeulen"), 73),
    Unit("thigh", ("thigh", "thighs"), ("thigh", "thighs"), ("Oberkeule", "Oberkeulen"), 74),
    Unit("wing", ("wing", "wings"), ("wing", "wings"), ("Flügel", "Flügel"), 75),
    Unit("leg", ("leg", "legs"), ("leg", "legs"), ("Keule", "Keulen"), 76),
    Unit("roll", ("roll", "rolls"), ("roll", "rolls"), ("Brötchen", "Brötchen"), 77),
    Unit("bun", ("bun", "buns"), ("bun", "buns"), ("Brötchen", "Brötchen"), 78),
    Unit("bagel", ("bagel", "bagels"), ("bagel", "bagels"), ("Bagel", "Bagels"), 79),
    Unit("muffin", ("muffin", "muffins"), ("muffin", "muffins"), ("Muffin", "Muffins"), 81),
    Unit("cookie", ("cookie", "cookies"), ("cookie", "cookies"), ("Keks", "Kekse"), 82),
    Unit("cracker", ("cracker", "crackers"), ("cracker", "crackers"), ("Cracker", "Cracker"), 83),
    Unit("tortilla", ("tortilla", "tortillas"), ("tortilla", "tortillas"), ("Tortilla", "Tortillas"), 84),
    Unit("loaf", ("loaf", "loaves"), ("loaf", "loaves"), ("Laib", "Laibe"), 86),
    Unit("sandwich", ("sandwich", "sandwiches"), ("sandwich", "sandwiches"), ("Sandwich", "Sandwiches"), 87),
    Unit("package", ("package", "packages"), ("package", "packages"), ("Packung", "Packungen"), 89),
    Unit("cubic_inch", ("cubic inch", "cubic inches"), ("cubic inch", "cubic inches"), ("Kubikzoll", "Kubikzoll"),
         90, "mass"),  # like oz: the labels read in grams (the word tables are not used), a cubic inch means nothing here
    Unit("bag", ("bag", "bags"), ("bag", "bags"), ("Beutel", "Beutel"), 91),
    Unit("jar", ("jar", "jars"), ("jar", "jars"), ("Glas", "Gläser"), 93),
    Unit("glass", ("glass", "glasses"), ("glass", "glasses"), ("Glas", "Gläser"), 94),
    Unit("envelope", ("envelope", "envelopes"), ("envelope", "envelopes"), ("Päckchen", "Päckchen"), 95),
    Unit("tablet", ("tablet", "tablets"), ("tablet", "tablets"), ("Tablette", "Tabletten"), 96),
    Unit("frank", ("frank", "franks"), ("frank", "franks"), ("Würstchen", "Würstchen"), 97),
    Unit("frankfurter", ("frankfurter", "frankfurters"), ("frankfurter", "frankfurters"), ("Würstchen", "Würstchen"), 97),
    Unit("sausage", ("sausage", "sausages"), ("sausage", "sausages"), ("Wurst", "Würste"), 98),
    Unit("order", ("order", "orders"), ("order", "orders"), ("Portion", "Portionen"), 99),
)
# A qualifier after the head ("cup, chopped"). One unmapped part leaves the German label out.
QUALIFIERS = {
    "chopped": "gehackt", "diced": "gewürfelt", "cubes": "gewürfelt", "cubed": "gewürfelt",
    "sliced": "in Scheiben", "slices": "in Scheiben", "mashed": "zerdrückt", "pureed": "püriert",
    "shredded": "geraspelt", "grated": "gerieben", "halves": "halbiert", "pieces": "in Stücken",
    "strips": "in Streifen", "wedges": "Spalten", "packed": "fest gedrückt", "thin": "dünn", "thick": "dick",
    "boneless": "ohne Knochen", "bone removed": "ohne Knochen", "bone and skin removed": "ohne Haut und Knochen",
    "skin removed": "ohne Haut", "cooked": "gegart", "raw": "roh", "drained": "abgetropft",
    "crumbled": "zerbröselt", "melted": "geschmolzen", "ground": "gemahlen", "sifted": "gesiebt",
    "florets": "Röschen", "small": "klein", "medium": "mittel", "large": "groß", "whole": "ganz",
    # D313
    "peeled": "geschält", "unpeeled": "ungeschält", "pitted": "entsteint", "crushed": "zerstoßen",
    "fresh": "frisch", "frozen": "tiefgekühlt", "dried": "getrocknet", "canned": "aus der Dose",
    "toasted": "getoastet", "with bone": "mit Knochen", "with skin": "mit Haut",
}
FRACTIONS = ((0.5, "½"), (1 / 3, "⅓"), (0.25, "¼"), (0.75, "¾"), (2 / 3, "⅔"), (0.125, "⅛"))
FRACTION_TOLERANCE = 0.004
ML_PER_FL_OZ = 29.5735
UNKNOWN_WORDS = re.compile(
    r"(?<!\w)(?:" + "|".join(re.escape(word) for word in sorted(UNKNOWN_MEASURES, key=len, reverse=True)) + r")(?!\w)",
    re.IGNORECASE)
# A parenthetical with an imperial measure: (3" dia), (8 fl oz), (1 oz), (approx 2-3/4" long, 2-1/2" dia).
IMPERIAL = re.compile(r'"' + r"|''|\b(?:dia|inch(?:es)?|long|wide|thick|high|sq|oz|lbs?)\b|\bin\.", re.IGNORECASE)
PAREN = re.compile(r"\([^()]*\)")
UNCLOSED_PAREN = re.compile(r"\([^()]*$")
EMPTY_PAREN = re.compile(r"\(\s*\)")
# An inch mark or "dia" outside a parenthetical: 1 cup 1/2" pieces, 1 piece 2" dia.
INCH_MARK = re.compile(r"\d+(?:-\d+/\d+|/\d+)?\s*(?:\"|'')|(?<!\w)dia\b\.?", re.IGNORECASE)
LABEL_LIMIT = 200  # nutrition.portions label and short-label CHECKs, and Go's ValidPortion
AMOUNT = re.compile(r"(\d+\s+\d+/\d+|\d+-\d+/\d+|\d+/\d+|\d+(?:\.\d+)?)(?=[\s,]|$)")
# Longest spelling first, so "extra large" and "fl oz" win over "large" and "oz".
HEAD_PATTERNS = tuple(
    (re.compile(re.escape(word) + r"(?!\w)", re.IGNORECASE), head)
    for word, head in sorted(((word, head) for head in (*SIZES, *UNITS) for word in head.words),
                             key=lambda pair: -len(pair[0])))
RAW_MARKER = re.compile(r'undetermined|"|\bdia\b', re.IGNORECASE)


@dataclass(frozen=True)
class CleanPortion:
    en: str
    de: str | None = None
    short_en: str | None = None
    short_de: str | None = None
    order: int | None = None  # None: keep today's 100 + priority
    key: str | None = None  # G1 key used to match curated staple portions
    rest: str = ""  # the cleaned text after the amount
    unmapped: bool = False

    def labels(self) -> dict[str, str]:
        return {"en": self.en, **({"de": self.de} if self.de else {})}


@dataclass(frozen=True)
class Nutrient:
    code: str
    amount: float
    method: str


@dataclass(frozen=True)
class Portion:
    label: str
    unit: str
    weight_g: float | None = None
    volume_ml: float | None = None
    source_portion_id: str | None = None
    portion_key: str | None = None
    labels: dict[str, str] = field(default_factory=dict)
    short_label_en: str | None = None
    short_label_de: str | None = None
    is_default: bool = False
    sort_order: int = 0
    legacy_label: str | None = None


@dataclass(frozen=True)
class Food:
    provider: str
    external_id: str
    version: str
    name: str
    brand: str
    names: list[tuple[str, str]]
    markets: list[str]
    gtin: str | None
    source_updated_at: datetime | None
    nutrients: list[Nutrient]
    category: Category
    omega3_mg: float | None
    warnings: tuple[str, ...]
    reference_unit: str = "g"
    portions: tuple[Portion, ...] = ()
    is_canonical_staple: bool = False
    canonical_group_key: str | None = None
    food_form: str | None = None
    subtitles: dict[str, str] = field(default_factory=dict)
    aliases: tuple[tuple[str, str], ...] = ()
    attribution: str | None = None
    density_g_ml: float | None = None

    @property
    def checksum(self) -> bytes:
        data = asdict(self)
        data.pop("version")
        for key in ("portions", "is_canonical_staple", "canonical_group_key", "food_form", "subtitles",
                    "aliases", "attribution", "density_g_ml"):
            if not data[key]:
                data.pop(key)
        return sha256(dumps(data, sort_keys=True, default=str, allow_nan=False).encode()).digest()

    @property
    def completeness(self) -> int:
        codes = {n.code for n in self.nutrients}
        return 100 if "fiber" in codes else 80


class Reject(ValueError):
    """A row that failed a quality rule, containing only its stable reason code."""


def number(x: object) -> float | None:
    if x is None or isinstance(x, bool):
        return None
    try:
        val = float(x)
    except (TypeError, ValueError, OverflowError):
        return None
    return val if math.isfinite(val) and val >= 0 else None


def measured_quantity(raw: object) -> float | None:
    value = number(raw)
    return value if value is not None and 0 < value <= 100_000 and round(value, 6) == value else None


def off_serving(text: str | None, reference_unit: str) -> tuple[Portion, ...]:
    if not text or len(text) > 200:
        return ()
    measure = DECLARED_SERVING.fullmatch(text)
    if not measure and (declared := DECLARED_SERVING_SUFFIX.fullmatch(text)):
        measure = DECLARED_SERVING.fullmatch(declared[1])
    if not measure or (unit := measure[2].lower()) != reference_unit:
        return ()
    size = measured_quantity(measure[1].replace(",", "."))
    if size is None:
        return ()
    return (Portion("Serving", "serving", weight_g=size if unit == "g" else None,
                    volume_ml=size if unit == "ml" else None),)


def amount_value(token: str | None) -> float | None:
    """A source amount such as 1, 0.5, 1/2, 1-1/2 or 1 1/2. Anything else is no amount."""
    token = (token or "").strip()
    if not AMOUNT.fullmatch(token):
        return None
    try:
        return float(sum(Fraction(part) for part in re.split(r"[\s-]+", token)))
    except ZeroDivisionError:
        return None


def format_amount(value: float, lang: str) -> str:
    """½ ⅓ ¼ ¾ ⅔ ⅛ for the common fractions of amounts below 2 (1½), else the number (2.5 en, 2,5 de)."""
    whole = math.floor(value)
    fraction = value - whole
    if fraction < FRACTION_TOLERANCE:
        return str(whole)
    if fraction > 1 - FRACTION_TOLERANCE:
        return str(whole + 1)
    if whole <= 1:
        for target, glyph in FRACTIONS:
            if abs(fraction - target) <= FRACTION_TOLERANCE:
                return f"{whole or ''}{glyph}"
    text = format(Decimal(str(round(value, 3))).normalize(), "f")
    return text.replace(".", ",") if lang == "de" else text


def strip_imperial(text: str) -> str:
    def drop(match: re.Match) -> str:
        return " " if IMPERIAL.search(match[0]) else match[0]
    return INCH_MARK.sub(" ", UNCLOSED_PAREN.sub(drop, PAREN.sub(drop, text)))


def squash(text: str) -> str:
    text = re.sub(r"\s+", " ", EMPTY_PAREN.sub(" ", text))
    return re.sub(r"\s*,\s*(?:,\s*)*", ", ", text).strip(" ,")


def qualifier_parts(tail: str, size: bool) -> list[str]:
    parts = []
    for piece in tail.split(","):
        piece = piece.strip()
        if piece.startswith("(") and piece.endswith(")") and piece.count("(") == 1:
            piece = piece[1:-1].strip()
        if piece and not (size and piece.lower() == "whole"):
            parts.append(piece)
    return parts


def clean_portion(text: str | None, amount: str | None, grams: float) -> CleanPortion:
    """Turn a USDA portion text into a clean English label and, where every word is mapped, a German one.

    A head the tables do not know keeps the cleaned English text and gets no German label or short label.
    """
    fallback = f"{grams:g} g"
    cleaned = squash(strip_imperial(UNKNOWN_WORDS.sub(" ", text or "")))
    found = AMOUNT.match(cleaned)
    rest = cleaned[found.end():].strip(" ,") if found else cleaned
    if not rest:
        return CleanPortion(fallback, fallback, fallback, fallback)
    value = next((v for v in (amount_value(found[1] if found else None), amount_value(amount)) if v and v > 0), 1.0)
    head, tail = None, ""
    for pattern, candidate in HEAD_PATTERNS:
        if hit := pattern.match(rest):
            head, tail = candidate, rest[hit.end():]
            break
    if head is None:
        return CleanPortion(cleaned, rest=rest, unmapped=True)
    sized = isinstance(head, Size)
    parts = qualifier_parts(tail, sized)
    qualifier = ", ".join(parts)
    mapped = [QUALIFIERS.get(part.lower()) for part in parts]
    qualifier_de = ", ".join(mapped) if None not in mapped else None
    suffix = re.sub(r"[^a-z0-9]+", "_", qualifier.lower()).strip("_")
    key = f"{head.key}_{suffix}" if suffix else head.key
    amount_en, amount_de = format_amount(value, "en"), format_amount(value, "de")
    one = abs(value - 1) < 1e-9
    qualified = True
    if sized:
        en = f"{amount_en} {head.en}"
        de = f"{amount_de} {head.de_one}" if one else f"{amount_de} Stück, {head.de_adj}"
        short_en = head.en_short if one else f"{amount_en} × {head.en_short}"
        short_de = head.de_short if one else f"{amount_de} × {head.de_short}"
    elif head.kind == "count":
        plural = value > 1 + 1e-9
        en, de = f"{amount_en} {head.en[plural]}", f"{amount_de} {head.de[plural]}"
        short_en, short_de = en, de
    else:
        qualified = False
        if head.kind == "mass":
            en = de = short_en = short_de = fallback
        elif head.kind == "volume":
            en = de = short_en = short_de = f"{max(1, round(value * ML_PER_FL_OZ))} ml"
        else:
            en, de = f"US label serving ({grams:g} g)", f"US-Referenzportion ({grams:g} g)"
            short_en = short_de = f"{grams:g} g"
    if qualified and parts:
        en, short_en = f"{en}, {qualifier}", f"{short_en}, {qualifier}"
        if qualifier_de is None:
            de = short_de = None
        else:
            de, short_de = f"{de}, {qualifier_de}", f"{short_de}, {qualifier_de}"
    # The mapped text can be longer than the source text ("cup" -> "US cup", German qualifiers).
    if len(en) > LABEL_LIMIT:
        en = cleaned if len(cleaned) <= LABEL_LIMIT else fallback
    if short_en is not None and len(short_en) > LABEL_LIMIT:
        short_en = None
    if de is not None and len(de) > LABEL_LIMIT or short_de is not None and len(short_de) > LABEL_LIMIT:
        de = short_de = None
    return CleanPortion(en, de, short_en, short_de, head.order, key, rest)


def head_word(rest: str) -> str:
    """The first word of a cleaned portion text that no table knows, lower-cased: what the dry run counts so the next
    mapping pass is data-driven (D313). A text without a leading letter run gives its first token."""
    word = re.match(r"[^\W\d_][\w'-]*", rest.strip().lower())
    return word[0] if word else (rest.split() or ["?"])[0].lower()[:40]


def with_grams(text: str | None, grams: float | None) -> str | None:
    tagged = f"{text} ({grams:g} g)" if text and grams is not None else text
    return tagged if tagged is not None and len(tagged) <= 200 else text


def disambiguate(items: list[tuple[Portion, bool]]) -> list[Portion]:
    """Per language, labels that two portions of one food share with different grams get " (<g> g)".

    The full labels and the short labels are compared separately. A hit tags both of a portion's labels in that
    language. A curated portion is compared but never changed.
    """
    portions = [portion for portion, _ in items]
    for lang in ("en", "de"):
        full: dict[str, set[int]] = {}
        short: dict[str, set[int]] = {}
        for index, portion in enumerate(portions):
            if text := portion.labels.get(lang):
                full.setdefault(text, set()).add(index)
            if text := (portion.short_label_en if lang == "en" else portion.short_label_de):
                short.setdefault(text, set()).add(index)
        hits = {index for group in (*full.values(), *short.values())
                if len({portions[index].weight_g for index in group}) > 1 for index in group}
        for index in sorted(hits):
            portion = portions[index]
            if items[index][1]:
                continue
            labels = dict(portion.labels)
            if labels.get(lang):
                labels[lang] = with_grams(labels[lang], portion.weight_g)
            if lang == "en":
                portions[index] = replace(portion, label=labels.get("en", portion.label), labels=labels,
                                          short_label_en=with_grams(portion.short_label_en, portion.weight_g))
            else:
                portions[index] = replace(portion, labels=labels,
                                          short_label_de=with_grams(portion.short_label_de, portion.weight_g))
    return portions


PORTION_SOURCES = ("match", "half_of", "preset_grams")


def keyed_entry(entry: dict) -> bool:
    return any(kind in entry for kind in PORTION_SOURCES)


def preset_weight(value: object) -> float | None:
    """A curated weight: a real number (not a bool, not a string), finite and above 0."""
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return None
    return measured_quantity(value)


def portion_matches(rule: dict, portion: Portion, clean: CleanPortion) -> bool:
    if "key" in rule and rule["key"] != clean.key:
        return False
    if "text" in rule and not any(clean.rest.lower().startswith(prefix.lower()) for prefix in rule["text"]):
        return False
    return "grams" not in rule or rule["grams"] == portion.weight_g


def usda_measures(rows: list[dict] | None, overrides: dict | None = None,
                  tally: dict | None = None) -> tuple[Portion, ...]:
    """The portions of one USDA food.

    The set of portions and their IDs follow the raw rows only. G1 changes how a portion is labelled, a curated
    staple entry wins over it. An entry keyed by a USDA portion ID applies when ID and grams match. An entry with
    "match" applies when exactly one raw row fits, and takes that row's grams and ID. "half_of" derives half of
    a matched entry. "preset_grams" is a curated weight that needs no raw row (a typical German cucumber or yogurt
    pot, D313): no USDA ID, the portion ID comes from its key and grams. `tally` collects what could not be applied,
    for the dry run.
    """
    overrides = overrides or {}
    numeric = {key: entry for key, entry in overrides.items() if not keyed_entry(entry)}
    keyed = {key: entry for key, entry in overrides.items() if keyed_entry(entry)}
    result: dict[tuple, tuple[Portion, CleanPortion | None]] = {}
    curated: set[tuple] = set()
    applied: set[str] = set()
    used_ids: set[str] = set()
    for row in (rows or [])[:50]:
        text = row.get("description")
        legacy_label = text
        if not text and (row.get("measure") or row.get("modifier")):
            legacy_label = " ".join(part for key in ("amount", "measure", "modifier") if (part := row.get(key)))
            parts = [part for part in (row.get("measure"), row.get("modifier")) if part and part.lower() not in UNKNOWN_MEASURES]
            text = " ".join(part for part in (row.get("amount"), *parts) if part) if parts else None
        grams = measured_quantity(row.get("grams"))
        if grams is None:
            continue
        desc = text or f"{grams:g} g"
        if len(desc) > 200 or "\0" in desc:
            continue
        source_id = row.get("source_id") or ""
        source_id = source_id if USDA_PORTION_ID.fullmatch(source_id) else None
        if source_id in used_ids:
            continue
        override = numeric.get(source_id, {})
        if override and override["grams"] != grams:
            override = {}  # The release weight changed. Keep the measured raw row without the curated claim.
        clean = clean_portion(text, row.get("amount"), grams)
        order = clean.order if clean.order is not None else 100 + (row.get("priority") or 0)
        if override:
            labels = override.get("labels") or clean.labels()
            shorts = override.get("short_labels") or {"en": clean.short_en, "de": clean.short_de}
            portion = Portion(labels.get("en") or clean.en, "portion", weight_g=grams, source_portion_id=source_id,
                              portion_key=override.get("key"), labels=labels, short_label_en=shorts.get("en"),
                              short_label_de=shorts.get("de"), is_default=override.get("default", False),
                              sort_order=override.get("order", order), legacy_label=legacy_label or desc)
        else:
            portion = Portion(clean.en, "portion", weight_g=grams, source_portion_id=source_id, labels=clean.labels(),
                              short_label_en=clean.short_en, short_label_de=clean.short_de, sort_order=order,
                              legacy_label=legacy_label or desc)
        key = (desc, grams)  # The raw text decides which rows are one portion, as before G1.
        if key not in result or override:
            result[key] = (portion, clean)
            if override:
                curated.add(key)
                applied.add(source_id)
            if source_id:
                used_ids.add(source_id)
    unmatched = [entry.get("key", source_id) for source_id, entry in numeric.items() if source_id not in applied]
    bases: dict[str, Portion] = {}
    # An entry has exactly one source: a raw row ("match"), half of one ("half_of") or a curated weight. Two at once
    # is a curation mistake: report it and apply none of them.
    ambiguous = {name for name, entry in keyed.items() if sum(kind in entry for kind in PORTION_SOURCES) > 1}
    unmatched.extend(name for name in keyed if name in ambiguous)
    for name, entry in keyed.items():
        if "match" not in entry or name in ambiguous:
            continue
        found = [key for key, (portion, clean) in result.items()
                 if key not in curated and portion_matches(entry["match"], portion, clean)]
        if len(found) != 1:
            unmatched.append(name)
            continue
        raw, clean = result[found[0]]
        labels, shorts = entry["labels"], entry["short_labels"]
        bases[name] = Portion(labels["en"], "portion", weight_g=raw.weight_g, source_portion_id=raw.source_portion_id,
                              portion_key=name, labels=labels, short_label_en=shorts["en"], short_label_de=shorts["de"],
                              is_default=entry.get("default", False), sort_order=entry["order"],
                              legacy_label=raw.legacy_label)
        result[found[0]] = (bases[name], clean)
        curated.add(found[0])
    for name, entry in keyed.items():
        if "half_of" not in entry or name in ambiguous:
            continue
        base = bases.get(entry["half_of"])
        if base is None:
            unmatched.append(name)
            continue
        labels, shorts = entry["labels"], entry["short_labels"]
        result[("curated", name)] = (Portion(
            labels["en"], "portion", weight_g=round(base.weight_g / 2, 6), portion_key=name, labels=labels,
            short_label_en=shorts["en"], short_label_de=shorts["de"], is_default=entry.get("default", False),
            sort_order=entry["order"]), None)
        curated.add(("curated", name))
    for name, entry in keyed.items():
        if "preset_grams" not in entry or name in ambiguous:
            continue
        grams = preset_weight(entry["preset_grams"])
        if grams is None:
            unmatched.append(name)
            continue
        labels, shorts = entry["labels"], entry["short_labels"]
        result[("curated", name)] = (Portion(
            labels["en"], "portion", weight_g=grams, portion_key=name, labels=labels,
            short_label_en=shorts["en"], short_label_de=shorts["de"], is_default=entry.get("default", False),
            sort_order=entry["order"]), None)
        curated.add(("curated", name))
    if tally is not None:
        tally["unmatched"] = unmatched
        unmapped = [clean.rest for key, (_, clean) in result.items() if key not in curated and clean.unmapped]
        tally["unmapped_head"] = len(unmapped)
        tally["unmapped_heads"] = [head_word(rest) for rest in unmapped]
    portions = disambiguate([(portion, key in curated) for key, (portion, _) in result.items()])
    return tuple(sorted(portions, key=lambda portion: portion.sort_order))


def gtin(code: str) -> str | None:
    if not re.fullmatch(r"(?:[0-9]{8}|[0-9]{12,14})", code) or not code.strip("0"):
        return None
    total = sum(int(n) * (1 if pos % 2 == 0 else 3) for pos, n in enumerate(reversed(code)))
    return code.zfill(14) if total % 10 == 0 else None


def clean(value: str | None, limit: int) -> str:
    return (value or "").replace("\x00", "").strip()[:limit]


def bls_energy_matches(amounts: dict[str, Nutrient], atwater: float, energy: float) -> bool:
    """The BLS energy rule (D313). BLS `CHO` is the available carbohydrate and BLS `ENERCC` counts fibre at 2 kcal/g
    (Regulation (EU) 1169/2011, Annex XIV), so the fibre belongs in the sum. BLS rounds kcal to whole numbers, so a gap
    of at most 2 kcal passes whatever its share of a near-zero energy. Only BLS rows use this: USDA's carbohydrate
    already includes fibre."""
    fiber = amounts["fiber"].amount if "fiber" in amounts else 0.0
    gap = abs(atwater + fiber * BLS_FIBER_KCAL_PER_G - energy)
    return gap <= energy * ATWATER_FRACTION or gap <= BLS_KCAL_ROUNDING


def validate(name: str, amounts: dict[str, Nutrient], alcohol: bool, bls: bool = False) -> tuple[str, ...]:
    """The quality rules. `bls` adds the BLS energy rule as one more way to pass the Atwater check, after the existing
    exceptions, so no row that was accepted before is decided differently."""
    if len(name) < 2 or len(normalize(name)) < 2:
        raise Reject("invalid_name")
    carb = amounts.get("carbohydrates_total") or amounts.get("carbohydrates")
    if carb is None or any(k not in amounts for k in ("energy_kcal", "protein", "fat")):
        raise Reject("missing_or_invalid_macro")
    energy, protein, fat = (amounts[k].amount for k in ("energy_kcal", "protein", "fat"))
    if protein + carb.amount + fat > 105:
        raise Reject("macro_mass_exceeds_105_g")
    if energy > 950:
        raise Reject("energy_exceeds_950_kcal")
    atwater = protein * 4 + carb.amount * 4 + fat * 9
    if abs(atwater - energy) > energy * ATWATER_FRACTION:
        if alcohol:
            return ("atwater_alcohol_exception",)
        if "fiber" in amounts and amounts["fiber"].amount >= FIBER_EXCEPTION_G:
            return ("atwater_high_fiber_exception",)
        if bls and bls_energy_matches(amounts, atwater, energy):
            return ()
        raise Reject("atwater_deviation")
    return ()


def store_amount(code: str, amount: float | None, method: str) -> Nutrient | None:
    if amount is None or amount > 100_000_000:
        return None
    scaled = round(amount, 9)
    if scaled == 0 and amount > 0:
        return None
    return Nutrient(code, scaled, method)


def off_names(names: list[dict] | None, lang: str | None) -> list[tuple[str, str]]:
    pairs = set()
    for entry in names or ():
        label = clean(entry.get("text"), 200)
        code = (entry.get("lang") or lang or "und").lower()
        if code == "main":
            code = (lang or "und").lower()
        if len(label) >= 2 and len(normalize(label)) >= 2:
            pairs.add((code if LANG.fullmatch(code) and len(code) <= 35 else "und", label))
    preference = {code: rank for rank, code in reversed(list(enumerate(("de", "en", lang))))}
    return sorted(pairs, key=lambda pair: (preference.get(pair[0], len(preference)), pair))


def off_food(row: dict, version: str) -> Food:
    code = row["code"]
    barcode = gtin(code)
    if barcode is None:
        raise Reject("invalid_barcode_checksum")
    markets = sorted({OFF_MARKETS[t] for t in row["countries_tags"] or () if t in OFF_MARKETS})
    if not markets:
        raise Reject("outside_market")
    names = off_names(row["product_name"], row.get("lang"))
    if not names:
        raise Reject("invalid_name")
    name = names[0][1]
    tags = row["categories_tags"] or []
    category = classify(name, tags, provider="off")
    unit = (row.get("product_quantity_unit") or "").strip().lower()
    volume_portion = bool(re.search(r"\b(?:ml|cl|dl|liters?|litres?|fl\.?\s*oz)\b|\d\s*l\b", (row.get("serving_size") or "").lower()))
    known_drink = bool(ALCOHOL.intersection(tags) or category.profile.get("sugary_drink"))
    liquid = (unit in VOLUME or unit not in MASS and volume_portion) and known_drink
    if row.get("no_nutrition_data") or row["nutrition_data_per"] != "100g" or (
        not liquid and (unit in VOLUME or unit not in MASS and (volume_portion or known_drink))
    ):
        raise Reject("unverified_100g_basis")

    amounts: dict[str, Nutrient] = {}
    raw: dict[str, float] = {}
    conflicts = set()
    for entry in row["nutriments"] or ():
        val = number(entry.get("100g"))
        key = entry.get("name")
        if val is None or not key:
            continue
        if key in raw and raw[key] != val:
            conflicts.add(key)
        raw[key] = val
    if conflicts & {"energy-kcal", "proteins", "carbohydrates", "carbohydrates-total", "fat"}:
        raise Reject("conflicting_macros")
    for key in conflicts:
        raw.pop(key, None)
    if "added-sugar" in raw and "added-sugars" in raw and raw["added-sugar"] != raw["added-sugars"]:
        raw.pop("added-sugar")
        raw.pop("added-sugars")
    basis = "ml" if liquid else "g"
    for key, (nutrient, unit) in OFF_NUTRIENTS.items():
        val = raw.get(key)
        if val is not None and nutrient not in amounts:
            if unit in UNIT_MASS_G:
                val /= UNIT_MASS_G[unit]  # OFF 100g mass is standardized in grams, also for label-entered mg.
            if value := store_amount(nutrient, val, f"off:{key}_100{basis}"):
                amounts[nutrient] = value
    abv = raw.get("alcohol")
    if abv is not None and (abv == 0 or liquid and abv <= 100):
        amounts["alcohol"] = Nutrient("alcohol", round(abv * ETHANOL_G_PER_ML, 9), "off:abv_to_ethanol")
    if "sodium" not in amounts and "salt" in raw:
        if derived := store_amount("sodium", raw["salt"] * SODIUM_MG_PER_G_SALT, "off:sodium_from_salt"):
            amounts["sodium"] = derived
    if "energy_kcal" not in amounts and "energy_kj" in amounts:
        if derived := store_amount("energy_kcal", amounts["energy_kj"].amount / 4.184, "off:kcal_from_kj"):
            amounts["energy_kcal"] = derived
    warnings = validate(name, amounts, (abv is not None and 0 < abv <= 100) or bool(ALCOHOL.intersection(tags)))
    modified = row.get("last_modified_t")
    updated = datetime.fromtimestamp(modified, timezone.utc) if modified and 0 < modified < 7258118400 else None
    omega_mg = raw["omega-3-fat"] * MG_PER_G if "omega_3" in amounts else None
    brand = clean((row.get("brands") or "").split(",", 1)[0], 160)
    return Food("off", code, version, name, brand, sorted(names), markets, barcode, updated,
                sorted(amounts.values(), key=lambda n: n.code), category, omega_mg, warnings, basis,
                off_serving(row.get("serving_size"), basis))


def staple_applies(entry: dict, description: str) -> bool:
    """A curated entry that names its source description only applies to a row that still has it."""
    guard = entry.get("source_description")
    return guard is None or normalize(guard) == normalize(description)


PORTION_STATS = ("total", "relabelled", "localized_de", "unmapped_head", "raw_marker")
UNMAPPED_HEADS_TOP = 25


@dataclass
class ImportNotes:
    """What the dry run reports about USDA portions and staples. Counting never changes what is written."""

    portions: Counter = field(default_factory=Counter)
    unmapped_heads: Counter = field(default_factory=Counter)
    staples_applied: int = 0
    description_mismatch: list[str] = field(default_factory=list)
    unmatched: dict[str, list[str]] = field(default_factory=dict)
    # The source grams each applied staple's curated portions got, so the operator can read them before a write.
    staple_portions: dict[str, dict[str, float]] = field(default_factory=dict)

    def record(self, fdc_id: str, portions: tuple[Portion, ...], tally: dict, applied: bool) -> None:
        for portion in portions:
            texts = (portion.label, *portion.labels.values(), portion.short_label_en or "", portion.short_label_de or "")
            self.portions["total"] += 1
            self.portions["relabelled"] += portion.label != portion.legacy_label
            self.portions["localized_de"] += bool(portion.labels.get("de"))
            self.portions["raw_marker"] += any(RAW_MARKER.search(text) for text in texts)
        self.portions["unmapped_head"] += tally.get("unmapped_head", 0)
        self.unmapped_heads.update(tally.get("unmapped_heads", ()))
        self.staples_applied += applied
        if applied:
            self.staple_portions[fdc_id] = {portion.portion_key: portion.weight_g
                                            for portion in portions if portion.portion_key}
        if tally.get("unmatched"):
            self.unmatched[fdc_id] = tally["unmatched"]

    def summary(self) -> dict:
        # A list, not a dict: the dry-run line is written with sorted keys, which would lose the ranking.
        top = sorted(self.unmapped_heads.items(), key=lambda item: (-item[1], item[0]))[:UNMAPPED_HEADS_TOP]
        return {"portions": {name: self.portions[name] for name in PORTION_STATS},
                "unmapped_heads": [{"head": head, "count": count} for head, count in top],
                "staples": {"applied": self.staples_applied, "description_mismatch": sorted(self.description_mismatch),
                            "unmatched": dict(sorted(self.unmatched.items()))},
                "staple_portions": dict(sorted(self.staple_portions.items()))}


def usda_food(row: dict, version: str, notes: ImportNotes | None = None) -> Food:
    external_id = row["fdc_id"]
    if not re.fullmatch(r"[1-9][0-9]{0,19}", external_id or ""):
        raise Reject("invalid_fdc_id")
    name = clean(row["description"], 200)
    amounts: dict[str, Nutrient] = {}
    priorities = {}
    for entry in row["nutrients"] or ():
        id = entry["id"]
        code, unit = USDA_NUTRIENTS[id]
        val = number(entry["amount"])
        source = (entry.get("unit") or unit).lower().replace("µ", "u").replace("μ", "u")
        if val is None:
            continue
        if source in UNIT_MASS_G and unit in UNIT_MASS_G:
            val = val * UNIT_MASS_G[source] / UNIT_MASS_G[unit]
        elif source != unit.lower():
            continue
        priority = USDA_ENERGY_PRIORITY.get(id, 0)
        if code in priorities and priority <= priorities[code]:
            continue
        if nutrient := store_amount(code, val, f"fdc:{id}"):
            amounts[code] = nutrient
            priorities[code] = priority
    if "energy_kcal" not in amounts and "energy_kj" in amounts:
        if derived := store_amount("energy_kcal", amounts["energy_kj"].amount / 4.184, "fdc:kcal_from_kj"):
            amounts["energy_kcal"] = derived
    alcohol = amounts.get("alcohol")
    warnings = validate(name, amounts, alcohol is not None and alcohol.amount > 0)
    category = classify(name, [row["category"]] if row.get("category") else [], provider="usda")
    try:
        updated = datetime.strptime(row.get("publication_date") or "", "%Y-%m-%d").replace(tzinfo=timezone.utc)
    except ValueError:
        updated = None
    staple = STAPLES["usda"].get(external_id)
    if staple and not staple_applies(staple, name):
        if notes is not None:
            notes.description_mismatch.append(external_id)
        staple = None  # A wrong FDC ID or a renamed release imports as a plain USDA food.
    tally: dict | None = {} if notes is not None else None
    portions = usda_measures(row.get("portions"), staple["portions"] if staple else None, tally)
    if notes is not None:
        notes.record(external_id, portions, tally, staple is not None)
    return Food("usda", external_id, version, staple["names"]["en"] if staple else name, "",
                list(staple["names"].items()) if staple else [("en", name)], [], None, updated,
                sorted(amounts.values(), key=lambda n: n.code), category, None, warnings,
                portions=portions,
                is_canonical_staple=staple.get("canonical", True) if staple else False,
                canonical_group_key=staple["group"] if staple else None,
                food_form=staple["form"] if staple else None, subtitles=staple["subtitles"] if staple else {},
                aliases=staple_aliases(staple, ("en", name)) if staple else ())


def staple_aliases(entry: dict, *extra: tuple[str, str]) -> tuple[tuple[str, str], ...]:
    preferred = set(entry["names"].items())
    aliases = [(lang, label) for lang, labels in entry["aliases"].items() for label in labels]
    return tuple(sorted(set(aliases + list(extra)) - preferred))


def bls_staple(external_id: str, version: str, amounts: dict[str, Nutrient] | None = None) -> Food:
    """A curated BLS staple. Its nutrients are the curated selection unless the caller brings the full CSV row's."""
    entry = STAPLES["bls"][external_id]
    name = entry["names"]["en"]
    if amounts is None:
        amounts = {code: Nutrient(code, amount, BLS_METHOD) for code, amount in entry["nutrients"].items()}
    portions = []
    for key, portion in sorted(entry["portions"].items(), key=lambda item: item[1]["order"]):
        ml = portion.get("ml")
        grams = round(ml * entry["density_g_per_ml"], 6) if ml is not None else portion["grams"]
        labels = portion["labels"]
        shorts = portion["short_labels"]
        portions.append(Portion(labels["en"], "portion", weight_g=grams, volume_ml=ml, portion_key=key, labels=labels,
                                short_label_en=shorts["en"], short_label_de=shorts["de"],
                                is_default=portion.get("default", False), sort_order=portion["order"]))
    alcohol = amounts.get("alcohol")
    return Food("bls", external_id, version, name, "", list(entry["names"].items()), ["DE"], None, None,
                sorted(amounts.values(), key=lambda n: n.code), classify(name, [], provider="bls"), None,
                validate(name, amounts, alcohol is not None and alcohol.amount > 0, bls=True), portions=tuple(portions),
                is_canonical_staple=True, canonical_group_key=entry["group"], food_form=entry["form"],
                subtitles=entry["subtitles"], aliases=staple_aliases(entry), attribution=BLS_ATTRIBUTION,
                density_g_ml=entry.get("density_g_per_ml"))


def bls_food(row: dict, version: str) -> Food:
    """The staples-only path, used while no CSV extract is committed."""
    return bls_staple(row["external_id"], version)


class Skip(Exception):
    """A row that is deliberately not imported (a twin of a curated staple). It is not a quality rejection."""


@dataclass(frozen=True)
class Curation:
    groups: tuple[tuple[str, ...], ...]
    patterns: tuple[tuple[tuple[str, re.Pattern[str]], ...], ...]  # per group: (member, whole-word pattern)
    twins: dict[str, dict[str, str]]
    usda_labels: dict[str, tuple[str, ...]]  # twin_keys of a German staple name or alias -> "usda:<fdc_id>" keys


BLS_STATE_WORDS = re.compile(r"(?<!\w)(?:roh|gekocht|gegart|gebraten|gebacken|getrocknet|tiefgefroren|tiefgekühlt)(?!\w)",
                             re.IGNORECASE)


def twin_keys(name: str) -> set[str]:
    """The keys under which a German name proposes a twin (dry-run reporting only, never what is written).

    BLS 4.0 adds the state after a space ("Banane roh") or a comma ("Möhre, roh") and sometimes splits a compound
    ("Hafer Flocken"). So: the whole name, its first comma segment, the name without its state words, and that
    without spaces.
    """
    first = normalize(name.split(",", 1)[0])
    bare = normalize(BLS_STATE_WORDS.sub(" ", name))
    return {key for key in (normalize(name), first, bare, bare.replace(" ", "")) if len(key) >= 2}


def curation_error(message: str) -> ValueError:
    return ValueError(f"bls_curation.v1.json: {message}")


def parse_curation(data: object) -> Curation:
    """Validate the curation data (synonym groups and twins) and compile it. Anything odd stops the run."""
    if not isinstance(data, dict) or set(data) != {"version", "synonyms", "twins"} or data["version"] != 1:
        raise curation_error("expected version 1 with the keys synonyms and twins")
    if not isinstance(data["synonyms"], list) or not isinstance(data["twins"], dict):
        raise curation_error("synonyms must be a list of groups and twins an object")
    groups = []
    for index, group in enumerate(data["synonyms"]):
        if not isinstance(group, list) or len(group) < 2:
            raise curation_error(f"synonym group {index} needs at least two members")
        if not all(isinstance(member, str) and member and member == member.strip() for member in group):
            raise curation_error(f"synonym group {index} has an empty or padded member")
        members = tuple(unicodedata.normalize("NFC", member) for member in group)
        if len(set(members)) != len(members):
            raise curation_error(f"synonym group {index} repeats a member")
        groups.append(members)
    twins = {}
    for code, entry in data["twins"].items():
        if not BLS_ID.fullmatch(code) or code in STAPLES["bls"]:
            raise curation_error(f"twin {code!r} is not a BLS code of a food outside the curated BLS staples")
        if not isinstance(entry, dict) or set(entry) != {"staple", "name_de"}:
            raise curation_error(f"twin {code} needs exactly staple and name_de")
        staple, name = entry["staple"], entry["name_de"]
        if not isinstance(staple, str) or staple.removeprefix("usda:") not in STAPLES["usda"] \
                or not staple.startswith("usda:"):
            raise curation_error(f"twin {code} names {staple!r}, which is no curated USDA staple")
        if not isinstance(name, str) or len(normalize(name)) < 2:
            raise curation_error(f"twin {code} needs the expected German name")
        twins[code] = {"staple": staple, "name_de": unicodedata.normalize("NFC", name)}
    labels: dict[str, set[str]] = {}
    for fdc_id, staple in STAPLES["usda"].items():
        for label in (staple["names"].get("de"), *staple["aliases"].get("de", ())):
            for key in twin_keys(label or ""):
                labels.setdefault(key, set()).add(f"usda:{fdc_id}")
    return Curation(
        tuple(groups),
        tuple(tuple((member, re.compile(rf"(?<!\w){re.escape(member)}(?!\w)")) for member in group)
              for group in groups),
        twins, {label: tuple(sorted(keys)) for label, keys in labels.items()})


def load_curation(path: Path = BLS_CURATION_PATH) -> Curation:
    return parse_curation(loads(path.read_text(encoding="utf-8")))


def synonym_aliases(curation: Curation, name_de: str, taken: set[str]) -> tuple[tuple[str, str], ...]:
    """German aliases from the curated synonym groups. Substitution, not translation.

    Every member that is a whole word of the (case-sensitive) name is replaced by each other member of its group.
    Groups and members are walked in file order, so the result is deterministic. An alias whose normalized form is
    already a name of the food (`taken`) or an earlier alias is dropped, and at most BLS_ALIAS_LIMIT remain.
    """
    seen = set(taken)
    aliases: list[tuple[str, str]] = []
    for group, patterns in zip(curation.groups, curation.patterns):
        for member, pattern in patterns:
            if not pattern.search(name_de):
                continue
            for other in group:
                if other == member:
                    continue
                alias = clean(pattern.sub(lambda _, other=other: other, name_de), 200)
                key = normalize(alias)
                if len(key) < 2 or key in seen:
                    continue
                seen.add(key)
                aliases.append(("de", alias))
                if len(aliases) == BLS_ALIAS_LIMIT:
                    return tuple(aliases)
    return tuple(aliases)


@dataclass
class BlsNotes:
    """What the dry run reports about the full BLS import. Counting never changes what is written."""

    aliases_added: int = 0
    skipped_twin: int = 0
    twin_mismatch: dict[str, dict[str, str]] = field(default_factory=dict)
    twin_candidates: dict[str, dict] = field(default_factory=dict)
    staple_nutrient_drift: dict[str, dict[str, list]] = field(default_factory=dict)
    staples_seen: set[str] = field(default_factory=set)
    twins_seen: set[str] = field(default_factory=set)

    def stale_twins(self, conn=None) -> dict[str, dict]:
        """Twin codes of the curation that appear in the CSV. With a connection, whether the catalog already holds a
        `bls` source for each (a twin added after a write leaves the earlier import behind). Read-only."""
        found: set[str] = set()
        if conn is not None and self.twins_seen:
            rows = conn.execute("SELECT external_id FROM nutrition.food_sources WHERE provider='bls' AND external_id = ANY(%s)",
                                (sorted(self.twins_seen),)).fetchall()
            found = {code for (code,) in rows}
        return {code: {"imported": None if conn is None else code in found} for code in sorted(self.twins_seen)}

    def summary(self, conn=None) -> dict:
        return {"bls_full": True, "aliases_added": self.aliases_added, "skipped_twin": self.skipped_twin,
                "twin_mismatch": dict(sorted(self.twin_mismatch.items())),
                "twin_candidates": dict(sorted(self.twin_candidates.items())),
                "stale_twins": self.stale_twins(conn),
                "staple_nutrient_drift": dict(sorted(self.staple_nutrient_drift.items())),
                "staples_missing": sorted(set(STAPLES["bls"]) - self.staples_seen)}


def bls_amounts(row: dict) -> dict[str, Nutrient]:
    amounts: dict[str, Nutrient] = {}
    for code, text in row.items():
        if code in BLS_NUTRIENT_CODES and text and (value := store_amount(code, number(text), BLS_METHOD)):
            amounts[code] = value
    if "energy_kcal" not in amounts and "energy_kj" in amounts:
        if derived := store_amount("energy_kcal", amounts["energy_kj"].amount / 4.184, "bls:kcal_from_kj"):
            amounts["energy_kcal"] = derived
    return amounts


def bls_food_form(name: str) -> str | None:
    return next((form for pattern, form in BLS_FORMS if pattern.search(name)), None)


def staple_drift(curated: dict[str, float], amounts: dict[str, Nutrient]) -> dict[str, list]:
    """Nutrients of a curated staple whose CSV value differs by more than 1 %: {code: [curated, csv or None]}."""
    drift = {}
    for code, value in curated.items():
        found = amounts.get(code)
        other = found.amount if found else None
        if other is None or abs(other - value) > abs(value) * STAPLE_DRIFT_FRACTION:
            drift[code] = [value, other]
    return drift


def bls_row_food(row: dict, version: str, curation: Curation, notes: BlsNotes | None = None) -> Food:
    """One row of data/bls_4_0.csv as a catalog food.

    A curated staple keeps today's overlay (names, aliases, portions, group, form, density) and takes its nutrients from
    the row. A twin of a USDA staple whose German name still matches its guard is skipped (`Skip`); one whose name
    changed imports normally and is reported. Every other food is a plain generic food with a category hint, a form
    from its German state words, no portions and the curated synonym aliases.
    """
    code = (row.get("bls_code") or "").strip()
    if not BLS_ID.fullmatch(code):
        raise Reject("invalid_bls_code")
    name_de = clean(unicodedata.normalize("NFC", row.get("name_de") or ""), 200)
    name_en = clean(unicodedata.normalize("NFC", row.get("name_en") or ""), 200)
    amounts = bls_amounts(row)
    staple = STAPLES["bls"].get(code)
    if staple:
        if notes is not None:
            notes.staples_seen.add(code)
            if drift := staple_drift(staple["nutrients"], amounts):
                notes.staple_nutrient_drift[code] = drift
        return bls_staple(code, version, amounts)
    twin = curation.twins.get(code)
    if twin:
        if notes is not None:
            notes.twins_seen.add(code)
        if normalize(twin["name_de"]) == normalize(name_de):
            if notes is not None:
                notes.skipped_twin += 1
            raise Skip(code)
        if notes is not None:
            notes.twin_mismatch[code] = {"expected": twin["name_de"], "found": name_de}
    if notes is not None and (staples := sorted({key for name in twin_keys(name_de)
                                                 for key in curation.usda_labels.get(name, ())})):
        notes.twin_candidates[code] = {"name_de": name_de, "staples": staples}
    alcohol = amounts.get("alcohol")
    warnings = validate(name_de, amounts, alcohol is not None and alcohol.amount > 0, bls=True)
    names = [("de", name_de)]
    if len(normalize(name_en)) >= 2:
        names.append(("en", name_en))
    aliases = synonym_aliases(curation, name_de, {normalize(label) for _, label in names})
    if notes is not None:
        notes.aliases_added += len(aliases)
    group = BLS_MAIN_GROUPS.get(code[0])
    return Food("bls", code, version, name_de, "", names, ["DE"], None, None,
                sorted(amounts.values(), key=lambda n: n.code), classify(name_de, [group] if group else [], provider="bls"),
                None, warnings, food_form=bls_food_form(name_de), aliases=aliases, attribution=BLS_ATTRIBUTION)


def bls_csv_rows(path: Path, batch_size: int, limit: int | None = None):
    """Batches of dict rows from the committed extract.

    The whole file is read and checked (columns, field counts, BLS codes, duplicates, numbers) before the first batch
    exists, so a bad extract stops the run before anything is processed or written. `limit` applies afterwards.
    """
    rows: list[dict] = []
    codes: set[str] = set()
    with path.open(encoding="utf-8", newline="") as stream:
        reader = csv.DictReader(stream)
        header = tuple(reader.fieldnames or ())
        unknown = [column for column in header[len(BLS_CSV_FIXED):] if column not in BLS_NUTRIENT_CODES]
        if header[:len(BLS_CSV_FIXED)] != BLS_CSV_FIXED or unknown or len(set(header)) != len(header):
            raise ValueError(f"{path.name} does not have the columns {', '.join(BLS_CSV_FIXED)} and registry nutrients"
                             + (f" (unknown: {', '.join(unknown)})" if unknown else ""))
        for row in reader:
            where = f"{path.name} line {reader.line_num}"
            if None in row or None in row.values():
                raise ValueError(f"{where} does not have {len(header)} fields")
            code = row["bls_code"]
            if not BLS_ID.fullmatch(code):
                raise ValueError(f"{where}: {code[:20]!r} is not a BLS code")
            if code in codes:
                raise ValueError(f"{where}: BLS code {code} appears twice")
            codes.add(code)
            for column in header[len(BLS_CSV_FIXED):]:
                if row[column] and number(row[column]) is None:
                    raise ValueError(f"{where}: {column} of {code} is not a number")
            rows.append(row)
    if limit is not None:
        rows = rows[:limit]
    return iter([rows[start:start + batch_size] for start in range(0, len(rows), batch_size)])


def verify_bls_extract(csv_path: Path) -> None:
    """The CSV must be the one its manifest describes (`csv_sha256`), so a torn pair is refused."""
    manifest_path = csv_path.with_suffix(".manifest.json")
    try:
        manifest = loads(manifest_path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        raise ValueError(f"{manifest_path.name} is missing next to {csv_path.name}; regenerate both with bls_convert.py") from None
    except ValueError:
        raise ValueError(f"{manifest_path.name} is not valid JSON; regenerate both with bls_convert.py") from None
    expected = manifest.get("csv_sha256") if isinstance(manifest, dict) else None
    if expected != sha256(csv_path.read_bytes()).hexdigest():
        raise ValueError(f"{csv_path.name} does not match the csv_sha256 in {manifest_path.name}; "
                         "regenerate both with bls_convert.py")


def bls_input_digest(csv_path: Path) -> str:
    """The digest that makes the BLS dataset version: the CSV, the curation, the staples and the transform version."""
    digest = sha256()
    for part in (csv_path.read_bytes(), BLS_CURATION_PATH.read_bytes(), STAPLES_PATH.read_bytes(),
                 BLS_TRANSFORM_VERSION.encode()):
        digest.update(sha256(part).digest())  # hash of hashes: no input can borrow bytes from its neighbour
    return digest.hexdigest()
