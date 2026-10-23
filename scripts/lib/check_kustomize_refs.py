"""The body of scripts/check_kustomize_refs.sh (see the header there)."""
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import yamlpolicy  # noqa: E402

CONTAIN = os.path.realpath("deploy")
SEGMENT = re.compile(r"^(\.\.|[A-Za-z0-9._-]+)$")
bad = False


def say(f, msg):
    global bad
    bad = True
    print(f"::error file={f},title=kustomize refs::{f}: {msg}", file=sys.stderr)


def ref(f, key, entry, kind, queue):
    """kind: res (a directory with kustomization.yaml, or *.yaml / *.yml), patch (*.yaml, *.yml,
    *.json), env (*.env; may be generated and git-ignored, so it need not exist)."""
    here = os.path.dirname(f)
    if not isinstance(entry, str) or not entry:
        return say(f, f"{key}: an entry that is not a plain string: {entry!r}")
    if not all(SEGMENT.match(s) for s in entry.split("/")):
        return say(f, f"{key}: {entry!r} is not a plain relative path (remote, absolute or odd characters)")
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
    if kind == "res":
        if os.path.isdir(p):
            k = os.path.join(p, "kustomization.yaml")
            if os.path.islink(k) or not os.path.isfile(k):
                say(f, f"{key}: {entry!r} is a directory without a regular kustomization.yaml")
            else:
                queue.append(k)
        elif not (os.path.isfile(p) and p.endswith((".yaml", ".yml"))):
            say(f, f"{key}: {entry!r} is neither a directory nor a *.yaml / *.yml file")
    elif kind == "patch":
        if not (os.path.isfile(p) and p.endswith((".yaml", ".yml", ".json"))):
            say(f, f"{key}: {entry!r} is not a *.yaml / *.yml / *.json file")
    else:
        if not (os.path.isfile(p) and p.endswith(".env")):
            say(f, f"{key}: {entry!r} is not a *.env file")


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
        k, errs = yamlpolicy.load(f, yamlpolicy.KUSTOMIZATION)
        for e in errs:
            say(f, e)
        if k is None:
            continue
        for e in k.get("resources", []):
            ref(f, "resources", e, "res", queue)
        for p in k.get("patches", []):
            if "path" in p:
                ref(f, "patches[].path", p["path"], "patch", queue)
        for key in ("configMapGenerator", "secretGenerator"):
            for g in k.get(key, []):
                for e in g.get("envs", []):
                    ref(f, f"{key}[].envs", e, "env", queue)
    if bad:
        return 1
    print(f"✓ {n} kustomizations: only the allowed keys and YAML tags, and every resource, patch and env file is a plain path inside deploy/ that exists, no symlink")
    return 0


sys.exit(main())
