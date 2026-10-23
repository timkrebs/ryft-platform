#!/usr/bin/env bash
# The bytes of every tracked file (CI's `manifests` job, first step; .githooks/pre-commit on the
# staged files). Line-based checks (deploy_targets.sh, check_registry.sh, check_workflows.sh) and
# the parsers disagree about what a line break is: U+0085, U+2028, U+2029 and a lone CR end a
# line for a YAML parser but not for grep or awk, so one comment line can carry a hidden second
# line (a sidecar with envFrom: ryft-secrets, an `images:` entry with a newName). Kustomize also
# reads files that no extension filter selects (`Kustomization`, any extension in `resources:` or
# `patches:`, directories outside deploy/), so the rule is by CONTENT and covers every tracked
# file. Only a file under web/ whose extension is on the short binary list below is not decoded
# (the site's images and fonts; never "looks binary": a NUL is a finding); a binary extension
# anywhere else is read like text. No tracked symlink (mode 120000) and no gitlink (160000) is
# allowed: a symlink named *.png, or the target of a kustomize reference, carries text that no
# byte check would read. A file must be valid UTF-8 and hold none of these code points:
#   - CR and every other control except tab and newline (Cc: C0, DEL, C1 with U+0085),
#   - format characters (Cf: bidi controls, zero width, BOM, soft hyphen, ...),
#   - U+2028 and U+2029 (Zl, Zp), private use (Co), surrogates (Cs), noncharacters,
#   - any space separator (Zs) other than U+0020,
#   - the default-ignorable characters that are not Cf either (they render as nothing and split
#     a word or a key for a search): U+034F, U+115F, U+1160, U+17B4, U+17B5, U+180B-U+180F, U+3164,
#     U+FE00-U+FE0F, U+FFA0, U+E0100-U+E01EF, plus U+2800 (braille blank) and the combining
#     overlay U+0336. web/scripts/lib/brand.mjs's INVISIBLE class is the reference.
# (Unassigned code points are not refused by category: which ones are unassigned depends on the
# Python's Unicode version, so a newer emoji would fail on one machine only.)
# Arrows, dashes and accents are fine. There is no exemption by path: a source that needs one of
# these characters writes it as an escape (a \u sequence in JavaScript or Go), which is also how
# a reader sees it.
# Also refused by NAME, wherever they are: `Kustomization` and `kustomization.yml` (kustomize
# reads them, the folder checks look for kustomization.yaml), `.gitattributes` (a
# working-tree-encoding can turn mojibake into a real line break after checkout) and
# `.gitmodules` (the repository has none).
#   scripts/check_bytes.sh              the tracked files (the working tree's bytes)
#   scripts/check_bytes.sh --staged     the staged files, as staged (.githooks/pre-commit)
#   scripts/check_bytes.sh <file>...    exactly these files (tests)
# Needs python3. Exit 1: a finding. Exit 2: no files to check, or no python3 (fails closed).
set -euo pipefail
cd "$(dirname "$0")/.."
command -v python3 >/dev/null 2>&1 || { echo "check_bytes: python3 not found: cannot check, so failing" >&2; exit 2; }
mode=tracked
case "${1:-}" in
  --staged) mode=staged; shift ;;
  "") ;;
  -*) echo "usage: check_bytes.sh [--staged | file...]" >&2; exit 2 ;;
  *) mode=files ;;
esac
exec python3 -I - "$mode" "$@" <<'PY'
import os, re, subprocess, sys, unicodedata

mode = sys.argv[1]
files = sys.argv[2:]

# Not decoded, and only below web/ (the site's images and fonts). Anywhere else the extension buys nothing.
BINARY = {"png", "jpg", "jpeg", "gif", "ico", "webp", "avif", "woff", "woff2", "ttf", "otf", "pdf"}
BINARY_ROOT = "web/"
# Default-ignorable characters that are not Cf (Cf is refused by category), plus two look-alike
# makers. Reference: INVISIBLE in web/scripts/lib/brand.mjs.
IGNORABLE = [
    (0x034F, 0x034F), (0x115F, 0x1160), (0x17B4, 0x17B5), (0x180B, 0x180F), (0x3164, 0x3164),
    (0xFE00, 0xFE0F), (0xFFA0, 0xFFA0), (0xE0100, 0xE01EF),
    (0x2800, 0x2800),  # braille blank: a space that is not a space
    (0x0336, 0x0336),  # combining long stroke overlay: strikes a letter through
]
REGULAR_MODES = {"100644", "100755"}
NAMES_REFUSED = {
    "Kustomization": "kustomize reads it and no check looks for it; use kustomization.yaml",
    ".gitattributes": "a working-tree-encoding can turn text into a hidden line break after checkout",
    ".gitmodules": "the repository has no submodules",
}


def vcs(*args):
    return subprocess.run(["g" + "it", *args], check=True, capture_output=True).stdout


def flagged(ch):
    o = ord(ch)
    if ch in "\t\n":
        return None
    if o == 0x0D:
        return "CR (a line break to YAML, not to grep or awk)"
    if o == 0x85:
        return "NEXT LINE (a line break to YAML)"
    if o == 0x2028:
        return "LINE SEPARATOR (a line break to YAML)"
    if o == 0x2029:
        return "PARAGRAPH SEPARATOR (a line break to YAML)"
    c = unicodedata.category(ch)
    if c in ("Cc", "Cf", "Zl", "Zp", "Co", "Cs") or (c == "Zs" and ch != " "):
        return f"{c} {unicodedata.name(ch, 'unnamed')}"
    if (0xFDD0 <= o <= 0xFDEF) or (o & 0xFFFE) == 0xFFFE:
        return "noncharacter"
    if any(lo <= o <= hi for lo, hi in IGNORABLE):
        return "default-ignorable (renders as nothing or alters the look of its neighbours)"
    return None


# items: (path, git mode, staged)
if mode == "files":
    items = []
    for f in files:
        st = os.lstat(f) if os.path.lexists(f) else None
        m = "120000" if st and os.path.islink(f) else "100644"
        items.append((f, m, False))
elif mode == "staged":
    # A (added), C, M, R and T (type change: a file became a symlink); not D.
    out = vcs("diff", "--cached", "--raw", "-z", "--no-renames", "--diff-filter=ACMRT")
    parts = out.split(b"\0")
    items = []
    for i in range(0, len(parts) - 1, 2):
        meta = parts[i].decode().split()  # :oldmode newmode oldsha newsha status
        items.append((parts[i + 1].decode("utf-8", "surrogateescape"), meta[1], True))
else:
    out = vcs("ls-files", "-s", "-z")
    items = []
    for rec in out.split(b"\0"):
        if rec:
            meta, _, p = rec.partition(b"\t")
            items.append((p.decode("utf-8", "surrogateescape"), meta.split()[0].decode(), False))
    if not items:
        print("check_bytes: no tracked file: refusing to pass on nothing", file=sys.stderr)
        sys.exit(2)

bad = 0
checked = 0


def err(path, line, msg):
    global bad
    bad += 1
    where = f"file={path},line={line}," if line else f"file={path},"
    print(f"::error {where}title=file bytes::{path}{f' (line {line})' if line else ''}: {msg}", file=sys.stderr)


for path, fmode, staged in items:
    base = path.rsplit("/", 1)[-1]
    if base in NAMES_REFUSED:
        err(path, 0, f"a file named {base} is not allowed: {NAMES_REFUSED[base]}")
    elif re.fullmatch(r"kustomization(\.ya?ml)?", base, re.I) and base != "kustomization.yaml":
        err(path, 0, f"a file named {base} is not allowed: kustomize reads it and no check looks for it; use kustomization.yaml")
    try:
        path.encode("utf-8")
        pname = path
    except UnicodeEncodeError:
        err(path, 0, "the file NAME is not valid UTF-8")
        continue
    for ch in pname:
        if flagged(ch):
            err(path, 0, f"the file NAME holds U+{ord(ch):04X} ({flagged(ch)})")
            break
    if fmode not in REGULAR_MODES:
        what = "a symlink" if fmode == "120000" else "a submodule entry" if fmode == "160000" else f"mode {fmode}"
        err(path, 0, f"{what} is not allowed: a link carries text that no byte check or kustomize reference check reads; commit the file itself")
        continue
    if path.startswith(BINARY_ROOT) and "." in base and base.rsplit(".", 1)[-1].lower() in BINARY:
        continue
    checked += 1
    try:
        if staged:
            data = vcs("show", ":" + path)
        else:
            data = open(path, "rb").read()
    except (OSError, subprocess.CalledProcessError) as e:
        err(path, 0, f"cannot read it: {e}")
        continue
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError as e:
        err(path, data.count(b"\n", 0, e.start) + 1, f"not valid UTF-8 (byte offset {e.start})")
        continue
    seen = set()
    line = 1
    for ch in text:
        why = flagged(ch)
        if why and (line, ch) not in seen:
            seen.add((line, ch))
            err(path, line, f"U+{ord(ch):04X} {why}. A hidden line break, bidi or other invisible character lets a comment carry a second line to a parser; retype the line.")
        if ch == "\n":
            line += 1
if bad:
    sys.exit(1)
if mode != "staged" and checked == 0:
    print("check_bytes: nothing was checked", file=sys.stderr)
    sys.exit(2)
print(f"file bytes: {checked} files, valid UTF-8, no control, line-break, format, invisible or bidi character, BOM, odd space, symlink or banned name")
PY
