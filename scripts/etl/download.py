"""Resolve official release artifacts and stage bounded USDA archives."""

from __future__ import annotations

import hashlib
import re
import shutil
import sys
import zipfile
from dataclasses import dataclass, replace
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import urljoin, urlparse
from uuid import uuid4

import httpx

FDC = "https://fdc.nal.usda.gov"
FDC_RELEASES = f"{FDC}/download-datasets/"
OFF_REPO = "https://huggingface.co/datasets/openfoodfacts/product-database"
OFF_METADATA = "https://huggingface.co/api/datasets/openfoodfacts/product-database"
CLIENT_HEADERS = {"User-Agent": "RYFT-Nutrition-ETL/1.0 (https://ryftlab.de)"}
# Sent with every USDA archive request: a cache between us and USDA must not answer with a stale ZIP (D313).
NO_CACHE_HEADERS = {"Cache-Control": "no-cache", "Pragma": "no-cache"}
USDA_ATTEMPTS = 3
ZIP_LIMIT = 64 * 1024**2
CSV_LIMIT = 192 * 1024**2
RELEASE_PATTERN = re.compile(
    r"/fdc-datasets/FoodData_Central_(foundation_food|sr_legacy_food)_csv_(\d{4}-\d{2}(?:-\d{2})?)\.zip$"
)
USDA_TABLES = {"food.csv", "food_nutrient.csv", "food_category.csv", "nutrient.csv", "food_portion.csv", "measure_unit.csv"}
REQUIRED_USDA_TABLES = {"food.csv", "food_nutrient.csv"}


@dataclass(frozen=True)
class Release:
    location: str
    version: str


class MissingUSDATables(ValueError):
    """An official ZIP was downloaded but lacks tables required by the importer."""


class ReleaseLinks(HTMLParser):
    def __init__(self) -> None:
        super().__init__()
        self.releases: dict[str, dict[str, Release]] = {}

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        if tag != "a":
            return
        link = dict(attrs).get("href")
        if not link:
            return
        url = urljoin(FDC_RELEASES, link)
        parsed = urlparse(url)
        found = RELEASE_PATTERN.fullmatch(parsed.path)
        if parsed.scheme != "https" or parsed.netloc != urlparse(FDC).netloc or not found:
            return
        kind, day = found.groups()
        self.releases.setdefault(kind, {}).setdefault(day, Release(url, day))


def resolve_usda(client: httpx.Client) -> dict[str, list[Release]]:
    response = client.get(FDC_RELEASES)
    response.raise_for_status()
    page = ReleaseLinks()
    page.feed(response.text)
    return {
        kind: sorted(releases.values(), key=lambda release: release.version, reverse=True)
        for kind, releases in page.releases.items()
    }


def resolve_off(client: httpx.Client, local: Path | None = None) -> Release:
    if local is not None:
        return Release(str(local.resolve()), f"local-{file_sha256(local)}")
    response = client.get(OFF_METADATA)
    response.raise_for_status()
    metadata = response.json()
    revision = metadata.get("sha") if isinstance(metadata, dict) else None
    if not isinstance(revision, str) or not re.fullmatch(r"[a-f0-9]{40}", revision):
        raise ValueError("OFF metadata did not identify an immutable dataset revision")
    return Release(f"{OFF_REPO}/resolve/{revision}/food.parquet", revision)


def file_sha256(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def download_usda(client: httpx.Client, release: Release, directory: Path) -> Path:
    archive = directory / "release.zip"
    total = 0
    with client.stream("GET", release.location, headers=NO_CACHE_HEADERS) as response:
        response.raise_for_status()
        if int(response.headers.get("Content-Length", 0)) > ZIP_LIMIT:
            raise ValueError("USDA archive exceeds its download limit")
        with archive.open("wb") as dest:
            for data in response.iter_bytes(chunk_size=256 * 1024):
                total += len(data)
                if total > ZIP_LIMIT:
                    raise ValueError("USDA archive exceeds its download limit")
                dest.write(data)
    return archive


def stage_usda(client: httpx.Client, releases: list[Release], directory: Path) -> tuple[Path, dict[str, Path], Release]:
    """Stage the newest release, retrying stale content, and never fall back to an older release (D313).

    USDA can replace the bytes at an existing release URL while caches still serve its old ZIP. Every attempt asks for
    a fresh URL (a new query parameter) with no-cache headers. After USDA_ATTEMPTS attempts that still lack the tables
    the kind fails: importing an older release under the newest one's name would hide the problem.
    """
    latest = releases[0]
    for attempt in range(1, USDA_ATTEMPTS + 1):
        fresh = httpx.URL(latest.location).copy_merge_params({"ryft_retry": uuid4().hex})
        archive = download_usda(client, replace(latest, location=str(fresh)), directory)
        try:
            files = extract_usda(archive, directory)
        except MissingUSDATables as error:
            if attempt == USDA_ATTEMPTS:
                raise MissingUSDATables(
                    f"USDA release {latest.version}: {error}; still incomplete after {attempt} attempts, "
                    "not falling back to an older release") from error
            print(f"USDA release {latest.version}: {error}; attempt {attempt} of {USDA_ATTEMPTS}, retrying with a fresh URL",
                  file=sys.stderr, flush=True)
        else:
            return archive, files, latest
    raise AssertionError("USDA attempts exhausted")


def extract_usda(archive: Path, directory: Path) -> dict[str, Path]:
    """Extract selected CSVs to fixed destinations, validating sizes and normalizing text."""
    with zipfile.ZipFile(archive) as source:
        members: dict[str, zipfile.ZipInfo] = {}
        for member in source.infolist():
            name = Path(member.filename).name
            if name not in USDA_TABLES or member.is_dir():
                continue
            if name in members:
                raise ValueError(f"duplicate USDA archive member: {name}")
            members[name] = member
        missing = REQUIRED_USDA_TABLES - members.keys()
        if missing:
            raise MissingUSDATables(f"missing USDA CSV tables: {', '.join(sorted(missing))}")
        if sum(m.file_size for m in members.values()) > CSV_LIMIT:
            raise ValueError("USDA CSV tables exceed the extraction limit")
        output = {}
        for name, member in members.items():
            dest = directory / name
            with source.open(member) as compressed, dest.open("wb") as staged:
                shutil.copyfileobj(compressed, staged, length=256 * 1024)
            if not is_utf8(dest):
                transcode_legacy_csv(dest)
            output[name] = dest
        return output


def is_utf8(path: Path) -> bool:
    try:
        with path.open(encoding="utf-8-sig") as text:
            while text.read(256 * 1024):
                pass
    except UnicodeDecodeError:
        return False
    return True


def transcode_legacy_csv(path: Path) -> None:
    output = path.with_suffix(".utf8")
    with path.open(encoding="latin-1", newline="") as src, output.open("w", encoding="utf-8", newline="") as dst:
        while chunk := src.read(256 * 1024):
            dst.write(chunk)
        if dst.tell() > CSV_LIMIT:
            raise ValueError("normalized legacy CSV exceeds the size limit")
    output.replace(path)
