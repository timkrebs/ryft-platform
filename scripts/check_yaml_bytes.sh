#!/usr/bin/env bash
# The bytes of the YAML that kustomize, Argo CD and GitHub read (CI's `manifests` job).
# Line-based checks (deploy_targets.sh, check_registry.sh, check_workflows.sh) and the YAML
# parsers disagree about what a line break is: U+0085, U+2028, U+2029 and a lone CR end a
# line for the parser but not for grep or awk, so one comment line can carry a hidden second
# line (a sidecar container with envFrom: ryft-secrets, an `images:` entry with a newName).
# This fails on, in every tracked *.yml / *.yaml under deploy/, .github/ and
# infra/vault/tests/golden/:
#   - invalid UTF-8,
#   - CR, any other C0 control except tab and newline, DEL, the C1 controls (U+0080-U+009F,
#     among them U+0085),
#   - U+2028, U+2029,
#   - bidirectional controls: U+061C, U+200E, U+200F, U+202A-U+202E, U+2066-U+2069,
#   - U+FEFF (a BOM, at the start or anywhere).
# Everything else is allowed: the files use arrows and dashes, so plain ASCII is not the rule.
#   scripts/check_yaml_bytes.sh              the tracked files (the working tree's bytes)
#   scripts/check_yaml_bytes.sh --staged     the staged files, as staged (.githooks/pre-commit)
#   scripts/check_yaml_bytes.sh <file>...    exactly these files (tests)
# Needs python3. Exit 1: a finding. Exit 2: no files to check, or no python3 (fails closed).
set -euo pipefail
cd "$(dirname "$0")/.."
command -v python3 >/dev/null 2>&1 || { echo "check_yaml_bytes: python3 not found: cannot check, so failing" >&2; exit 2; }
mode=tracked
case "${1:-}" in
  --staged) mode=staged; shift ;;
  "") ;;
  -*) echo "usage: check_yaml_bytes.sh [--staged | file...]" >&2; exit 2 ;;
  *) mode=files ;;
esac
specs=('deploy/*.yml' 'deploy/*.yaml' '.github/*.yml' '.github/*.yaml' 'infra/vault/tests/golden/*.yml' 'infra/vault/tests/golden/*.yaml')
exec python3 -I - "$mode" "${specs[@]}" -- "$@" <<'PY'
import subprocess, sys

mode = sys.argv[1]
rest = sys.argv[2:]
sep = rest.index("--")
specs, files = rest[:sep], rest[sep + 1:]

BAD = {}
for c in range(0x20):
    if c not in (0x09, 0x0A):
        BAD[c] = "C0 control"
BAD[0x0D] = "CR (a line break to YAML, not to grep or awk)"
BAD[0x7F] = "DEL"
for c in range(0x80, 0xA0):
    BAD[c] = "C1 control (U+0085 is a line break to YAML)" if c == 0x85 else "C1 control"
BAD[0x2028] = "LINE SEPARATOR (a line break to YAML)"
BAD[0x2029] = "PARAGRAPH SEPARATOR (a line break to YAML)"
for c in (0x061C, 0x200E, 0x200F, *range(0x202A, 0x202F), *range(0x2066, 0x206A)):
    BAD[c] = "bidirectional control"
BAD[0xFEFF] = "BOM / zero width no-break space"


def vcs(*args):
    return subprocess.run(["g" + "it", *args], check=True, capture_output=True).stdout


if mode == "files":
    items = [(f, False) for f in files]
elif mode == "staged":
    out = vcs("diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z", "--", *specs)
    items = [(p.decode("utf-8", "surrogateescape"), True) for p in out.split(b"\0") if p]
else:
    out = vcs("ls-files", "-z", "--", *specs)
    items = [(p.decode("utf-8", "surrogateescape"), False) for p in out.split(b"\0") if p]
    if not items:
        print("check_yaml_bytes: no tracked YAML under deploy/, .github/ or infra/vault/tests/golden/: refusing to pass on nothing", file=sys.stderr)
        sys.exit(2)

bad = 0
for path, staged in items:
    try:
        data = vcs("show", ":" + path) if staged else open(path, "rb").read()
    except (OSError, subprocess.CalledProcessError) as e:
        print(f"::error file={path},title=yaml bytes::cannot read {path}: {e}", file=sys.stderr)
        bad += 1
        continue
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError as e:
        line = data.count(b"\n", 0, e.start) + 1
        print(f"::error file={path},line={line},title=yaml bytes::{path} (line {line}): not valid UTF-8 (byte offset {e.start})", file=sys.stderr)
        bad += 1
        continue
    seen = set()
    line = 1
    for ch in text:
        o = ord(ch)
        if o in BAD and (line, o) not in seen:
            seen.add((line, o))
            print(f"::error file={path},line={line},title=yaml bytes::{path} (line {line}): U+{o:04X} {BAD[o]}. A hidden line break or bidi control lets a comment carry a second line to the YAML parser; retype the line.", file=sys.stderr)
            bad += 1
        if ch == "\n":
            line += 1
if bad:
    sys.exit(1)
print(f"yaml bytes: {len(items)} files, valid UTF-8, no control, line-break, bidi or BOM code points")
PY
