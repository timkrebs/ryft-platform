"""Deterministic catalog tags and conservative scoring profiles."""

from __future__ import annotations

import re
import unicodedata
from dataclasses import dataclass


def normalize(value: str) -> str:
    folded = unicodedata.normalize("NFD", value.lower().replace("ß", "ss"))
    text = "".join(c if c.isalnum() else " " for c in folded if not unicodedata.category(c).startswith("M"))
    return " ".join(text.split())


def words(pattern: str) -> re.Pattern[str]:
    return re.compile(rf"\b(?:{pattern})\b")


RULES = [
    ("starchy_veg", words(r"potato(?:es)?|kartoffeln?|susskartoffeln?|sweet potato(?:es)?|mais|corn|cassava|maniok|yuca")),
    ("healthy_fat", words(r"olive oils?|olivenol|rapeseed oils?|canola oils?|rapsol|linseed oils?|flaxseed oils?|leinol|avocados?")),
    ("red_processed_meat", words(r"beef|rind(?:erfleisch)?|pork|schwein(?:efleisch)?|lamb|lamm(?:fleisch)?|sausages?|wurst|wurste|ham|schinken|bacon|speck|processed meat")),
    ("whole_grain", words(r"whole grains?|whole wheat|wholemeal|vollkorn(?:brot|nudeln)?|oats?|oatmeal|hafer(?:flocken)?|brown rice|naturreis|quinoa")),
    ("refined_grain", words(r"white bread|weissbrot|white (?:rice|pasta)|nudeln|pasta|pastr(?:y|ies)|geback|croissants?|bakery products|baked products|biscuits?")),
    ("nuts_legumes", words(r"nuts?|nusse|seeds?|samen|lentils?|linsen|beans?|bohnen|chickpeas?|kichererbsen|tofu|almonds?|mandeln|walnuts?|walnusse|peanuts?|erdnusse|legumes?")),
    ("fish_seafood", words(r"fish|fisch|salmon|lachs|tuna|thunfisch|shrimps?|garnelen|meeresfruchte|seafood|finfish|shellfish|cod|kabeljau")),
    ("poultry_eggs", words(r"chicken|hahnchen(?:brust)?|huhn(?:erfleisch)?|turkey|pute|poultry|geflugel|eggs?|eier")),
    ("dairy", words(r"milk|milch|yog(?:h)?urts?|joghurts?|cheeses?|kase|quark|dairy")),
    ("non_starchy_veg", words(r"vegetables?|gemuse|salat[ea]?|lettuce|broccoli|brokkoli|karotten?|carrots?|spinach|spinat|tomat(?:o|oes|en)|gurken?|cucumbers?|paprika|cauliflower|blumenkohl")),
    ("whole_fruit", words(r"fruits?|obst|fruchte|apples?|apfel|pears?|birnen|bananas?|bananen|berries|beeren|erdbeeren|strawberries|oranges?|grapes?|trauben")),
]

JUICE_OR_SODA = words(r"juices?|(?:frucht|obst|apfel|orangen)saft|safte|saft|nectars?|nektare?|soft drinks?|sodas?|lemonades?|limonaden?|cola")
SWEETS = words(r"cand(?:y|ies)|sweets?|sussigkeiten|syrups?|sirup|chocolate|schokolade|sugars?|zucker")
DRIED_FRUIT = words(r"dried|getrocknet[eorsn]*|dehydrated|raisins?|rosinen|candied|kandiert[eorsn]*|sweetened|gezuckert[eorsn]*")
MIXED = words(r"mixed|mixture|mix|with|mit|pizza|soup|suppe|stew|eintopf|prepared meals?|ready meals?|composite foods?|sandwich(?:es)?|gefullt|filled|sauces?|sossen?|dressing|smoothies?")
PROCESSED = words(r"dried|getrocknet|dehydrated|concentrate|konzentrat|powder|pulver|sweetened|gezuckert|flavoured|flavored|seasoned|breaded|paniert|chips|crisps")
LABEL_CONFLICT = words(r"style|flavou?r|geschmack|imitation|ersatz|artificial|plant based|vegan|extract|extrakt")
SIMPLE = {
    "non_starchy_veg": "non_starchy_vegetable_fraction",
    "whole_fruit": "whole_fruit_fraction",
    "nuts_legumes": "nuts_legumes_fraction",
    "red_processed_meat": "red_processed_meat_fraction",
}
RAW = words(r"raw|fresh|frozen|roh|frisch|tiefgekuhlt|uncooked")


@dataclass(frozen=True)
class Category:
    name: str
    confidence: float
    profile: dict[str, float]


def classify(name: str, categories: list[str] | tuple[str, ...], *, provider: str) -> Category:
    label = normalize(name)
    hints = normalize(" ".join(categories))
    beverage_hint = any(JUICE_OR_SODA.search(hint) and hint != "fruits and fruit juices"
                        for hint in map(normalize, categories))
    if JUICE_OR_SODA.search(label) or beverage_hint:
        return Category("added_sugar", 0.95, {"whole_fruit_fraction": 0, "sugary_drink": 1})
    mixed = bool(MIXED.search(label) or MIXED.search(hints) or LABEL_CONFLICT.search(label))
    if SWEETS.search(label) and not mixed:
        return Category("added_sugar", 0.85, {})
    matches = [category for category, rule in RULES if rule.search(label)]
    if not matches:
        if SWEETS.search(hints) and not mixed:
            return Category("added_sugar", 0.60, {})
        matches = [category for category, rule in RULES if rule.search(hints)]
        if not matches or matches[0] == "whole_fruit" and DRIED_FRUIT.search(hints):
            return Category("other", 0, {})
        return Category(matches[0], 0.60, {})
    primary = matches[0]
    if primary == "whole_fruit" and DRIED_FRUIT.search(f"{label} {hints}"):
        return Category("other", 0, {})
    # A lexical label is enough for discovery. Scoring needs evidence that the edible mass
    # is essentially one ingredient. USDA raw/basic names give that evidence for this subset.
    if mixed or len(matches) > 1 or PROCESSED.search(label) or provider != "usda" or not RAW.search(label):
        return Category(primary, 0.65 if mixed or len(matches) > 1 else 0.80, {})
    profile = {key: float(primary == group) for group, key in SIMPLE.items()}
    profile["sugary_drink"] = 0
    return Category(primary, 0.90, profile)
