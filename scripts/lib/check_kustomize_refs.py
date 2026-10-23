"""The body of scripts/check_kustomize_refs.sh (see the header there)."""
import os
import re
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import yamlpolicy  # noqa: E402

CONTAIN = os.path.realpath("deploy")
SEGMENT = re.compile(r"^(\.\.|[A-Za-z0-9._-]+)$")
PATCH_STYLES = '[(.patches // [])[] | select(has("patch")) | .patch | style]'
bad = False


def say(f, msg):
    global bad
    bad = True
    print(f"::error file={f},title=kustomize refs::{f}: {msg}", file=sys.stderr)


def plain_resource(entry):
    """Why a `resources:` entry could be read by kustomize as something remote, or None. Kustomize
    treats `github.com/org/repo`, `host.tld/x.git` and the like as a git URL and clones it BEFORE it
    looks for a local directory of that name, so a committed decoy directory does not help: no
    segment but the last may contain a dot (other than . and ..), and the last may only if the entry
    is a *.yaml / *.yml file."""
    segs = entry.split("/")
    for i, sg in enumerate(segs):
        if sg in (".", ".."):
            continue
        if "." in sg and not (i == len(segs) - 1 and sg.endswith((".yaml", ".yml")) and not sg.startswith(".")):
            return f"the segment {sg!r} contains a dot: kustomize would read {entry!r} as a remote git URL (any case, with or without .git) and fetch it before looking for a local directory"
    return None


def ref(f, key, entry, kind, queue):
    """kind: res (a directory with kustomization.yaml, or *.yaml / *.yml), patch (*.yaml, *.yml,
    *.json), env (*.env; may be generated and git-ignored, so it need not exist), file (any of
    those, for keys that may be added to the schema later)."""
    here = os.path.dirname(f)
    if not isinstance(entry, str) or not entry:
        return say(f, f"{key}: an entry that is not a plain string: {entry!r}")
    if not all(SEGMENT.match(s) for s in entry.split("/") if s):
        return say(f, f"{key}: {entry!r} is not a plain relative path (remote, absolute or odd characters)")
    if entry.startswith("/") or entry.endswith("/") or "//" in entry:
        return say(f, f"{key}: {entry!r} has a leading, trailing or doubled slash: write the path without it")
    if kind in ("res", "file"):
        why = plain_resource(entry)
        if why:
            return say(f, f"{key}: {why}")
    if kind not in ("res", "patch", "env", "file"):
        return say(f, f"{key}: the schema marks it as path kind {kind!r}, which this check does not know: add it to ref()")
    cur = here
    for part in entry.split("/"):
        cur = os.path.dirname(cur) if part == ".." else os.path.join(cur, part)
        if os.path.islink(cur):
            say(f, f"{key}: {entry!r} is or goes through a symlink ({cur})")
    p = os.path.realpath(os.path.join(here, entry))
    if p != CONTAIN and not p.startswith(CONTAIN + os.sep):
        return say(f, f"{key}: {entry!r} resolves outside deploy/")
    if not os.path.lexists(p):
        if kind == "env":
            if not entry.endswith(".env"):
                say(f, f"{key}: {entry!r} is not a *.env file")
            return
        return say(f, f"{key}: {entry!r} does not exist")
    exts = {"patch": (".yaml", ".yml", ".json"), "env": (".env",), "file": (".yaml", ".yml", ".json", ".env")}
    if kind == "res":
        if os.path.isdir(p):
            k = os.path.join(p, "kustomization.yaml")
            if os.path.islink(k) or not os.path.isfile(k):
                say(f, f"{key}: {entry!r} is a directory without a regular kustomization.yaml")
            else:
                queue.append(k)
        elif not (os.path.isfile(p) and p.endswith((".yaml", ".yml"))):
            say(f, f"{key}: {entry!r} is neither a directory nor a *.yaml / *.yml file")
    elif not (os.path.isfile(p) and p.endswith(exts[kind])):
        say(f, f"{key}: {entry!r} is not a {' / '.join('*' + e for e in exts[kind])} file")
    if kind in ("env", "file") and os.path.isfile(p) and p.endswith(".env"):
        env_lines(f, key, entry, p)


def env_lines(f, key, entry, p):
    """A line of a generator's env file without `=` takes its VALUE from the environment of the process
    that renders (kustomize reads os.Getenv for it): CI's tokens would end up in a ConfigMap."""
    try:
        lines = open(p, encoding="utf-8").read().split("\n")
    except (OSError, UnicodeDecodeError) as e:
        return say(f, f"{key}: {entry!r} cannot be read: {e}")
    for n, line in enumerate(lines, 1):
        t = line.strip()
        if t and not t.startswith("#") and "=" not in t:
            say(f, f"{key}: {entry!r} line {n} has no '=': kustomize would take that variable's value from the environment of whoever renders it")


def scan_decoded(root, skip):
    """Every other YAML / JSON file under root: no keys or strings that decode to a line break, a
    control or an invisible character (the byte check reads bytes, a quoted escape is ASCII)."""
    n = 0
    for d, _, files in os.walk(root):
        for name in files:
            full = os.path.join(d, name)
            if name == "kustomization.yaml" or os.path.islink(full) or not name.endswith((".yaml", ".yml", ".json")):
                continue
            r = subprocess.run(["yq", "-o=json", "-I0", ".", full], capture_output=True, text=True)
            if r.returncode != 0:
                say(full, f"yq cannot parse it: {r.stderr.strip()[:200]}")
                continue
            try:
                docs = yamlpolicy._json_stream(r.stdout)
            except ValueError as e:
                say(full, str(e))
                continue
            errs = []
            yamlpolicy._decoded(docs, "", errs)
            for e in errs:
                say(full, e)
            n += 1
    return n


def main():
    root = sys.argv[1] if len(sys.argv) > 1 else "deploy"
    queue = []
    for d, dirs, files in os.walk(root):  # followlinks=False
        for name in dirs + files:
            full = os.path.join(d, name)
            if os.path.islink(full):
                say(full, "a symlink under deploy/ is not allowed (the byte check and the reference check cannot read through one)")
            if re.fullmatch(r"kustomization(\.ya?ml)?", name, re.I):
                if name != "kustomization.yaml":
                    say(full, f"a file named {name} is not allowed; use kustomization.yaml")
                elif os.path.isfile(full):
                    queue.append(full)
    if not queue:
        print(f"check_kustomize_refs: no kustomization under {root}", file=sys.stderr)
        return 2
    seen, n = set(), 0
    while queue:
        f = queue.pop(0)
        if os.path.realpath(f) in seen:
            continue
        seen.add(os.path.realpath(f))
        n += 1
        k, errs, paths = yamlpolicy.load(f, yamlpolicy.KUSTOMIZATION, literal=PATCH_STYLES)
        for e in errs:
            say(f, e)
        if k is None or errs:
            continue  # the walk needs a document of the right shape
        for kind, value, where in paths:
            ref(f, where, value, kind, queue)
    m = scan_decoded(root, seen)
    if bad:
        return 1
    print(f"✓ {n} kustomizations: only the allowed keys and YAML tags, inline patches as literal blocks, every path "
          f"(resource, patch, env) plain, inside deploy/, no symlink, no remote-looking name; {m} other YAML/JSON files have no escaped line breaks or invisible characters")
    return 0


sys.exit(main())
