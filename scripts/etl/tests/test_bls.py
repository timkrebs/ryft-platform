"""The full BLS 4.0 import (G4, D296): the offline converter, the curation, the row transform and the CLI.

The workbook fixtures are built at runtime with zipfile, so no data file is committed for the tests.
"""

import csv
import hashlib
import io
import json
import os
import subprocess
import sys
import unittest
import uuid
from contextlib import redirect_stderr, redirect_stdout
from decimal import Decimal
from functools import partial
from pathlib import Path
from tempfile import TemporaryDirectory
from unittest.mock import patch
from xml.sax.saxutils import escape
import zipfile

import psycopg

import transform
from bls_convert import (BLS_COMPONENTS, REGISTRY_UNITS, ConvertError, column_letters, convert, parse_number,
                         unit_factor)
from bls_convert import main as convert_main
from categorizer import classify
from import_pg import IMPORT_NAMESPACE
from run_etl import process, refuse_staples_over_full_bls
from transform import (BLS_ATTRIBUTION, BLS_CSV_PATH, BLS_CURATION_PATH, BLS_NUTRIENT_CODES, STAPLES, STAPLES_PATH,
                       USDA_NUTRIENTS, BlsNotes, Reject, Skip, bls_csv_rows, bls_food, bls_input_digest, bls_row_food,
                       load_curation, parse_curation, staple_aliases, verify_bls_extract)

ETL_DIR = Path(__file__).resolve().parents[1]
NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
MEBIBYTE = 1024**2


class Cells:
    """Builds sheet rows as XML and collects the shared strings they use."""

    def __init__(self):
        self.shared: list[str] = []
        self.lookup: dict[str, int] = {}

    def index(self, text: str) -> int:
        if text not in self.lookup:
            self.lookup[text] = len(self.shared)
            self.shared.append(text)
        return self.lookup[text]

    def row_xml(self, number: int, cells) -> str:
        """cells: (column, kind, value). Kinds: n number, s shared string, i inline string, str formula string."""
        parts = [f'<row r="{number}">']
        for column, kind, value in cells:
            ref = f"{column_letters(column)}{number}"
            if kind == "n":
                parts.append(f'<c r="{ref}"><v>{value}</v></c>')
            elif kind == "s":
                parts.append(f'<c r="{ref}" t="s"><v>{self.index(value)}</v></c>')
            elif kind == "i":
                parts.append(f'<c r="{ref}" t="inlineStr"><is><t>{escape(value)}</t></is></c>')
            else:
                parts.append(f'<c r="{ref}" t="str"><v>{escape(value)}</v></c>')
        parts.append("</row>")
        return "".join(parts)


def write_xlsx(path: Path, sheets: dict, shared: list[str]) -> None:
    """sheets: name -> iterable of row XML. The shared strings are written last, after the rows were generated."""
    names = list(sheets)
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as archive:
        archive.writestr("[Content_Types].xml", '<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>')
        archive.writestr("xl/workbook.xml", f'<?xml version="1.0"?><workbook xmlns="{NS}" xmlns:r="{REL}"><sheets>'
                         + "".join(f'<sheet name="{escape(name)}" sheetId="{i}" r:id="rId{i}"/>' for i, name in enumerate(names, 1))
                         + "</sheets></workbook>")
        archive.writestr("xl/_rels/workbook.xml.rels",
                         '<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
                         + "".join(f'<Relationship Id="rId{i}" Type="{REL}/worksheet" Target="worksheets/sheet{i}.xml"/>'
                                   for i in range(1, len(names) + 1)) + "</Relationships>")
        for i, name in enumerate(names, 1):
            with archive.open(f"xl/worksheets/sheet{i}.xml", "w", force_zip64=True) as out:
                out.write(f'<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="{NS}"><sheetData>'.encode())
                for chunk in sheets[name]:
                    out.write(chunk.encode())
                out.write(b"</sheetData></worksheet>")
        archive.writestr("xl/sharedStrings.xml", f'<?xml version="1.0"?><sst xmlns="{NS}" count="{len(shared)}" uniqueCount="{len(shared)}">'
                         + "".join(f"<si><t>{escape(text)}</t></si>" for text in shared) + "</sst>")


FIXTURE_COMPONENTS = (  # BLS code, header text of the value column
    ("ENERCJ", "ENERCJ Energie (Kilojoule) [kJ/100g]"),
    ("ENERCC", "ENERCC Energie (Kilokalorien) [kcal/100g]"),
    ("PROT625", "PROT625 Protein (Nx6,25) [g/100g]"),
    ("FAT", "FAT Fett [g/100g]"),
    ("CHO", "CHO Kohlenhydrate, verfügbar [g/100g]"),
    ("FIBT", "FIBT Ballaststoffe [g/100g]"),
    ("NA", "NA Natrium [mg/100g]"),
    ("SE", "SE Selen [µg/100g]"),
    ("F20D5N3", "F20D5N3 Eicosapentaensäure [mg/100g]"),
    ("ZZUNKNOWN", "ZZUNKNOWN Nicht abgebildet [g/100g]"),
)
# code, German name, English name, {BLS code: (cell kind, value)}. Written out of order on purpose.
FIXTURE_FOODS = (
    ("F200000", "Apfel, roh", None, {
        "ENERCJ": ("str", "218,5"), "ENERCC": ("i", "52"), "PROT625": ("i", "0,3"), "FAT": ("str", "0,4"),
        "CHO": ("i", "11,4"), "FIBT": ("i", "-"), "NA": ("i", "1")}),
    ("B100000", "Brötchen, Weizen", "Bread roll, wheat", {
        "ENERCJ": ("n", "1000"), "ENERCC": ("n", "239"), "PROT625": ("n", "8.5"), "FAT": ("n", "1.2"),
        "CHO": ("n", "47.3"), "FIBT": ("n", "3.1"), "NA": ("n", "480"), "SE": ("n", "12.5"),
        "F20D5N3": ("n", "120"), "ZZUNKNOWN": ("n", "9")}),
    # The German name arrives decomposed (o + combining diaeresis), as some spreadsheet exports write it.
    ("G300000", "Möhre, roh", "Carrot, raw", {
        "ENERCJ": ("n", "130"), "ENERCC": ("n", "31"), "PROT625": ("n", "0.9"), "FAT": ("n", "0"),
        "CHO": ("n", "6.4"), "FIBT": ("n", "2.8")}),
)


def fixture_workbook(path: Path, components=FIXTURE_COMPONENTS, foods=FIXTURE_FOODS,
                     ids=("BLS-Code", "Bezeichnung", "Name (EN)")) -> None:
    cells = Cells()
    header = [(1, "i", ids[0]), (2, "i", ids[1]), (3, "i", ids[2])]
    positions = {}
    for index, (code, text) in enumerate(components):
        base = 4 + 3 * index
        positions[code] = base
        header += [(base, "s", text), (base + 1, "s", f"Datenherkunft {code}"), (base + 2, "s", f"Referenz {code}")]
    rows = [cells.row_xml(1, [(1, "s", "Bundeslebensmittelschlüssel 4.0 (Testdaten)")]), cells.row_xml(2, header)]
    for number, (code, name_de, name_en, values) in enumerate(foods, 3):
        row = [(1, "s", code), (2, "s", name_de)]
        if name_en:
            row.append((3, "s", name_en))
        for bls_code, (kind, value) in values.items():
            if bls_code in positions:
                row.append((positions[bls_code], kind, value))
                if code == "B100000":  # origin and reference cells are present for this food only (sparse rows)
                    row += [(positions[bls_code] + 1, "s", "A"), (positions[bls_code] + 2, "s", "R1")]
        rows.append(cells.row_xml(number, row))
    after = 3 + len(foods)
    rows.append(cells.row_xml(after, [(1, "i", "Quelle: Max Rubner-Institut")]))  # a footer
    rows.append(cells.row_xml(after + 1, [(1, "s", "M11130"), (2, "s", "Code too short")]))
    rows.append(cells.row_xml(after + 2, []))  # an empty row
    notes = [cells.row_xml(1, [(1, "s", "Hinweise zur Nutzung")])]
    write_xlsx(path, {"Hinweise": notes, "Daten": rows}, cells.shared)


def read_csv(path: Path) -> dict[str, dict[str, str]]:
    with path.open(encoding="utf-8", newline="") as stream:
        return {row["bls_code"]: row for row in csv.DictReader(stream)}


class ConverterTests(unittest.TestCase):
    def setUp(self):
        self.directory = Path(self.enterContext(TemporaryDirectory()))
        self.xlsx = self.directory / "bls.xlsx"
        self.out = self.directory / "out"

    def test_registry_agrees_with_the_transform(self):
        self.assertEqual(set(REGISTRY_UNITS), BLS_NUTRIENT_CODES)
        self.assertEqual(set(BLS_COMPONENTS), set(REGISTRY_UNITS))
        self.assertNotIn("carbohydrates_total", REGISTRY_UNITS)
        self.assertEqual(BLS_COMPONENTS["carbohydrates"][0], "CHO")
        units = {code: unit for code, unit in USDA_NUTRIENTS.values()}
        for code, unit in REGISTRY_UNITS.items():
            if code in units:
                self.assertEqual(unit, units[code], code)

    def test_fixture_becomes_csv_and_manifest(self):
        fixture_workbook(self.xlsx)
        manifest = convert(self.xlsx, out_dir=self.out)
        text = (self.out / "bls_4_0.csv").read_text(encoding="utf-8")
        self.assertEqual(text.splitlines()[0], ",".join(["bls_code", "name_de", "name_en", *REGISTRY_UNITS]))
        self.assertEqual(list(read_csv(self.out / "bls_4_0.csv")), ["B100000", "F200000", "G300000"])  # sorted by code
        rows = read_csv(self.out / "bls_4_0.csv")
        blank = {code: "" for code in REGISTRY_UNITS}
        self.assertEqual(rows["B100000"], {
            **blank, "bls_code": "B100000", "name_de": "Brötchen, Weizen", "name_en": "Bread roll, wheat",
            "energy_kcal": "239", "energy_kj": "1000", "protein": "8.5", "fat": "1.2", "carbohydrates": "47.3",
            "fiber": "3.1", "sodium": "480", "selenium": "12.5", "epa": "0.12"})  # 120 mg EPA = 0.12 g
        self.assertEqual(rows["F200000"], {
            **blank, "bls_code": "F200000", "name_de": "Apfel, roh", "name_en": "",  # no English name, a "-" marker
            "energy_kcal": "52", "energy_kj": "218.5", "protein": "0.3", "fat": "0.4", "carbohydrates": "11.4",
            "sodium": "1"})
        self.assertEqual(rows["G300000"]["name_de"], "Möhre, roh")  # composed
        self.assertEqual((rows["G300000"]["fat"], rows["G300000"]["fiber"]), ("0", "2.8"))
        self.assertEqual(json.loads((self.out / "bls_4_0.manifest.json").read_text(encoding="utf-8")), manifest)
        self.assertEqual(manifest["converter_version"], 2)
        self.assertEqual(manifest["markers"], {"below_limit_as_zero": ["<LOD", "<LOD or <LOQ", "<LOQ", "TR"],
                                               "no_value": ["-"]})
        self.assertEqual(manifest["source"], {
            "filename": "bls.xlsx", "sha256": hashlib.sha256(self.xlsx.read_bytes()).hexdigest(), "sheet": "Daten",
            "header_row": 2, "name_columns": [1, 2, 3], "id_headers": ["BLS-Code", "Bezeichnung", "Name (EN)"]})
        self.assertEqual(manifest["csv_sha256"], hashlib.sha256((self.out / "bls_4_0.csv").read_bytes()).hexdigest())
        verify_bls_extract(self.out / "bls_4_0.csv")  # the pair belongs together
        self.assertEqual(manifest["rows"], {"written": 3, "skipped": 2, "skipped_reasons": {"not_a_food_row": 2}})
        # "-" is BLS's no-data marker: counted as a marker, unknown in the CSV, not unparsed.
        fiber, protein = manifest["components"]["fiber"], manifest["components"]["protein"]
        self.assertEqual((fiber["markers"], fiber["unparsed"], fiber["unparsed_samples"]), ({"-": 1}, 0, []))
        self.assertEqual((protein["markers"], protein["unparsed"], protein["unparsed_samples"]), ({}, 0, []))
        energy = manifest["components"]["energy_kj"]
        self.assertEqual((energy["bls_code"], energy["unit"], energy["unit_source"], energy["factor"], energy["column"]),
                         ("ENERCJ", "kJ", "header", "1", "D"))
        epa = manifest["components"]["epa"]
        self.assertEqual((epa["bls_code"], epa["unit"], epa["factor"], epa["non_null"]), ("F20D5N3", "mg", "0.001", 1))
        self.assertEqual({code: manifest["components"][code]["non_null"] for code in ("sodium", "fiber", "selenium", "protein")},
                         {"sodium": 2, "fiber": 2, "selenium": 1, "protein": 3})
        self.assertIn("vitamin_c", manifest["not_found"])
        self.assertNotIn("epa", manifest["not_found"])

    def test_rerun_is_byte_identical(self):
        fixture_workbook(self.xlsx)
        convert(self.xlsx, out_dir=self.out)
        first = [(self.out / name).read_bytes() for name in ("bls_4_0.csv", "bls_4_0.manifest.json")]
        convert(self.xlsx, out_dir=self.out)
        convert(self.xlsx, out_dir=self.directory / "elsewhere")
        for directory in (self.out, self.directory / "elsewhere"):
            self.assertEqual([(directory / name).read_bytes() for name in ("bls_4_0.csv", "bls_4_0.manifest.json")], first)
        self.assertEqual(sorted(path.name for path in self.out.iterdir()), ["bls_4_0.csv", "bls_4_0.manifest.json"])

    def test_missing_required_column_lists_the_headers_and_writes_nothing(self):
        fixture_workbook(self.xlsx, [pair for pair in FIXTURE_COMPONENTS if pair[0] != "FAT"])
        with self.assertRaises(ConvertError) as error:
            convert(self.xlsx, out_dir=self.out)
        message = str(error.exception)
        for expected in ("fat (FAT)", "ENERCJ Energie (Kilojoule) [kJ/100g]", "CHO Kohlenhydrate, verfügbar [g/100g]"):
            self.assertIn(expected, message)
        self.assertFalse(self.out.exists())
        stderr = io.StringIO()
        with redirect_stderr(stderr):
            self.assertEqual(convert_main([str(self.xlsx), "--out-dir", str(self.out)]), 1)
        self.assertIn("bls_convert:", stderr.getvalue())
        self.assertIn("fat (FAT)", stderr.getvalue())
        self.assertFalse(self.out.exists())

    def test_no_header_row_lists_the_rows_seen(self):
        fixture_workbook(self.xlsx, [("SE", "SE Selen [µg/100g]")])  # one code only: no header row
        with self.assertRaises(ConvertError) as error:
            convert(self.xlsx, out_dir=self.out)
        self.assertIn("no header row", str(error.exception))
        self.assertIn("Hinweise zur Nutzung", str(error.exception))
        self.assertIn("SE Selen", str(error.exception))
        self.assertFalse(self.out.exists())

    def test_sheet_selection(self):
        fixture_workbook(self.xlsx)
        self.assertEqual(convert(self.xlsx, sheet="Daten", out_dir=self.out)["source"]["sheet"], "Daten")
        with self.assertRaisesRegex(ConvertError, "no sheet named 'Tabelle'.*Hinweise, Daten"):
            convert(self.xlsx, sheet="Tabelle", out_dir=self.directory / "none")
        with self.assertRaisesRegex(ConvertError, "no header row"):
            convert(self.xlsx, sheet="Hinweise", out_dir=self.directory / "none")

    def test_a_missing_unit_is_an_error_until_the_unit_option_gives_it(self):
        components = [(code, "SE Selen" if code == "SE" else text) for code, text in FIXTURE_COMPONENTS]
        fixture_workbook(self.xlsx, components)
        with self.assertRaisesRegex(ConvertError, r"SE \(selenium, column \w+\) has no unit.*--unit SE="):
            convert(self.xlsx, out_dir=self.out)
        self.assertFalse(self.out.exists())
        manifest = convert(self.xlsx, units={"SE": "ug"}, out_dir=self.out)
        selenium = manifest["components"]["selenium"]
        self.assertEqual((selenium["unit"], selenium["unit_source"], selenium["factor"]), ("ug", "cli", "1"))
        self.assertEqual(read_csv(self.out / "bls_4_0.csv")["B100000"]["selenium"], "12.5")
        # The CLI spelling, with the micro sign and the RYFT code instead of the BLS code.
        other = self.directory / "other"
        with redirect_stdout(io.StringIO()):
            self.assertEqual(convert_main([str(self.xlsx), "--out-dir", str(other), "--unit", "selenium=µg"]), 0)
        self.assertEqual((other / "bls_4_0.csv").read_bytes(), (self.out / "bls_4_0.csv").read_bytes())
        with self.assertRaisesRegex(ConvertError, "cannot be converted"):
            convert(self.xlsx, units={"SE": "kcal"}, out_dir=self.directory / "none")
        with self.assertRaisesRegex(ConvertError, "no mapped component: QQ"):
            convert(self.xlsx, units={"SE": "ug", "QQ": "g"}, out_dir=self.directory / "none")
        with redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            convert_main([str(self.xlsx), "--unit", "SE=stone"])

    def test_two_value_headers_for_one_component_are_ambiguous(self):
        fixture_workbook(self.xlsx, [*FIXTURE_COMPONENTS, ("FAT", "FAT Fett, zweite Spalte [g/100g]")])
        with self.assertRaisesRegex(ConvertError, "FAT .* several header cells"):
            convert(self.xlsx, out_dir=self.out)

    def test_units_are_converted_to_the_registry_unit(self):
        for source, target, expected in (("mg", "g", "0.001"), ("g", "mg", "1000"), ("ug", "mg", "0.001"),
                                         ("mg", "ug", "1000"), ("g", "ug", "1000000"), ("ug", "g", "0.000001"),
                                         ("kcal", "kcal", "1"), ("kJ", "kJ", "1"), ("mg", "mg", "1")):
            with self.subTest(source=source, target=target):
                self.assertEqual(unit_factor(source, target), Decimal(expected))
        for source, target in (("kJ", "kcal"), ("kcal", "g"), ("g", "kJ")):
            with self.assertRaises(ConvertError):
                unit_factor(source, target)
        # Sodium reported in grams becomes milligrams.
        fixture_workbook(self.xlsx, [(code, text.replace("[mg/100g]", "[g/100g]") if code == "NA" else text)
                                     for code, text in FIXTURE_COMPONENTS])
        manifest = convert(self.xlsx, out_dir=self.out)
        self.assertEqual((manifest["components"]["sodium"]["factor"], read_csv(self.out / "bls_4_0.csv")["B100000"]["sodium"]),
                         ("1000", "480000"))

    def test_numbers_decimal_commas_and_markers(self):
        for text, expected in (("12.5", "12.5"), ("12,5", "12.5"), (" 7 ", "7"), ("0", "0"), ("-0", "0"), ("1e2", "100"),
                               (".5", "0.5"), ("1.234,5", None), ("-1", None), ("-", None), ("n.b.", None), ("Sp.", None),
                               ("", None), ("NaN", None), ("1e12", None), ("1,2,3", None)):
            with self.subTest(text=text):
                value = parse_number(text)
                self.assertEqual(None if value is None else format(value.normalize(), "f"), expected)

    def test_unsafe_archives_are_refused(self):
        fixture_workbook(self.xlsx)
        duplicate = self.directory / "duplicate.xlsx"
        with zipfile.ZipFile(self.xlsx) as source, zipfile.ZipFile(duplicate, "w") as target:
            for info in source.infolist():
                target.writestr(info.filename, source.read(info))
            with self.assertWarns(UserWarning):  # zipfile itself warns about the duplicate name
                target.writestr("xl/workbook.xml", source.read("xl/workbook.xml"))
        with self.assertRaisesRegex(ConvertError, "duplicate archive members: xl/workbook.xml"):
            convert(duplicate, out_dir=self.out)
        notbook = self.directory / "notbook.xlsx"
        with zipfile.ZipFile(notbook, "w") as target:
            target.writestr("readme.txt", "no workbook")
        with self.assertRaisesRegex(ConvertError, "not an xlsx workbook"):
            convert(notbook, out_dir=self.out)
        self.assertFalse(self.out.exists())
        broken = self.directory / "broken.xlsx"
        broken.write_bytes(b"not a zip file")
        with redirect_stderr(io.StringIO()):
            self.assertEqual(convert_main([str(broken), "--out-dir", str(self.out)]), 1)

    def test_a_repeated_bls_code_is_an_error(self):
        cells = Cells()
        header = [(1, "i", "BLS-Code"), (2, "i", "Bezeichnung"), (3, "i", "Name (EN)")]
        for index, (_, text) in enumerate(FIXTURE_COMPONENTS[:5]):  # energy, protein, fat and carbohydrates
            header.append((4 + 3 * index, "s", text))
        rows = [cells.row_xml(1, header)]
        for number in (2, 3):
            rows.append(cells.row_xml(number, [(1, "s", "B100000"), (2, "s", "Brötchen"), (4, "n", "1"), (7, "n", "2"),
                                               (10, "n", "3"), (13, "n", "4"), (16, "n", "5")]))
        write_xlsx(self.xlsx, {"Daten": rows}, cells.shared)
        with self.assertRaisesRegex(ConvertError, "B100000 appears twice \\(rows 2 and 3\\)"):
            convert(self.xlsx, out_dir=self.out)
        self.assertFalse(self.out.exists())

    def test_a_required_component_with_cells_that_are_not_numbers_fails(self):
        base = FIXTURE_FOODS[1][3]
        foods = (FIXTURE_FOODS[1], ("F200000", "Apfel, roh", None, {**base, "PROT625": ("i", "k.A."), "FAT": ("i", "n.b.")}),
                 ("F200001", "Birne, roh", None, {**base, "PROT625": ("n", "0.4"), "FAT": ("i", "Sp.")}))
        fixture_workbook(self.xlsx, foods=foods)
        with self.assertRaises(ConvertError) as error:
            convert(self.xlsx, out_dir=self.out)
        self.assertIn("required component protein (PROT625) has 1 cell(s) that are not numbers, e.g. 'k.A.'", str(error.exception))
        self.assertIn("required component fat (FAT) has 2 cell(s) that are not numbers, e.g. 'n.b.', 'Sp.'", str(error.exception))
        self.assertFalse(self.out.exists())

    def test_alphanumeric_codes_and_fatty_acid_headers_of_the_2025_workbook(self):
        # The workbook names EPA "F20:5CN3 ... [g/100g]": the colon belongs to the code, and a code next to an unmapped
        # "F20:0" column is no ambiguity. Codes such as M5B1600 are foods; anything else in column 1 is skipped.
        components = [pair for pair in FIXTURE_COMPONENTS if pair[0] != "F20D5N3"] + [
            ("F20:0", "F20:0 Fettsäure C20:0 (Arachinsäure) [g/100g]"),
            ("F20:5CN3", "F20:5CN3 Fettsäure C20:5 n-3 all-cis (Eicosapentaensäure) [g/100g]"),
            ("F22:6CN3", "F22:6CN3 Fettsäure C22:6 n-3 all-cis (Docosahexaensäure) [g/100g]")]
        base = {key: value for key, value in FIXTURE_FOODS[1][3].items() if key != "F20D5N3"}
        foods = (("M5B1600", "Gorgonzola", None, {**base, "F20:5CN3": ("n", "0.01"), "F22:6CN3": ("n", "0.02")}),
                 ("U57B100", "Schwein Grillbauch, roh", None, base),
                 ("MA51600", "Kein Code", None, base), ("HINWEIS", "Kein Code", None, base))
        fixture_workbook(self.xlsx, components, foods)
        manifest = convert(self.xlsx, out_dir=self.out)
        rows = read_csv(self.out / "bls_4_0.csv")
        self.assertEqual(list(rows), ["M5B1600", "U57B100"])
        self.assertEqual((rows["M5B1600"]["epa"], rows["M5B1600"]["dha"]), ("0.01", "0.02"))
        self.assertEqual((manifest["components"]["epa"]["bls_code"], manifest["components"]["dha"]["bls_code"]),
                         ("F20:5CN3", "F22:6CN3"))
        self.assertEqual(manifest["rows"]["skipped_reasons"], {"not_a_food_row": 4})  # 2 codes, the footer and M11130

    def test_bls_markers_are_zero_below_the_limit_and_unknown_without_data(self):
        base = FIXTURE_FOODS[1][3]
        foods = (FIXTURE_FOODS[1],
                 ("Q550000", "Kokosöl", None, {**base, "PROT625": ("s", "TR"), "FAT": ("n", "100"), "NA": ("s", "<LOQ")}),
                 ("F110400", "Apfel getrocknet", None, {**base, "FAT": ("s", "<LOD"), "FIBT": ("s", "<LOD or <LOQ")}),
                 ("R172000", "Glutamat", None, {**base, "FAT": ("s", "-"), "NA": ("i", " - ")}),
                 # Only the exact spelling is a marker: anything else in a required component still stops the run.
                 ("F200001", "Birne, roh", None, {**base, "SE": ("s", "tr")}))
        fixture_workbook(self.xlsx, foods=foods)
        manifest = convert(self.xlsx, out_dir=self.out)
        rows = read_csv(self.out / "bls_4_0.csv")
        self.assertEqual((rows["Q550000"]["protein"], rows["Q550000"]["sodium"]), ("0", "0"))
        self.assertEqual((rows["F110400"]["fat"], rows["F110400"]["fiber"]), ("0", "0"))
        self.assertEqual((rows["R172000"]["fat"], rows["R172000"]["sodium"]), ("", ""))
        self.assertEqual(rows["F200001"]["selenium"], "")
        components = manifest["components"]
        self.assertEqual(components["fat"]["markers"], {"-": 1, "<LOD": 1})
        self.assertEqual(components["sodium"]["markers"], {"-": 1, "<LOQ": 1})
        self.assertEqual((components["protein"]["markers"], components["protein"]["non_null"]), ({"TR": 1}, 5))
        self.assertEqual((components["fat"]["non_null"], components["fat"]["unparsed"]), (4, 0))
        self.assertEqual((components["selenium"]["unparsed"], components["selenium"]["unparsed_samples"]), (1, ["tr"]))
        fixture_workbook(self.xlsx, foods=(FIXTURE_FOODS[1], ("Q550000", "Kokosöl", None, {**base, "PROT625": ("s", "Tr")})))
        with self.assertRaisesRegex(ConvertError, "required component protein \\(PROT625\\) has 1 cell.*'Tr'"):
            convert(self.xlsx, out_dir=self.directory / "none")

    def test_german_thousands_separators_in_text_cells_are_ambiguous(self):
        base = {"ENERCJ": ("n", "100"), "ENERCC": ("n", "24"), "PROT625": ("n", "1"), "FAT": ("n", "1"), "CHO": ("n", "4")}
        comma = ("B100000", "Brot", None, {**base, "FAT": ("i", "0,5")})
        dotted = ("B100001", "Brot zwei", None, {**base, "FIBT": ("i", "1.234")})
        with self.assertRaisesRegex(ConvertError, "thousands separators.*fiber='1.234'"):
            fixture_workbook(self.xlsx, foods=(comma, dotted))
            convert(self.xlsx, out_dir=self.out)
        self.assertFalse(self.out.exists())
        # "1.234,5" can only be a German number, with or without other decimal commas.
        fixture_workbook(self.xlsx, foods=(("B100001", "Brot zwei", None, {**base, "FIBT": ("i", "1.234,5")}),))
        with self.assertRaisesRegex(ConvertError, "thousands separators.*fiber='1.234,5'"):
            convert(self.xlsx, out_dir=self.out)
        # No decimal comma anywhere: "1.234" is a number with a decimal point.
        fixture_workbook(self.xlsx, foods=(dotted,))
        convert(self.xlsx, out_dir=self.out)
        self.assertEqual(read_csv(self.out / "bls_4_0.csv")["B100001"]["fiber"], "1.234")
        # A real number cell is never a text: 0.125 next to a decimal comma is fine.
        number = ("B100001", "Brot zwei", None, {**base, "FIBT": ("n", "0.125")})
        fixture_workbook(self.xlsx, foods=(comma, number))
        convert(self.xlsx, out_dir=self.directory / "numbers")
        self.assertEqual(read_csv(self.directory / "numbers" / "bls_4_0.csv")["B100001"]["fiber"], "0.125")

    def test_columns_1_to_3_are_checked_against_their_headers(self):
        fixture_workbook(self.xlsx, ids=("Foo", "Bar", "Baz"))
        with self.assertRaises(ConvertError) as error:
            convert(self.xlsx, out_dir=self.out)
        for expected in ("A='Foo'", "B='Bar'", "C='Baz'", "--name-columns"):
            self.assertIn(expected, str(error.exception))
        self.assertFalse(self.out.exists())
        for ids in (("BLS-Code", "Name englisch", "Bezeichnung"), ("Bezeichnung", "BLS-Code", "Name (EN)")):
            fixture_workbook(self.xlsx, ids=ids)
            with self.subTest(ids=ids), self.assertRaisesRegex(ConvertError, "columns 1-3"):
                convert(self.xlsx, out_dir=self.out)
        for ids in (("SBLS", "Lebensmittelbezeichnung", "Lebensmittel englisch"), ("Schlüssel", "Name deutsch", "Food name"),
                    ("BLS Code", "Name", "Name EN")):
            fixture_workbook(self.xlsx, ids=ids)
            with self.subTest(ids=ids):
                self.assertEqual(convert(self.xlsx, out_dir=self.out)["source"]["id_headers"], list(ids))
        # --name-columns skips the check and chooses the columns.
        fixture_workbook(self.xlsx, ids=("Foo", "Bar", "Baz"))
        manifest = convert(self.xlsx, out_dir=self.out, name_columns=(1, 2, 3))
        self.assertEqual((manifest["source"]["name_columns"], manifest["source"]["id_headers"]), ([1, 2, 3], ["Foo", "Bar", "Baz"]))
        swapped = convert(self.xlsx, out_dir=self.directory / "swapped", name_columns=(1, 3, 2))
        rows = read_csv(self.directory / "swapped" / "bls_4_0.csv")
        self.assertEqual(rows["B100000"]["name_de"], "Bread roll, wheat")  # column 3 is now the German name
        self.assertEqual(swapped["rows"]["skipped_reasons"], {"not_a_food_row": 2, "empty_german_name": 1})
        with redirect_stdout(io.StringIO()):
            self.assertEqual(convert_main([str(self.xlsx), "--out-dir", str(self.directory / "cli"), "--name-columns", "1,2,3"]), 0)
        with redirect_stderr(io.StringIO()):
            self.assertEqual(convert_main([str(self.xlsx), "--out-dir", str(self.directory / "none")]), 1)
            for bad in ("1,1,2", "1,2", "a,b,c", "0,1,2"):
                with self.subTest(argument=bad), self.assertRaises(SystemExit):
                    convert_main([str(self.xlsx), "--name-columns", bad])

    def test_old_python_is_refused(self):
        stderr = io.StringIO()
        with patch.object(sys, "version_info", (3, 10, 0)), redirect_stderr(stderr):
            self.assertEqual(convert_main([str(self.xlsx)]), 1)
        self.assertIn("Python 3.11 or newer is required", stderr.getvalue())


WRAPPER = """
import json, resource, subprocess, sys

def limit():
    if sys.platform.startswith("linux"):
        resource.setrlimit(resource.RLIMIT_AS, (1 << 30, 1 << 30))

done = subprocess.run([sys.executable, "-I", *sys.argv[1:]], preexec_fn=limit, capture_output=True, text=True)
print(json.dumps({"code": done.returncode, "stderr": done.stderr[-2000:],
                  "maxrss": resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss}))
"""


class ConverterMemoryTests(unittest.TestCase):
    FOODS, COLUMNS = 7_140, 418

    def synthetic_rows(self, cells: Cells):
        """A sheet of the documented size: 7,140 foods, 418 columns (3 + 138 components x 3 + one note column)."""
        components = 138
        letters = [column_letters(column) for column in range(1, self.COLUMNS + 1)]
        codes = [code for code, _ in FIXTURE_COMPONENTS[:-1]]
        header = [(1, "i", "BLS-Code"), (2, "i", "Bezeichnung"), (3, "i", "Name (EN)")]
        for index in range(components):
            code = codes[index] if index < len(codes) else f"XC{index:03d}"
            text = dict(FIXTURE_COMPONENTS).get(code, f"{code} Filler [g/100g]")
            base = 4 + 3 * index
            header += [(base, "s", text), (base + 1, "s", f"Datenherkunft {code}"), (base + 2, "s", f"Referenz {code}")]
        header.append((self.COLUMNS, "s", "Anmerkung"))
        yield cells.row_xml(1, header)
        origin, reference, note = cells.index("A"), cells.index("R1"), cells.index("-")
        for food in range(self.FOODS):
            number = food + 2
            parts = [f'<row r="{number}">',
                     f'<c r="A{number}" t="s"><v>{cells.index(f"B{100000 + food}")}</v></c>',
                     f'<c r="B{number}" t="s"><v>{cells.index(f"Lebensmittel {food}")}</v></c>',
                     f'<c r="C{number}" t="s"><v>{cells.index(f"Food {food}")}</v></c>']
            for component in range(components):
                base = 4 + 3 * component
                value = f"{(food * 7 + component * 3) % 997 / 10:.1f}"
                parts.append(f'<c r="{letters[base - 1]}{number}"><v>{value}</v></c>'
                             f'<c r="{letters[base]}{number}" t="s"><v>{origin}</v></c>'
                             f'<c r="{letters[base + 1]}{number}" t="s"><v>{reference}</v></c>')
            parts.append(f'<c r="{letters[self.COLUMNS - 1]}{number}" t="s"><v>{note}</v></c></row>')
            yield "".join(parts)

    def test_peak_memory_stays_far_below_the_sheet_size(self):
        with TemporaryDirectory() as workspace:
            xlsx, out = Path(workspace) / "bls-full.xlsx", Path(workspace) / "out"
            cells = Cells()
            write_xlsx(xlsx, {"Daten": self.synthetic_rows(cells)}, cells.shared)
            done = subprocess.run(
                [sys.executable, "-c", WRAPPER, str(ETL_DIR / "bls_convert.py"), str(xlsx), "--out-dir", str(out)],
                check=True, capture_output=True, text=True)
            result = json.loads(done.stdout)
            self.assertEqual((result["code"], result["stderr"]), (0, ""))
            peak = result["maxrss"] if sys.platform == "darwin" else result["maxrss"] * 1024  # bytes on macOS, KiB on Linux
            self.assertLess(peak, 512 * MEBIBYTE)
            rows = read_csv(out / "bls_4_0.csv")
            self.assertEqual(len(rows), self.FOODS)
            self.assertEqual(rows["B100001"]["energy_kj"], "0.7")  # (1 * 7 + 0) % 997 / 10
            manifest = json.loads((out / "bls_4_0.manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(manifest["rows"]["written"], self.FOODS)
            self.assertEqual(manifest["components"]["epa"]["unit"], "mg")


def csv_row(code, name_de, name_en="", **nutrients):
    return {"bls_code": code, "name_de": name_de, "name_en": name_en,
            **{"energy_kcal": "100", "protein": "5", "fat": "2", "carbohydrates": "15"}, **nutrients}


def write_manifest(path: Path) -> None:
    """The part of the manifest the import checks: the digest of the CSV it describes."""
    path.with_suffix(".manifest.json").write_text(
        json.dumps({"csv_sha256": hashlib.sha256(path.read_bytes()).hexdigest()}), encoding="utf-8")


def write_csv(path: Path, rows, manifest=True) -> None:
    with path.open("w", encoding="utf-8", newline="") as stream:
        writer = csv.DictWriter(stream, fieldnames=["bls_code", "name_de", "name_en", *REGISTRY_UNITS],
                                restval="", lineterminator="\n")
        writer.writeheader()
        writer.writerows(rows)
    if manifest:
        write_manifest(path)


class FakeConnection:
    """What BlsNotes.stale_twins needs from a connection: execute(sql, params).fetchall()."""

    def __init__(self, existing):
        self.existing, self.statements = existing, []

    def execute(self, sql, params):
        self.statements.append((sql, params))
        return self

    def fetchall(self):
        return [(code,) for code in self.existing]


def curation_with_twins(twins):
    data = json.loads(BLS_CURATION_PATH.read_text(encoding="utf-8"))
    return parse_curation({**data, "twins": twins})


CURATION = load_curation()
KAROTTE_TWIN = {"G300000": {"staple": "usda:170393", "name_de": "Möhre, roh"}}


class TransformTests(unittest.TestCase):
    def food(self, code, name_de, name_en="", curation=CURATION, notes=None, **nutrients):
        return bls_row_food(csv_row(code, name_de, name_en, **nutrients), "v1", curation, notes)

    def test_a_generic_food(self):
        self.assertEqual(BLS_ATTRIBUTION, "Max Rubner-Institut (2025): Bundeslebensmittelschlüssel (BLS), Version 4.0 "
                                          "— Deutsche Nährstoffdatenbank. Karlsruhe. DOI: 10.25826/Data20251217-134202-0")
        notes = BlsNotes()
        food = self.food("G300000", "Möhre, roh", "Carrot, raw", notes=notes, fiber="2.8")
        self.assertEqual((food.provider, food.external_id, food.name, food.reference_unit, food.markets),
                         ("bls", "G300000", "Möhre, roh", "g", ["DE"]))
        self.assertEqual(food.names, [("de", "Möhre, roh"), ("en", "Carrot, raw")])
        self.assertEqual((food.portions, food.canonical_group_key, food.is_canonical_staple, food.food_form,
                          food.subtitles, food.brand, food.gtin, food.density_g_ml),
                         ((), None, False, "raw", {}, "", None, None))
        self.assertEqual(food.attribution, BLS_ATTRIBUTION)
        self.assertEqual({nutrient.method for nutrient in food.nutrients}, {"bls:4.0"})
        values = {nutrient.code: nutrient.amount for nutrient in food.nutrients}
        self.assertEqual(values, {"energy_kcal": 100, "protein": 5, "fat": 2, "carbohydrates": 15, "fiber": 2.8})
        self.assertEqual(food.category, classify("Möhre, roh", ["Gemüse und Gemüseerzeugnisse"], provider="bls"))
        self.assertEqual((food.category.name, food.category.profile), ("non_starchy_veg", {}))
        self.assertEqual(food.aliases, (("de", "Karotte, roh"), ("de", "Mohrrübe, roh"), ("de", "Rüebli, roh")))
        self.assertEqual(notes.aliases_added, 3)
        self.assertEqual(notes.twin_candidates, {"G300000": {"name_de": "Möhre, roh", "staples": ["usda:170393"]}})
        self.assertEqual(food.checksum, self.food("G300000", "Möhre, roh", "Carrot, raw", fiber="2.8").checksum)

    def test_without_an_english_name_and_for_unknown_groups(self):
        food = self.food("F200000", "Apfel, roh")
        self.assertEqual(food.names, [("de", "Apfel, roh")])
        self.assertEqual(food.category, classify("Apfel, roh", ["Obst und Obsterzeugnisse"], provider="bls"))
        unknown = self.food("Z999999", "Apfel, roh")  # no main group for Z: no hint
        self.assertEqual(unknown.category, classify("Apfel, roh", [], provider="bls"))

    def test_twin_candidates_ignore_state_words_spaces_and_commas(self):
        # BLS 4.0 writes the state after a space ("Banane roh") or a comma, and splits some compounds ("Hafer Flocken").
        # Codes outside the committed twins, so the rows are proposed, not skipped.
        for code, name, staples in (("F900001", "Banane roh", ["usda:173944"]), ("C900001", "Hafer Flocken", ["usda:173904"]),
                                    ("E900001", "Hühnerei gekocht", ["usda:171287"]),
                                    ("X900001", "Kartoffeln gekocht, mit Butter", ["usda:170440"]),
                                    ("F900002", "Apfel roh", ["usda:171688"]), ("G300000", "Möhre, roh", ["usda:170393"])):
            with self.subTest(name=name):
                notes = BlsNotes()
                self.food(code, name, notes=notes)
                self.assertEqual(notes.twin_candidates, {code: {"name_de": name, "staples": staples}})
        notes = BlsNotes()
        self.food("D130100", "Apfelkuchen (Sandmasse)", notes=notes)
        self.assertEqual(notes.twin_candidates, {})

    def test_the_staples_only_path_never_writes_over_a_full_import(self):
        class Rows:
            def __init__(self, row):
                self.row, self.calls = row, []

            def execute(self, sql, params):
                self.calls.append((sql, params))
                return self

            def fetchone(self):
                return self.row

        refuse_staples_over_full_bls(None, "py-etl4:bls_4_0:staples2")  # a dry run has nothing to protect
        empty = Rows(None)
        refuse_staples_over_full_bls(empty, "py-etl4:bls_4_0:staples2")
        self.assertEqual(empty.calls[0][1], ("py-etl4:bls_4_0:staples2:",))
        with self.assertRaisesRegex(ValueError, "holds the full BLS import"):
            refuse_staples_over_full_bls(Rows((1,)), "py-etl4:bls_4_0:staples2")

    def test_bls_codes_may_carry_capital_letters_in_positions_3_and_4(self):
        for code in ("M5B1600", "U57B100", "P2A3000", "M111300"):
            with self.subTest(code=code):
                self.assertEqual(self.food(code, "Rotwein trocken").external_id, code)
        for code in ("m5b1600", "MA51600", "M5B160", "M5B16000", "HINWEIS", "M51600B"):
            with self.subTest(code=code), self.assertRaises(Reject):
                self.food(code, "Rotwein trocken")

    def test_rows_that_fail_the_quality_rules_are_rejected(self):
        for row, reason in ((csv_row("X1", "Apfel"), "invalid_bls_code"),
                            (csv_row("F200000", "Apfel", carbohydrates=""), "missing_or_invalid_macro"),
                            (csv_row("F200000", "A"), "invalid_name"),
                            (csv_row("F200000", "Öl", energy_kcal="990", fat="99", carbohydrates="0", protein="0"),
                             "energy_exceeds_950_kcal"),
                            (csv_row("F200000", "Apfel", energy_kcal="300"), "atwater_deviation")):
            with self.subTest(reason=reason), self.assertRaises(Reject) as error:
                bls_row_food(row, "v1", CURATION)
            self.assertEqual(str(error.exception), reason)

    def test_energy_from_kilojoules_and_alcohol(self):
        food = self.food("F200000", "Apfel, roh", energy_kcal="", energy_kj="418.4")
        energy = {nutrient.code: nutrient for nutrient in food.nutrients}
        self.assertEqual((energy["energy_kcal"].amount, energy["energy_kcal"].method, energy["energy_kj"].method),
                         (100, "bls:kcal_from_kj", "bls:4.0"))
        wine = dict(energy_kcal="85", protein="0.1", fat="0", carbohydrates="2.6")
        with self.assertRaises(Reject):
            self.food("P100000", "Wein, rot", **wine)
        self.assertEqual(self.food("P100000", "Wein, rot", alcohol="9.5", **wine).warnings, ("atwater_alcohol_exception",))

    def test_food_forms_come_from_whole_german_words(self):
        for name, form in (("Möhre, roh", "raw"), ("Kartoffeln, gekocht", "boiled"), ("Hähnchen, gegart", "cooked"),
                           ("Kartoffeln, gebraten", "fried"), ("Brot, gebacken", "baked"), ("Apfel, getrocknet", "dried"),
                           ("Erbsen, tiefgefroren", "frozen"), ("Spinat, tiefgekühlt", "frozen"), ("Roh, Apfel", "raw"),
                           ("Fisch, gegart, tiefgekühlt", "cooked"), ("Rohmilch", None), ("Rohkost, gemischt", None),
                           ("Apfel", None), ("Gekochter Schinken", None)):
            with self.subTest(name=name):
                self.assertEqual(self.food("G300000", name).food_form, form)

    def test_synonym_aliases(self):
        for name, aliases in (
                ("Möhre, roh", ["Karotte, roh", "Mohrrübe, roh", "Rüebli, roh"]),
                ("Paprika, orange", []),  # the adjective is lower case; the synonym is the noun
                ("Lauchzwiebel, roh", []),  # not a whole word
                ("Sauerrahm, 10 % Fett", []),
                ("möhre, roh", []),  # case-sensitive
                ("Kartoffeln, gekocht", ["Erdäpfel, gekocht"]),  # Kartoffel is not Kartoffeln
                ("Speisequark, mager", ["Quark, mager", "Topfen, mager"]),
                ("Orange, roh", ["Apfelsine, roh"]),
                ("Rote Bete, gegart", ["Rote Rübe, gegart", "Rote Beete, gegart"]),
                ("Hackfleisch, Rind", ["Gehacktes, Rind", "Faschiertes, Rind"]),
                ("Pfifferlinge, roh", ["Eierschwammerl, roh"]),  # Pfifferling is another word
                ("Joghurt, mild", ["Jogurt, mild"])):
            with self.subTest(name=name):
                food = self.food("G300001", name)
                self.assertEqual([label for lang, label in food.aliases if lang == "de"], aliases)
                self.assertEqual(len(food.aliases), len(aliases))

    def test_aliases_are_capped_deduplicated_and_deterministic(self):
        name = "Möhre, Hähnchen, Tomate, Pute, Orange, Hefe"
        first, second = self.food("G300001", name).aliases, self.food("G300001", name).aliases
        self.assertEqual(first, second)
        self.assertEqual(len(first), 6)
        self.assertEqual(first[0], ("de", "Karotte, Hähnchen, Tomate, Pute, Orange, Hefe"))
        self.assertEqual(first[3], ("de", "Möhre, Hähnchen, Paradeiser, Pute, Orange, Hefe"))
        self.assertEqual(first[5], ("de", "Möhre, Hendl, Tomate, Pute, Orange, Hefe"))
        self.assertEqual(len({label for _, label in first}), 6)
        # An alias that is already a name of the food (here its English name) is dropped.
        self.assertEqual(self.food("G300001", "Pute, roh", "Truthahn, roh").aliases, ())
        self.assertEqual(self.food("G300001", "Pute, roh", "Turkey, raw").aliases, (("de", "Truthahn, roh"),))

    def test_twins_are_skipped_when_their_name_matches_and_reported_when_it_does_not(self):
        curation = curation_with_twins(KAROTTE_TWIN)
        notes = BlsNotes()
        with self.assertRaises(Skip):
            self.food("G300000", "Möhre, Roh", "Carrot, raw", curation, notes)
        self.assertEqual((notes.skipped_twin, notes.twin_mismatch, notes.aliases_added), (1, {}, 0))
        food = self.food("G300000", "Möhre, gegart", "Carrot, cooked", curation, notes)
        self.assertEqual(food.name, "Möhre, gegart")
        self.assertEqual(notes.skipped_twin, 1)
        self.assertEqual(notes.twin_mismatch, {"G300000": {"expected": "Möhre, roh", "found": "Möhre, gegart"}})
        self.assertEqual(self.food("G300001", "Möhre, roh", curation=curation).name, "Möhre, roh")  # another code

    def test_a_curated_staple_keeps_its_overlay_and_takes_the_nutrients_from_the_row(self):
        curated = STAPLES["bls"]["M111300"]["nutrients"]
        values = {code: str(amount) for code, amount in curated.items()}
        values.update(energy_kcal="63", vitamin_b12="0.4")
        notes = BlsNotes()
        food = self.food("M111300", "Milch, Kuhmilch, 3,5 % Fett", "Cow milk", notes=notes, **values)
        reference = bls_food({"external_id": "M111300"}, "v1")
        for attribute in ("name", "names", "portions", "aliases", "subtitles", "food_form", "canonical_group_key",
                          "is_canonical_staple", "density_g_ml", "attribution", "markets", "category", "brand"):
            self.assertEqual(getattr(food, attribute), getattr(reference, attribute), attribute)
        self.assertEqual(food.names, [("de", "Vollmilch 3,5 %"), ("en", "Whole milk 3.5%")])
        self.assertEqual(food.aliases, staple_aliases(STAPLES["bls"]["M111300"]))  # no synonym aliases for staples
        amounts = {nutrient.code: nutrient.amount for nutrient in food.nutrients}
        self.assertEqual((amounts["energy_kcal"], amounts["vitamin_b12"], amounts["calcium"]), (63, 0.4, 117))
        self.assertEqual(notes.staple_nutrient_drift, {"M111300": {"energy_kcal": [62, 63.0]}})
        self.assertEqual((notes.staples_seen, notes.aliases_added, notes.twin_candidates), ({"M111300"}, 0, {}))
        # A curated nutrient the row does not carry is drift too.
        del values["fiber"]
        notes = BlsNotes()
        self.food("M111300", "Milch", notes=notes, **values)
        self.assertEqual(notes.staple_nutrient_drift["M111300"]["fiber"], [0, None])
        # Within 1 % is no drift.
        values.update(energy_kcal="62.5", fiber="0")
        notes = BlsNotes()
        self.food("M111300", "Milch", notes=notes, **values)
        self.assertEqual(notes.staple_nutrient_drift, {})

    def test_the_staples_only_path_is_unchanged(self):
        food = bls_food({"external_id": "M713100"}, "v1")
        self.assertEqual((food.name, [portion.weight_g for portion in food.portions]), ("Low-fat quark", [25, 150, 250]))
        self.assertEqual({nutrient.method for nutrient in food.nutrients}, {"bls:4.0"})
        self.assertEqual(len(food.nutrients), len(STAPLES["bls"]["M713100"]["nutrients"]))

    def test_the_dry_run_summary(self):
        notes = BlsNotes()
        self.assertEqual(notes.summary(), {
            "bls_full": True, "aliases_added": 0, "skipped_twin": 0, "twin_mismatch": {}, "twin_candidates": {},
            "stale_twins": {}, "staple_nutrient_drift": {}, "staples_missing": sorted(STAPLES["bls"])})

    def test_stale_twins_are_reported_without_writing(self):
        curation = curation_with_twins(KAROTTE_TWIN)
        notes = BlsNotes()
        quiet = FakeConnection(["G300000"])
        self.assertEqual(notes.summary(quiet)["stale_twins"], {})
        self.assertEqual(quiet.statements, [])  # no twin in the CSV: nothing to look up
        with self.assertRaises(Skip):
            self.food("G300000", "Möhre, roh", "Carrot, raw", curation, notes)
        self.food("G300000", "Möhre, gegart", curation=curation, notes=notes)  # a mismatch is a twin in the CSV too
        self.assertEqual(notes.summary()["stale_twins"], {"G300000": {"imported": None}})  # no connection
        for existing, imported in ((["G300000"], True), ([], False)):
            conn = FakeConnection(existing)
            self.assertEqual(notes.summary(conn)["stale_twins"], {"G300000": {"imported": imported}})
            [(sql, params)] = conn.statements
            self.assertTrue(sql.startswith("SELECT"))  # read-only
            self.assertEqual(params, (["G300000"],))

    def test_the_curation_is_validated(self):
        base = json.loads(BLS_CURATION_PATH.read_text(encoding="utf-8"))
        self.assertEqual(set(base), {"version", "synonyms", "twins"})
        for name, data in {
            "version": {**base, "version": 2},
            "extra key": {**base, "more": []},
            "one member": {**base, "synonyms": [["Möhre"]]},
            "empty member": {**base, "synonyms": [["Möhre", ""]]},
            "padded member": {**base, "synonyms": [["Möhre", "Karotte "]]},
            "repeated member": {**base, "synonyms": [["Möhre", "Möhre"]]},
            "not a list": {**base, "synonyms": {"Möhre": "Karotte"}},
            "twin on a bad code": {**base, "twins": {"x": {"staple": "usda:170393", "name_de": "Möhre, roh"}}},
            "twin on a curated staple": {**base, "twins": {"M111300": {"staple": "usda:170393", "name_de": "Milch"}}},
            "twin of an unknown staple": {**base, "twins": {"G300000": {"staple": "usda:1", "name_de": "Möhre, roh"}}},
            "twin of a bls staple": {**base, "twins": {"G300000": {"staple": "bls:M111300", "name_de": "Möhre, roh"}}},
            "twin without a name": {**base, "twins": {"G300000": {"staple": "usda:170393"}}},
            "not an object": [],
        }.items():
            with self.subTest(case=name), self.assertRaisesRegex(ValueError, "bls_curation.v1.json"):
                parse_curation(data)
        self.assertIsInstance(parse_curation(base).twins, dict)
        self.assertEqual(load_curation().groups[0], ("Möhre", "Karotte", "Mohrrübe", "Rüebli"))

    def test_csv_reader_checks_columns_and_applies_the_limit(self):
        with TemporaryDirectory() as workspace:
            path = Path(workspace) / "bls.csv"
            write_csv(path, [csv_row(f"B10000{n}", f"Brot {n}") for n in range(5)])
            batches = list(bls_csv_rows(path, 2))
            self.assertEqual([len(batch) for batch in batches], [2, 2, 1])
            self.assertEqual(batches[0][0]["bls_code"], "B100000")
            self.assertEqual([len(batch) for batch in bls_csv_rows(path, 2, 3)], [2, 1])
            head = "bls_code,name_de,name_en,fat\n"
            for text in ("bls_code,name_de,name_en,bogus\nB100000,Brot,,1\n", "bls_code,name_en,name_de\n",
                         head + "B100000,Brot,\n", head + "B100000,Brot,,1,2\n", "bls_code,name_de,name_en,fat,fat\n", "",
                         head + "X1,Brot,,1\n", head + "B100000,Brot,,1\nB100000,Brot zwei,,2\n",
                         head + "B100000,Brot,,-1\n", head + "B100000,Brot,,nan\n", head + "B100000,Brot,,1,5\n"):
                path.write_text(text, encoding="utf-8")
                with self.subTest(text=text), self.assertRaises(ValueError):
                    list(bls_csv_rows(path, 10))
            # The whole file is checked when the function is called, before any batch exists: the last row is bad.
            path.write_text(head + "B100000,Brot,,1\nB100001,Brot zwei,,1\nB100002,Brot drei,,viel\n", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "line 4: fat of B100002 is not a number"):
                bls_csv_rows(path, 1)
            path.write_text(head + "B100000,Brot,,1\nB100001,Brot zwei,,\n", encoding="utf-8")  # empty is unknown, fine
            self.assertEqual([len(batch) for batch in bls_csv_rows(path, 1)], [1, 1])

    def test_the_extract_must_match_its_manifest(self):
        with TemporaryDirectory() as workspace:
            path = Path(workspace) / "bls.csv"
            write_csv(path, [csv_row("B100000", "Brot")], manifest=False)
            with self.assertRaisesRegex(ValueError, "bls.manifest.json is missing"):
                verify_bls_extract(path)
            write_manifest(path)
            verify_bls_extract(path)
            path.write_text(path.read_text(encoding="utf-8") + "\n", encoding="utf-8")  # the CSV changed, the manifest did not
            with self.assertRaisesRegex(ValueError, "does not match the csv_sha256"):
                verify_bls_extract(path)
            for text in ("{not json", "[]", "{}", '{"csv_sha256": 5}'):
                path.with_suffix(".manifest.json").write_text(text, encoding="utf-8")
                with self.subTest(manifest=text), self.assertRaises(ValueError):
                    verify_bls_extract(path)

    def test_the_dataset_version_covers_every_input(self):
        self.assertEqual(transform.BLS_TRANSFORM_VERSION, "bls1")
        with TemporaryDirectory() as workspace:
            path = Path(workspace) / "bls.csv"
            write_csv(path, [csv_row("B100000", "Brot")])
            digest = bls_input_digest(path)
            expected = hashlib.sha256()
            for part in (path.read_bytes(), BLS_CURATION_PATH.read_bytes(), STAPLES_PATH.read_bytes(), b"bls1"):
                expected.update(hashlib.sha256(part).digest())
            self.assertEqual(digest, expected.hexdigest())
            with patch.object(transform, "BLS_TRANSFORM_VERSION", "bls2"):
                self.assertNotEqual(bls_input_digest(path), digest)
            write_csv(path, [csv_row("B100000", "Brot zwei")])
            self.assertNotEqual(bls_input_digest(path), digest)


class CliTests(unittest.TestCase):
    def run_etl(self, *arguments, check=True):
        return subprocess.run([sys.executable, "run_etl.py", "--source", "bls", *arguments], cwd=ETL_DIR, check=check,
                              capture_output=True, text=True)

    def test_without_the_extract_only_the_curated_staples_import(self):
        [line] = self.run_etl("--no-bls-csv").stdout.splitlines()
        result = json.loads(line)
        self.assertEqual((result["release"], result["mode"], result["version"], result["bls_full"]),
                         ("bls", "dry-run", "py-etl4:bls_4_0:staples2", False))
        self.assertEqual((result["counts"]["accepted"], result["counts"]["rejected"]), (3, 0))
        for name in ("aliases_added", "skipped_twin", "twin_mismatch", "twin_candidates", "staple_nutrient_drift"):
            self.assertNotIn(name, result)
        if not BLS_CSV_PATH.exists():  # the default, until the extract is committed
            self.assertEqual(json.loads(self.run_etl().stdout), result)
        self.assertEqual(json.loads(self.run_etl("--no-bls-csv", "--limit", "1").stdout)["counts"]["accepted"], 1)

    def test_the_extract_is_imported_in_full(self):
        with TemporaryDirectory() as workspace:
            path = Path(workspace) / "bls.csv"
            staple = {code: str(amount) for code, amount in STAPLES["bls"]["M111300"]["nutrients"].items()}
            staple.update(energy_kcal="63")
            write_csv(path, [
                csv_row("B100000", "Brötchen, Weizen", "Bread roll, wheat"),
                csv_row("G300000", "Möhre, roh", "Carrot, raw"),
                csv_row("G300001", "Rübe, roh", carbohydrates=""),
                csv_row("M111300", "Milch, Kuhmilch, 3,5 % Fett", "Cow milk", **staple),
            ])
            [line] = self.run_etl("--bls-csv", str(path)).stdout.splitlines()
            result = json.loads(line)
            self.assertEqual(result["version"], f"py-etl4:bls_4_0:staples2:{bls_input_digest(path)}")
            self.assertEqual(result["stale_twins"], {})  # none of the committed twins is in this CSV
            self.assertEqual((result["release"], result["mode"], result["bls_full"]), ("bls", "dry-run", True))
            self.assertEqual({key: result["counts"][key] for key in ("processed", "accepted", "rejected")},
                             {"processed": 4, "accepted": 3, "rejected": 1})
            self.assertEqual(result["reject_reasons"], {"missing_or_invalid_macro": 1})
            self.assertEqual(result["aliases_added"], 6)  # three for Brötchen, three for Möhre
            self.assertEqual((result["skipped_twin"], result["twin_mismatch"]), (0, {}))
            self.assertEqual(result["twin_candidates"],
                             {"G300000": {"name_de": "Möhre, roh", "staples": ["usda:170393"]}})
            self.assertEqual(result["staple_nutrient_drift"], {"M111300": {"energy_kcal": [62, 63.0]}})
            self.assertEqual(result["staples_missing"], ["M111200", "M713100"])
            self.assertEqual(sum(result["categories"].values()), 3)
            limited = json.loads(self.run_etl("--bls-csv", str(path), "--limit", "2").stdout)
            self.assertEqual((limited["counts"]["processed"], limited["counts"]["accepted"]), (2, 2))
            self.assertEqual(json.loads(self.run_etl("--bls-csv", str(path)).stdout)["version"], result["version"])

    def test_a_broken_extract_stops_the_run(self):
        with TemporaryDirectory() as workspace:
            path = Path(workspace) / "bls.csv"
            path.write_text("bls_code,name_de,name_en,bogus\nB100000,Brot,,1\n", encoding="utf-8")
            write_manifest(path)
            done = self.run_etl("--bls-csv", str(path), check=False)
            self.assertEqual((done.returncode, done.stdout), (1, ""))
            self.assertIn("ETL failed", done.stderr)
            self.assertIn("bogus", done.stderr)
            done = self.run_etl("--bls-csv", str(Path(workspace) / "missing.csv"), check=False)
            self.assertEqual(done.returncode, 1)
            both = self.run_etl("--bls-csv", str(path), "--no-bls-csv", check=False)
            self.assertEqual(both.returncode, 2)

    def test_a_torn_pair_or_a_bad_row_anywhere_stops_the_run_before_any_output(self):
        with TemporaryDirectory() as workspace:
            path = Path(workspace) / "bls.csv"
            write_csv(path, [csv_row("B100000", "Brot"), csv_row("B100001", "Brot zwei")], manifest=False)
            done = self.run_etl("--bls-csv", str(path), check=False)  # no manifest
            self.assertEqual((done.returncode, done.stdout), (1, ""))
            self.assertIn("manifest.json is missing", done.stderr)
            write_manifest(path)
            self.assertEqual(self.run_etl("--bls-csv", str(path), check=False).returncode, 0)
            path.write_text(path.read_text(encoding="utf-8").replace("Brot zwei", "Brot drei"), encoding="utf-8")  # by hand
            done = self.run_etl("--bls-csv", str(path), check=False)
            self.assertEqual((done.returncode, done.stdout), (1, ""))
            self.assertIn("does not match the csv_sha256", done.stderr)
            # The digest fits, but the last row's fat is not a number: nothing is processed, not even the first row.
            write_csv(path, [csv_row("B100000", "Brot"), csv_row("B100001", "Brot zwei", fat="x")])
            done = self.run_etl("--bls-csv", str(path), check=False)
            self.assertEqual((done.returncode, done.stdout), (1, ""))
            self.assertIn("is not a number", done.stderr)


@unittest.skipUnless(os.getenv("RYFT_ETL_TEST_DATABASE_URL"), "requires a disposable migrated PostgreSQL database")
class PostgresTests(unittest.TestCase):
    CODES = ("M111300", "G300000", "B100000", "F200000")

    def test_full_import_skips_twins_keeps_staples_and_reruns_unchanged(self):
        ids = {code: uuid.uuid5(IMPORT_NAMESPACE, f"bls:{code}") for code in self.CODES}
        staple = {code: str(amount) for code, amount in STAPLES["bls"]["M111300"]["nutrients"].items()}
        staple.update(vitamin_b12="0.4")
        version = "py-etl-test:bls_4_0:staples2:csv"
        curation = curation_with_twins(KAROTTE_TWIN)
        with TemporaryDirectory() as workspace, psycopg.connect(os.environ["RYFT_ETL_TEST_DATABASE_URL"], autocommit=True) as db:
            path = Path(workspace) / "bls.csv"
            write_csv(path, [
                csv_row("M111300", "Milch, Kuhmilch, 3,5 % Fett", "Cow milk", **staple),
                csv_row("G300000", "Möhre, roh", "Carrot, raw"),
                csv_row("B100000", "Brötchen, Weizen", "Bread roll, wheat"),
                csv_row("F200000", "Apfel, roh"),
            ])

            def run():
                notes = BlsNotes()
                return process(bls_csv_rows(path, 100), partial(bls_row_food, curation=curation, notes=notes), version,
                               db, "bls", partial(notes.summary, db))

            def wipe():  # the catalog is shared with the other tests: leave no BLS rows behind
                db.execute("RESET ROLE")
                db.execute("DELETE FROM nutrition.foods WHERE id = ANY(%s)", (list(ids.values()),))

            wipe()
            try:
                db.execute("SET ROLE ryft_nutrition_etl")
                first = run()
                self.assertEqual(first["counts"], {"processed": 4, "accepted": 3, "skipped": 1, "inserted": 3, "duplicates": 0,
                                                   "rejected": 0, "updated": 0, "unchanged": 0, "conflict": 0})
                self.assertEqual((first["skipped_twin"], first["twin_mismatch"], first["aliases_added"]), (1, {}, 3))
                self.assertEqual(first["stale_twins"], {"G300000": {"imported": False}})  # looked up, not written
                second = run()
                self.assertEqual(second["counts"], {"processed": 4, "accepted": 3, "skipped": 1, "unchanged": 3, "duplicates": 0,
                                                    "rejected": 0, "updated": 0, "inserted": 0, "conflict": 0})
                db.execute("RESET ROLE")

                self.assertEqual(dict(db.execute("""SELECT external_id, license_identifier FROM nutrition.food_sources
                    WHERE provider='bls' AND external_id = ANY(%s)""", (list(self.CODES),)).fetchall()),
                                 {"M111300": "CC-BY-4.0", "B100000": "CC-BY-4.0", "F200000": "CC-BY-4.0"})  # no twin
                self.assertEqual(db.execute("SELECT count(*) FROM nutrition.foods WHERE id=%s", (ids["G300000"],)).fetchone(), (0,))

                # The curated staple keeps its names, portions and flags; the nutrients are the row's.
                self.assertEqual(db.execute("SELECT canonical_name, is_canonical_staple, canonical_group_key, density_g_ml FROM nutrition.foods WHERE id=%s",
                                            (ids["M111300"],)).fetchone(), ("Whole milk 3.5%", True, "milk", Decimal("1.030")))
                names = db.execute("SELECT language, label, alias FROM nutrition.food_names WHERE food_id=%s",
                                   (ids["M111300"],)).fetchall()
                self.assertIn(("de", "Vollmilch 3,5 %", False), names)
                self.assertIn(("de", "Milch", True), names)
                self.assertEqual(db.execute("""SELECT count(*), count(*) FILTER (WHERE is_default) FROM nutrition.portions
                    WHERE food_id=%s""", (ids["M111300"],)).fetchone(), (3, 1))
                self.assertEqual(db.execute("SELECT method FROM nutrition.nutrient_values WHERE food_id=%s AND code='vitamin_b12'",
                                            (ids["M111300"],)).fetchone(), ("bls:4.0",))

                # A generic food: German canonical name, English name, synonym aliases flagged as aliases.
                self.assertEqual(db.execute("SELECT canonical_name, is_canonical_staple, food_type, markets FROM nutrition.foods WHERE id=%s",
                                            (ids["B100000"],)).fetchone(), ("Brötchen, Weizen", False, "generic", ["DE"]))
                self.assertEqual(sorted(db.execute("SELECT language, label FROM nutrition.food_names WHERE food_id=%s AND NOT alias",
                                                   (ids["B100000"],)).fetchall()),
                                 [("de", "Brötchen, Weizen"), ("en", "Bread roll, wheat")])
                self.assertEqual(sorted(label for (label,) in db.execute(
                    "SELECT label FROM nutrition.food_names WHERE food_id=%s AND alias AND language='de'", (ids["B100000"],))),
                                 ["Schrippe, Weizen", "Semmel, Weizen", "Weckle, Weizen"])
                self.assertEqual(db.execute("SELECT count(*) FROM nutrition.portions WHERE food_id=%s", (ids["B100000"],)).fetchone(), (0,))
                self.assertEqual(db.execute("SELECT food_form FROM nutrition.foods WHERE id=%s", (ids["F200000"],)).fetchone(), ("raw",))
                self.assertEqual(db.execute("SELECT count(*) FROM nutrition.classifications WHERE food_id=%s AND system='ahei_category'",
                                            (ids["B100000"],)).fetchone(), (1,))
            finally:
                wipe()


if __name__ == "__main__":
    unittest.main()
