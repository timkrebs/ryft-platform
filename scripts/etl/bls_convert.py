#!/usr/bin/env python3
"""Offline tool: turn the official BLS 4.0 data workbook into the reviewed CSV extract and its manifest.

Standard library only (zipfile and ElementTree), so it runs anywhere and is not part of the ETL image. The sheet is
streamed row by row, so peak memory does not grow with the number of rows or columns.

Assumed layout (documented by the publisher, checked here, never guessed): one header row in which every component
column starts with its EuroFIR-style code ("ENERCJ ... [kJ/100g]"); column 1 holds the BLS code (seven characters, see
BLS_CODE), column 2 the German and column 3 the English name; each component has a value column followed by a data
origin and a reference column. Anything that does not fit stops the run with a message and writes nothing.
"""

from __future__ import annotations

import argparse
import csv
import hashlib
import io
import json
import os
import posixpath
import re
import sys
import tempfile
import unicodedata
import zipfile
from collections import Counter
from decimal import ROUND_HALF_EVEN, Decimal, InvalidOperation
from pathlib import Path
from xml.etree import ElementTree as ET

CONVERTER_VERSION = 2
CSV_NAME = "bls_4_0.csv"
MANIFEST_NAME = "bls_4_0.manifest.json"
DEFAULT_OUT_DIR = Path(__file__).with_name("data")

# Archive caps, in the spirit of download.py: a bounded workbook, no duplicate members, no zip bombs.
SHEET_LIMIT = 768 * 1024**2
MEMBER_LIMIT = 64 * 1024**2
ARCHIVE_LIMIT = 1024 * 1024**2
MEMBER_COUNT_LIMIT = 2_000
HEADER_SEARCH_ROWS = 100
ERROR_CELLS = 120  # header cells quoted in an error message

# A BLS 4.0 code: the main-group letter, a digit, two digits or capital letters, three digits ("M111300", "M5B1600",
# "U57B100"; the workbook of 2025 has 765 codes with a letter in position 3 and 7 with one in position 4). The same
# pattern is transform.BLS_ID and the nutrition_bls_attribution constraint (migration 0062).
BLS_CODE = re.compile(r"[A-Z][0-9][0-9A-Z]{2}[0-9]{3}")
# The leading code of a header cell. Fatty acids carry a colon inside ("F20:5CN3"); a colon after the code is not part.
HEADER_CODE = re.compile(r"[A-Z][A-Z0-9]*(?::[A-Z0-9]+)*(?![A-Za-z0-9_])")
# A header cell that belongs to a component's data origin or reference, not to its value.
SIDE_COLUMN = re.compile(r"datenherkunft|herkunft|referenz|reference|origin|quelle|source", re.IGNORECASE)
NUMBER = re.compile(r"[+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?")
# A string cell that reads like a German thousands separator ("1.234" or "1.234,5") is ambiguous: stop, don't guess.
THOUSANDS = re.compile(r"\d{1,3}(?:\.\d{3})+(?:,\d+)?")
DECIMAL_COMMA = re.compile(r"\d+,\d+")
# Loose checks of the header cells of columns 1-3 (code, German name, English name); --name-columns overrides them.
ID_CODE = re.compile(r"code|schlüssel|sbls|bls", re.IGNORECASE)
ID_DE = re.compile(r"bezeichnung|name|deutsch", re.IGNORECASE)
ID_ENGLISH = re.compile(r"engl", re.IGNORECASE)
ID_EN = re.compile(r"engl|english|food name|name en|\(en\)", re.IGNORECASE)
SAMPLE_LIMIT = 5
UNIT_PER_100G = re.compile(r"(?<![A-Za-zµμ])(kcal|kj|mg|µg|μg|ug|g)\s*/\s*100\s*g(?![A-Za-z])", re.IGNORECASE)
UNIT_BRACKET = re.compile(r"[\[(]\s*(kcal|kj|mg|µg|μg|ug|g)\s*[\])]", re.IGNORECASE)
UNIT_NAMES = {"kcal": "kcal", "kj": "kJ", "mg": "mg", "µg": "ug", "μg": "ug", "ug": "ug", "g": "g"}
MASS_G = {"g": Decimal(1), "mg": Decimal("0.001"), "ug": Decimal("0.000001")}
QUANTUM = Decimal("0.000000001")  # nine decimals, like the catalog's own rounding
MAX_VALUE = Decimal(100_000_000)
# The value markers of BLS 4.0, exactly as the workbook writes them (D296). Each one's data-origin cell says what it
# means: "TR" comes with "Spuren" (traces), the limit markers with "Analyse", "Literatur", "Nährstoffdatenbank" or
# "Labelangabe" (below the limit of detection or quantification). Both are stored as 0, as for a measured trace. "-"
# comes with the origin "-" (no data) and stays unknown. Any other text is unparsed, and fails a required component.
BELOW_LIMIT = frozenset({"TR", "<LOD", "<LOQ", "<LOD or <LOQ"})
NO_VALUE = frozenset({"-"})

# Our nutrient code and its registry unit, in the order of the CSV columns. The set equals transform.BLS_NUTRIENT_CODES
# (a test pins that). BLS CHO is the available carbohydrate and therefore `carbohydrates`, never `carbohydrates_total`.
REGISTRY_UNITS = {
    "energy_kcal": "kcal", "energy_kj": "kJ", "protein": "g", "fat": "g", "carbohydrates": "g", "fiber": "g",
    "total_sugar": "g", "saturated_fat": "g", "monounsaturated_fat": "g", "polyunsaturated_fat": "g",
    "trans_fat": "g", "epa": "g", "dha": "g", "sodium": "mg", "potassium": "mg", "calcium": "mg",
    "magnesium": "mg", "iron": "mg", "zinc": "mg", "phosphorus": "mg", "selenium": "ug", "vitamin_a": "ug",
    "vitamin_c": "mg", "vitamin_d": "ug", "vitamin_e": "mg", "vitamin_k": "ug", "vitamin_b1": "mg",
    "vitamin_b2": "mg", "vitamin_b3": "mg", "vitamin_b6": "mg", "vitamin_b12": "ug", "folate": "ug",
    "caffeine": "mg", "alcohol": "g", "water": "g", "cholesterol": "mg",
}
# Our code -> candidate BLS component codes, best first. Only the leading token of a header cell is compared.
BLS_COMPONENTS = {
    "energy_kcal": ("ENERCC",), "energy_kj": ("ENERCJ",), "protein": ("PROT625",), "fat": ("FAT",),
    "carbohydrates": ("CHO", "CHOAVL"), "fiber": ("FIBT", "FIBTG"), "total_sugar": ("SUGAR",),
    "saturated_fat": ("FASAT",), "monounsaturated_fat": ("FAMS",), "polyunsaturated_fat": ("FAPU",),
    "trans_fat": ("FATRN",), "epa": ("F20:5CN3", "F20D5N3"), "dha": ("F22:6CN3", "F22D6N3"), "sodium": ("NA",),
    "potassium": ("K",),
    "calcium": ("CA",), "magnesium": ("MG",), "iron": ("FE",), "zinc": ("ZN",), "phosphorus": ("P",),
    "selenium": ("SE",), "vitamin_a": ("VITA",), "vitamin_c": ("VITC",), "vitamin_d": ("VITD",),
    "vitamin_e": ("VITE",), "vitamin_k": ("VITK",), "vitamin_b1": ("THIA",), "vitamin_b2": ("RIBF",),
    "vitamin_b3": ("NIA",), "vitamin_b6": ("VITB6", "VITB6A"), "vitamin_b12": ("VITB12",), "folate": ("FOL",),
    "caffeine": ("CAFFN",), "alcohol": ("ALC",), "water": ("WATER",), "cholesterol": ("CHORL",),
}
MACROS = ("protein", "fat", "carbohydrates")
ENERGY = ("energy_kcal", "energy_kj")
HEADER_CANDIDATES = frozenset(code for ours in (*ENERGY, *MACROS) for code in BLS_COMPONENTS[ours])


class ConvertError(Exception):
    """The workbook does not look like the documented layout, or the input is unsafe. Nothing is written."""


class TextCell(str):
    """A value read from a string cell (shared, inline or formula string) as opposed to a number cell."""


def local(tag: str) -> str:
    return tag.rsplit("}", 1)[-1]


def column_index(reference: str) -> int:
    """The 1-based column of a cell reference such as AB12."""
    letters = reference.rstrip("0123456789").upper()
    if not letters.isalpha() or not letters.isascii():
        raise ConvertError(f"cell reference without a column: {reference!r}")
    number = 0
    for char in letters:
        number = number * 26 + ord(char) - 64
    return number


def column_letters(number: int) -> str:
    letters = ""
    while number:
        number, rest = divmod(number - 1, 26)
        letters = chr(65 + rest) + letters
    return letters


def squash(text: str) -> str:
    return " ".join(unicodedata.normalize("NFC", text).replace("\x00", "").split())


def rich_text(node: ET.Element) -> str:
    """The text of a shared or inline string, runs joined, phonetic hints left out."""
    parts = []
    for child in node:
        name = local(child.tag)
        if name == "t":
            parts.append(child.text or "")
        elif name == "r":
            parts.extend(part.text or "" for part in child if local(part.tag) == "t")
    return "".join(parts)


def check_archive(archive: zipfile.ZipFile) -> None:
    infos = archive.infolist()
    if len(infos) > MEMBER_COUNT_LIMIT:
        raise ConvertError("the workbook archive has too many members")
    names = Counter(info.filename for info in infos)
    duplicates = sorted(name for name, count in names.items() if count > 1)
    if duplicates:
        raise ConvertError(f"duplicate archive members: {', '.join(duplicates)}")
    for info in infos:
        limit = SHEET_LIMIT if info.filename.startswith("xl/worksheets/") else MEMBER_LIMIT
        if info.file_size > limit:
            raise ConvertError(f"archive member {info.filename} exceeds its size limit")
    if sum(info.file_size for info in infos) > ARCHIVE_LIMIT:
        raise ConvertError("the workbook exceeds the total uncompressed size limit")


def parse_member(archive: zipfile.ZipFile, name: str) -> ET.Element:
    try:
        with archive.open(name) as stream:
            return ET.fromstring(stream.read())
    except KeyError:
        raise ConvertError(f"the archive has no {name}; this is not an xlsx workbook") from None


def list_sheets(archive: zipfile.ZipFile) -> list[tuple[str, str]]:
    workbook = parse_member(archive, "xl/workbook.xml")
    relations = parse_member(archive, "xl/_rels/workbook.xml.rels")
    targets = {node.get("Id"): node.get("Target") for node in relations.iter() if local(node.tag) == "Relationship"}
    members = {info.filename for info in archive.infolist()}
    sheets = []
    for node in workbook.iter():
        if local(node.tag) != "sheet":
            continue
        target = targets.get(next((value for key, value in node.attrib.items() if key.endswith("}id")), None))
        if not target:
            continue
        member = posixpath.normpath(target.lstrip("/") if target.startswith("/") else f"xl/{target}")
        if member in members:
            sheets.append((node.get("name") or member, member))
    if not sheets:
        raise ConvertError("the workbook lists no readable worksheet")
    return sheets


def load_shared_strings(archive: zipfile.ZipFile) -> list[str]:
    if "xl/sharedStrings.xml" not in {info.filename for info in archive.infolist()}:
        return []
    strings: list[str] = []
    with archive.open("xl/sharedStrings.xml") as stream:
        for _, element in ET.iterparse(stream, events=("end",)):
            if local(element.tag) == "si":
                strings.append(TextCell(rich_text(element)))
                element.clear()
    return strings


def cell_value(cell: ET.Element, shared: list[str]) -> str | None:
    kind = cell.get("t")
    text = None
    for child in cell:
        tag = child.tag
        if tag == "v" or tag.endswith("}v"):
            text = child.text
        elif kind == "inlineStr" and (tag == "is" or tag.endswith("}is")):
            return TextCell(rich_text(child))
    if text is None or kind in ("e", "b"):
        return None
    if kind == "s":
        try:
            return shared[int(text)]
        except (ValueError, IndexError):
            raise ConvertError("a cell refers to a shared string that does not exist") from None
    return TextCell(text) if kind == "str" else text


def iter_rows(archive: zipfile.ZipFile, member: str, shared: list[str]):
    """Yield (row number, {column number: text}) for every non-empty row, clearing each row after use."""
    row_number = 0
    columns: dict[str, int] = {}  # column letters -> number, so a cell reference is parsed once per column
    with archive.open(member) as stream:
        for _, element in ET.iterparse(stream, events=("end",)):
            if element.tag != "row" and not element.tag.endswith("}row"):
                continue
            try:
                row_number = int(element.get("r") or row_number + 1)
            except ValueError:
                row_number += 1
            cells: dict[int, str] = {}
            column = 0
            for cell in element:
                tag = cell.tag
                if tag != "c" and not tag.endswith("}c"):
                    continue
                reference = cell.get("r")
                if reference:
                    prefix = reference.rstrip("0123456789")
                    column = columns.get(prefix) or columns.setdefault(prefix, column_index(reference))
                else:
                    column += 1
                value = cell_value(cell, shared)
                if value is not None and value.strip():
                    cells[column] = value
            element.clear()
            if cells:
                yield row_number, cells


def header_code(text: str) -> str | None:
    found = HEADER_CODE.match(text)
    return found[0] if found else None


def describe(cells: dict[int, str]) -> str:
    shown = [f"{column_letters(column)}={text[:40]!r}" for column, text in sorted(cells.items())[:ERROR_CELLS]]
    more = f" (+{len(cells) - ERROR_CELLS} more)" if len(cells) > ERROR_CELLS else ""
    return "; ".join(shown) + more


def find_header(rows, seen: list[str], sheet: str) -> tuple[int, dict[int, str]] | None:
    """The first row with at least three required component codes as the leading token of a cell."""
    for row_number, cells in rows:
        if row_number > HEADER_SEARCH_ROWS:
            break
        texts = {column: squash(text) for column, text in cells.items()}
        if len(seen) < 5:
            seen.append(f"{sheet} row {row_number}: {describe(texts)}")
        if len({header_code(text) for text in texts.values()} & HEADER_CANDIDATES) >= 3:
            return row_number, texts
    return None


def locate_columns(header: dict[int, str]) -> dict[str, tuple[str, int, str]]:
    """Our code -> (BLS code, column, header text). A code in several value-like cells is ambiguous: stop."""
    by_code: dict[str, list[tuple[int, str]]] = {}
    for column, text in sorted(header.items()):
        if (code := header_code(text)) and not SIDE_COLUMN.search(text):
            by_code.setdefault(code, []).append((column, text))
    columns = {}
    for ours, candidates in BLS_COMPONENTS.items():
        for code in candidates:
            hits = by_code.get(code, [])
            if len(hits) > 1:
                where = ", ".join(f"{column_letters(column)}={text[:40]!r}" for column, text in hits)
                raise ConvertError(f"component {code} ({ours}) is the leading token of several header cells: {where}")
            if hits:
                columns[ours] = (code, hits[0][0], hits[0][1])
                break
    return columns


def header_unit(text: str) -> str | None:
    """The unit a header names, or None when it names none. Two different units are an error."""
    found = {UNIT_NAMES[unit.lower()] for unit in UNIT_PER_100G.findall(text)}
    if not found:
        found = {UNIT_NAMES[unit.lower()] for unit in UNIT_BRACKET.findall(text)}
    if len(found) > 1:
        raise ConvertError(f"the header names several units ({', '.join(sorted(found))}): {text[:80]!r}")
    return next(iter(found), None)


def unit_factor(source: str, target: str) -> Decimal:
    if source in MASS_G and target in MASS_G:
        return MASS_G[source] / MASS_G[target]
    if source == target:
        return Decimal(1)
    raise ConvertError(f"unit {source} cannot be converted to {target}")


def parse_number(text: str) -> Decimal | None:
    """A value per 100 g. German decimal commas are fine. Markers, text and negative numbers are unknown."""
    text = text.strip()
    if "," in text:
        if "." in text:
            return None
        text = text.replace(",", ".")
    if not NUMBER.fullmatch(text):
        return None
    try:
        value = Decimal(text)
    except InvalidOperation:
        return None
    if not value.is_finite() or value < 0 or value > MAX_VALUE:
        return None
    return Decimal(0) if value == 0 else value


def format_value(value: Decimal, factor: Decimal) -> str:
    scaled = (value * factor).quantize(QUANTUM, rounding=ROUND_HALF_EVEN)
    return format(scaled.normalize(), "f") if scaled else "0"


def file_digest(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def check_name_columns(header: dict[int, str]) -> None:
    """Columns 1-3 must look like the BLS code, the German and the English name (loose, on their header cells)."""
    code, german, english = (header.get(column, "") for column in (1, 2, 3))
    if not (ID_CODE.search(code) and ID_DE.search(german) and not ID_ENGLISH.search(german) and ID_EN.search(english)):
        raise ConvertError(f"the header cells of columns 1-3 do not look like code, German name and English name: "
                           f"A={code!r}, B={german!r}, C={english!r}; pass --name-columns CODE,DE,EN (1-based) "
                           "if the workbook orders them differently")


def convert(source: Path, sheet: str | None = None, units: dict[str, str] | None = None,
            out_dir: Path = DEFAULT_OUT_DIR, name_columns: tuple[int, int, int] | None = None) -> dict:
    """Read the workbook and write the CSV and the manifest. Returns the manifest.

    `name_columns` (1-based columns of the code, the German and the English name) replaces the default 1, 2, 3 and
    switches off the check of their header cells.
    """
    units = {key.upper(): value for key, value in (units or {}).items()}
    code_column, german_column, english_column = name_columns or (1, 2, 3)
    used_units: set[str] = set()
    with zipfile.ZipFile(source) as archive:
        check_archive(archive)
        shared = load_shared_strings(archive)
        sheets = list_sheets(archive)
        wanted = [pair for pair in sheets if sheet is None or pair[0] == sheet]
        if not wanted:
            raise ConvertError(f"no sheet named {sheet!r}; the workbook has: {', '.join(name for name, _ in sheets)}")
        seen: list[str] = []
        for sheet_name, member in wanted:
            rows = iter_rows(archive, member, shared)
            found = find_header(rows, seen, sheet_name)
            if found:
                break
            rows.close()
        else:
            raise ConvertError("no header row with at least three of "
                               f"{', '.join(sorted(HEADER_CANDIDATES))} in the first {HEADER_SEARCH_ROWS} rows; "
                               "rows seen: " + " | ".join(seen))
        try:
            header_row, header = found
            if name_columns is None:
                check_name_columns(header)
            columns = locate_columns(header)
            missing = [name for name in MACROS if name not in columns]
            if not any(name in columns for name in ENERGY):
                missing.append("energy (kcal or kJ)")
            if missing:
                wanted_codes = {name: "/".join(BLS_COMPONENTS[name]) for name in (*ENERGY, *MACROS)}
                raise ConvertError(
                    "required component missing: "
                    + ", ".join(f"{name} ({wanted_codes.get(name, 'ENERCC or ENERCJ')})" for name in missing)
                    + f"; header row {header_row} cells: {describe(header)}")
            plan: dict[str, tuple[str, int, str, str, str, Decimal]] = {}
            problems = []
            for ours, (code, column, text) in columns.items():
                override = units.get(code) or units.get(ours.upper())
                try:
                    if override:
                        if override.lower() not in UNIT_NAMES:
                            raise ConvertError(f"unknown unit {override!r} for {code}")
                        unit, where = UNIT_NAMES[override.lower()], "cli"
                        used_units.update(key for key in (code, ours.upper()) if key in units)
                    else:
                        unit, where = header_unit(text), "header"
                    if unit is None:
                        problems.append(f"{code} ({ours}, column {column_letters(column)}) has no unit in {text[:60]!r}; "
                                        f"pass --unit {code}=<g|mg|ug|kcal|kJ>")
                        continue
                    plan[ours] = (code, column, text, unit, where, unit_factor(unit, REGISTRY_UNITS[ours]))
                except ConvertError as error:
                    problems.append(f"{code} ({ours}): {error}")
            unused = sorted(set(units) - used_units)
            if unused:
                problems.append(f"--unit names no mapped component: {', '.join(unused)}")
            if problems:
                raise ConvertError("; ".join(problems))

            out_rows: dict[str, list[str]] = {}
            first_row: dict[str, int] = {}
            skipped: Counter = Counter()
            counts: Counter = Counter()
            unparsed: Counter = Counter()
            markers: dict[str, Counter] = {ours: Counter() for ours in plan}
            samples: dict[str, list[str]] = {ours: [] for ours in plan}
            thousands: list[tuple[str, str]] = []
            comma_text = False
            for row_number, cells in rows:
                code = cells.get(code_column, "").strip()
                if not BLS_CODE.fullmatch(code):
                    skipped["not_a_food_row"] += 1
                    continue
                name_de, name_en = squash(cells.get(german_column, "")), squash(cells.get(english_column, ""))
                if not name_de:
                    skipped["empty_german_name"] += 1
                    continue
                if code in out_rows:
                    raise ConvertError(f"BLS code {code} appears twice (rows {first_row[code]} and {row_number})")
                first_row[code] = row_number
                values = []
                for ours in REGISTRY_UNITS:
                    entry = plan.get(ours)
                    raw = cells.get(entry[1]) if entry else None
                    if raw is None:
                        values.append("")
                        continue
                    if isinstance(raw, TextCell) and (marker := raw.strip()) in BELOW_LIMIT | NO_VALUE:
                        markers[ours][marker] += 1
                        if marker in BELOW_LIMIT:
                            counts[ours] += 1
                            values.append("0")
                        else:
                            values.append("")
                        continue
                    if isinstance(raw, TextCell):
                        text = raw.strip()
                        comma_text = comma_text or bool(DECIMAL_COMMA.fullmatch(text))
                        if THOUSANDS.fullmatch(text):
                            comma_text = comma_text or "," in text
                            if len(thousands) < SAMPLE_LIMIT:
                                thousands.append((ours, text))
                    parsed = parse_number(raw)
                    if parsed is None:
                        unparsed[ours] += 1
                        if len(samples[ours]) < SAMPLE_LIMIT and raw.strip()[:20] not in samples[ours]:
                            samples[ours].append(raw.strip()[:20])
                        values.append("")
                        continue
                    counts[ours] += 1
                    values.append(format_value(parsed, entry[5]))
                out_rows[code] = [code, name_de, name_en, *values]
            problems = []
            if thousands and comma_text:
                problems.append("string cells look like German thousands separators next to decimal commas, so the "
                                "numbers are ambiguous: " + ", ".join(f"{ours}={text!r}" for ours, text in thousands))
            for ours in (*MACROS, *(name for name in ENERGY if name in plan)):
                if unparsed[ours]:
                    problems.append(f"required component {ours} ({plan[ours][0]}) has {unparsed[ours]} cell(s) that are "
                                    f"not numbers, e.g. {', '.join(repr(text) for text in samples[ours])}")
            if problems:
                raise ConvertError("; ".join(problems))
        finally:
            rows.close()
    if not out_rows:
        raise ConvertError(f"no food rows below header row {header_row}")

    buffer = io.StringIO()
    writer = csv.writer(buffer, lineterminator="\n")
    writer.writerow(["bls_code", "name_de", "name_en", *REGISTRY_UNITS])
    writer.writerows(out_rows[code] for code in sorted(out_rows))
    csv_text = buffer.getvalue()
    manifest = {
        "converter_version": CONVERTER_VERSION,
        # The import refuses a CSV whose digest differs from this one, so a pair that was torn apart is caught.
        "csv_sha256": hashlib.sha256(csv_text.encode("utf-8")).hexdigest(),
        "source": {"filename": source.name, "sha256": file_digest(source), "sheet": sheet_name,
                   "header_row": header_row, "name_columns": [code_column, german_column, english_column],
                   "id_headers": [header.get(column, "") for column in (code_column, german_column, english_column)]},
        "rows": {"written": len(out_rows), "skipped": sum(skipped.values()), "skipped_reasons": dict(skipped)},
        "components": {
            ours: {"bls_code": code, "header": text, "column": column_letters(column), "unit": unit,
                   "unit_source": where, "factor": format(factor.normalize(), "f"), "non_null": counts[ours],
                   "markers": dict(sorted(markers[ours].items())),
                   "unparsed": unparsed[ours], "unparsed_samples": samples[ours]}
            for ours, (code, column, text, unit, where, factor) in plan.items()},
        "markers": {"below_limit_as_zero": sorted(BELOW_LIMIT), "no_value": sorted(NO_VALUE)},
        "not_found": [ours for ours in REGISTRY_UNITS if ours not in plan],
    }
    write_outputs(out_dir, csv_text, json.dumps(manifest, indent=2, sort_keys=True, ensure_ascii=False) + "\n")
    return manifest


def write_outputs(out_dir: Path, csv_text: str, manifest_text: str) -> None:
    """Both files are written to temporary names first, so an error never leaves a half-written extract.

    Then the CSV is replaced, and the manifest last. A run that dies between the two replaces leaves a CSV that does not
    match the manifest's `csv_sha256`, and the import refuses that pair.
    """
    out_dir.mkdir(parents=True, exist_ok=True)
    staged: list[tuple[Path, Path]] = []
    try:
        for name, text in ((CSV_NAME, csv_text), (MANIFEST_NAME, manifest_text)):
            handle, temporary = tempfile.mkstemp(dir=out_dir, prefix=f"{name}.", suffix=".tmp")
            staged.append((Path(temporary), out_dir / name))
            with os.fdopen(handle, "w", encoding="utf-8", newline="") as stream:
                stream.write(text)
            os.chmod(temporary, 0o644)
        for temporary, destination in staged:
            os.replace(temporary, destination)
    finally:
        for temporary, _ in staged:
            temporary.unlink(missing_ok=True)


def unit_override(value: str) -> tuple[str, str]:
    code, separator, unit = value.partition("=")
    if not separator or not code.strip() or unit.strip().lower() not in UNIT_NAMES:
        raise argparse.ArgumentTypeError("expected CODE=unit with unit g, mg, ug, kcal or kJ")
    return code.strip(), unit.strip()


def name_columns_arg(value: str) -> tuple[int, int, int]:
    try:
        columns = tuple(int(part) for part in value.split(","))
    except ValueError:
        columns = ()
    if len(columns) != 3 or min(columns, default=0) < 1 or len(set(columns)) != 3:
        raise argparse.ArgumentTypeError("expected three different 1-based column numbers: CODE,DE,EN")
    return columns


def main(argv: list[str] | None = None) -> int:
    if sys.version_info < (3, 11):
        print(f"bls_convert: Python 3.11 or newer is required (this is {sys.version_info[0]}.{sys.version_info[1]})",
              file=sys.stderr)
        return 1
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("workbook", type=Path, help="BLS_4_0_Daten_2025_DE.xlsx")
    parser.add_argument("--sheet", help="sheet name (default: the first sheet with a recognizable header row)")
    parser.add_argument("--unit", action="append", default=[], type=unit_override, metavar="CODE=unit",
                        help="unit of a component whose header names none (BLS or RYFT code), e.g. SE=ug")
    parser.add_argument("--name-columns", type=name_columns_arg, metavar="CODE,DE,EN",
                        help="1-based columns of the BLS code, German and English name (default 1,2,3, checked "
                             "against their header cells; giving the option skips the check)")
    parser.add_argument("--out-dir", type=Path, default=DEFAULT_OUT_DIR, help="default: data/ next to this script")
    args = parser.parse_args(argv)
    try:
        manifest = convert(args.workbook, args.sheet, dict(args.unit), args.out_dir, args.name_columns)
    except ConvertError as error:
        print(f"bls_convert: {error}", file=sys.stderr)
        return 1
    except (OSError, zipfile.BadZipFile, ET.ParseError) as error:
        print(f"bls_convert: could not read {args.workbook.name}: {type(error).__name__}", file=sys.stderr)
        return 1
    rows = manifest["rows"]
    print(f"wrote {rows['written']} foods to {args.out_dir} (skipped {rows['skipped']} rows; "
          f"{len(manifest['components'])} components mapped, not found: {', '.join(manifest['not_found']) or 'none'})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
