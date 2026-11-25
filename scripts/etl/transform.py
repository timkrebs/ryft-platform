"""Normalize projected provider rows into the existing RYFT catalog model."""

from __future__ import annotations

import math
import re
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from hashlib import sha256
from json import dumps

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

    @property
    def checksum(self) -> bytes:
        data = asdict(self)
        data.pop("version")
        if not self.portions:
            data.pop("portions")
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


def usda_measures(rows: list[dict] | None) -> tuple[Portion, ...]:
    result = {}
    for row in (rows or [])[:50]:
        desc = row.get("description")
        if not desc and (row.get("measure") or row.get("modifier")):
            desc = " ".join(part for key in ("amount", "measure", "modifier") if (part := row.get(key)))
        grams = measured_quantity(row.get("grams"))
        if not desc or len(desc) > 200 or "\0" in desc or grams is None:
            continue
        portion = Portion(desc, "portion", weight_g=grams)
        result[portion] = None
    return tuple(result)


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


def usda_food(row: dict, version: str) -> Food:
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
    return Food("usda", external_id, version, name, "", [("en", name)], [], None, updated,
                sorted(amounts.values(), key=lambda n: n.code), category, None, warnings,
                portions=usda_measures(row.get("portions")))
