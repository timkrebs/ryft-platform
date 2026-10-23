#!/usr/bin/env bash
# The bytes of every tracked file (CI's `bytes` job, which runs on every push and pull request with
# no path filter; the `manifests` job repeats it as its first step; .githooks/pre-commit runs it on
# the staged files). Line-based checks (deploy_targets.sh, check_registry.sh, check_workflows.sh) and
# the parsers disagree about what a line break is: U+0085, U+2028, U+2029 and a lone CR end a
# line for a YAML parser but not for grep or awk, so one comment line can carry a hidden second
# line (a sidecar with envFrom: ryft-secrets, an `images:` entry with a newName). Kustomize also
# reads files that no extension filter selects (`Kustomization`, any extension in `resources:` or
# `patches:`, directories outside deploy/), so the rule is by CONTENT and covers every tracked
# file. A file must be valid UTF-8 and hold none of the code points that scripts/lib/charpolicy.py
# refuses (it is the one definition, shared with the decoded-string check of yamlpolicy.py):
# controls (CR, C1 with U+0085), format characters (bidi, zero width, BOM, soft hyphen), U+2028 and
# U+2029, private use, surrogates, noncharacters, any space but U+0020, and the default-ignorable
# characters that are not format characters (U+034F, U+115F/1160, U+17B4/5, U+180B-180F, U+2065,
# U+3164, U+FE00-FE0F, U+FFA0, U+FFF0-FFF8, U+E0000-E0FFF, plus U+2800 and U+0336). The one
# allowance: a single U+FE0E / U+FE0F directly after a symbol (the emoji picker inserts it).
# Binary extensions (png, jpg, woff2, pdf, ...) are not decoded anywhere EXCEPT under deploy/ and
# .github/ (kustomize can name any file there, so a text file called .png is read); a NUL is never
# "looks binary", it is a finding in a text file. No tracked symlink (mode 120000) and no gitlink
# (160000) is allowed: a link carries text that no byte check would read. There is no exemption by
# path: a source that needs an invisible character writes it as an escape (a backslash-u sequence in
# JavaScript or Go), which is also how a reader sees it.
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
import os, re, subprocess, sys

sys.path.insert(0, os.path.join("scripts", "lib"))
try:
    import charpolicy
except ImportError as e:
    print(f"check_bytes: scripts/lib/charpolicy.py cannot be imported ({e}): cannot check, so failing", file=sys.stderr)
    sys.exit(2)
flagged = charpolicy.flagged

mode = sys.argv[1]
files = sys.argv[2:]

# A binary extension means "not decoded" (images, fonts, a PDF) EVERYWHERE except where kustomize or
# GitHub Actions read files: under deploy/ and .github/ every file is read as text, because a
# kustomization can name any file there. Symlinks are refused, so an extension cannot point at
# another file, and kustomize references are limited to yaml/yml/json/env (check_kustomize_refs).
BINARY = {"png", "jpg", "jpeg", "gif", "ico", "webp", "avif", "woff", "woff2", "ttf", "otf", "pdf"}
READ_AS_TEXT = ("deploy/", ".github/")
REGULAR_MODES = {"100644", "100755"}
NAMES_REFUSED = {
    "Kustomization": "kustomize reads it and no check looks for it; use kustomization.yaml",
    ".gitattributes": "a working-tree-encoding can turn text into a hidden line break after checkout",
    ".gitmodules": "the repository has no submodules",
}


def vcs(*args):
    return subprocess.run(["g" + "it", *args], check=True, capture_output=True).stdout


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
    binary_ext = "." in base and base.rsplit(".", 1)[-1].lower() in BINARY
    if binary_ext and not path.startswith(READ_AS_TEXT):
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
        err(path, data.count(b"\n", 0, e.start) + 1, f"not valid UTF-8 (byte offset {e.start})"
            + (" (a binary extension is read as text under deploy/ and .github/, which hold no binaries: kustomize can name any file there)" if binary_ext else ""))
        continue
    for line, ch, why in charpolicy.findings(text):
        err(path, line, f"U+{ord(ch):04X} {why}. A hidden line break, bidi or other invisible character lets a comment carry a second line to a parser; retype the line.")
if bad:
    sys.exit(1)
if mode != "staged" and checked == 0:
    print("check_bytes: nothing was checked", file=sys.stderr)
    sys.exit(2)
print(f"file bytes: {checked} files, valid UTF-8, no control, line-break, format, invisible or bidi character, BOM, odd space, symlink or banned name")
PY
