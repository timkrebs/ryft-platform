"""Normalize projected provider rows into the existing RYFT catalog model."""

from __future__ import annotations

import math
import re
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
BLS_ATTRIBUTION = "Max Rubner-Institut (2025), Bundeslebensmittelschlüssel (BLS), Version 4.0"
USDA_PORTION_ID = re.compile(r"[1-9][0-9]{0,19}")
UNKNOWN_MEASURES = {"undetermined", "unspecified", "unknown", "not specified", "n/a", "none"}
STAPLES = loads(Path(__file__).with_name("staples.v1.json").read_text(encoding="utf-8"))


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


def keyed_entry(entry: dict) -> bool:
    return "match" in entry or "half_of" in entry


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
    a matched entry. `tally` collects what could not be applied, for the dry run.
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
    for name, entry in keyed.items():
        if "match" not in entry:
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
        if "half_of" not in entry:
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
    if tally is not None:
        tally["unmatched"] = unmatched
        tally["unmapped_head"] = sum(1 for key, (_, clean) in result.items() if key not in curated and clean.unmapped)
    portions = disambiguate([(portion, key in curated) for key, (portion, _) in result.items()])
    return tuple(sorted(portions, key=lambda portion: portion.sort_order))


def gtin(code: str) -> str | None:
    if not re.fullmatch(r"(?:[0-9]{8}|[0-9]{12,14})", code) or not code.strip("0"):
        return None
    total = sum(int(n) * (1 if pos % 2 == 0 else 3) for pos, n in enumerate(reversed(code)))
    return code.zfill(14) if total % 10 == 0 else None


def clean(value: str | None, limit: int) -> str:
    return (value or "").replace("\x00", "").strip()[:limit]


def validate(name: str, amounts: dict[str, Nutrient], alcohol: bool) -> tuple[str, ...]:
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


@dataclass
class ImportNotes:
    """What the dry run reports about USDA portions and staples. Counting never changes what is written."""

    portions: Counter = field(default_factory=Counter)
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
        self.staples_applied += applied
        if applied:
            self.staple_portions[fdc_id] = {portion.portion_key: portion.weight_g
                                            for portion in portions if portion.portion_key}
        if tally.get("unmatched"):
            self.unmatched[fdc_id] = tally["unmatched"]

    def summary(self) -> dict:
        return {"portions": {name: self.portions[name] for name in PORTION_STATS},
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


def bls_food(row: dict, version: str) -> Food:
    external_id = row["external_id"]
    entry = STAPLES["bls"][external_id]
    name = entry["names"]["en"]
    amounts = {code: Nutrient(code, amount, "bls:4.0") for code, amount in entry["nutrients"].items()}
    portions = []
    for key, portion in sorted(entry["portions"].items(), key=lambda item: item[1]["order"]):
        ml = portion.get("ml")
        grams = round(ml * entry["density_g_per_ml"], 6) if ml is not None else portion["grams"]
        labels = portion["labels"]
        shorts = portion["short_labels"]
        portions.append(Portion(labels["en"], "portion", weight_g=grams, volume_ml=ml, portion_key=key, labels=labels,
                                short_label_en=shorts["en"], short_label_de=shorts["de"],
                                is_default=portion.get("default", False), sort_order=portion["order"]))
    return Food("bls", external_id, version, name, "", list(entry["names"].items()), ["DE"], None, None,
                sorted(amounts.values(), key=lambda n: n.code), classify(name, [], provider="bls"), None,
                validate(name, amounts, False), portions=tuple(portions), is_canonical_staple=True,
                canonical_group_key=entry["group"], food_form=entry["form"], subtitles=entry["subtitles"],
                aliases=staple_aliases(entry), attribution=BLS_ATTRIBUTION,
                density_g_ml=entry.get("density_g_per_ml"))
