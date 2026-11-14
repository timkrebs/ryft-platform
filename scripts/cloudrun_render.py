#!/usr/bin/env python3
"""Render the Cloud Run YAML that deploy.yml applies, from the templates in deploy/run (D223).

  python3 -I scripts/cloudrun_render.py --out <dir> --digest api=sha256:<64 hex> --digest ingest=... \\
      --digest scoring=... --digest notify=... --digest migrate=...      (deploy.yml; all five)
  python3 -I scripts/cloudrun_render.py --lint                            (scripts/check_cloudrun.py)
  Options: --config <config.env> (default deploy/run/config.env), --templates <dir> (default deploy/run).

The values come from config.env (a fixed list of keys, each with its shape) and the image digests. The
templates' placeholders are a fixed list too (VARS below); a placeholder outside it, or any `$` left after
the substitution, fails. No environment variable is read, so nothing outside git and the digests can
change what is deployed. Derived values:
  DATABASE_URL   postgresql://ryft_<svc>.<ref>@<pooler host>:6543/postgres?sslmode=verify-full&
                 sslrootcert=/etc/ryft/supabase-ca.crt&default_query_exec_mode=exec   (the four services)
                 postgresql://postgres.<ref>@<pooler host>:5432/postgres?sslmode=verify-full&
                 sslrootcert=/etc/ryft/supabase-ca.crt                                 (migrate)
  PUSH_AUDIENCE  https://<svc>-<project number>.europe-west1.run.app (the service's own run.app URL)
  IMAGE          europe-west1-docker.pkg.dev/ryftlab-prod/ryft/<svc>@sha256:<digest>
Fails closed: a FILL_ME_IN left in config.env (the config PR before the first deploy), a value of the
wrong shape, a missing, unknown or repeated key, half of the OTLP pair. --lint checks the same but lets
FILL_ME_IN pass (and names what is left), so CI catches a typo in a config PR before the deploy does.
Exit 0 rendered (or linted), 1 a finding, 2 usage.
"""
import argparse
import datetime
import os
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SERVICES = ("api", "ingest", "scoring", "notify")
FILES = SERVICES + ("migrate",)
PROJECT = "ryftlab-prod"
REGION = "europe-west1"
REGISTRY = f"{REGION}-docker.pkg.dev/{PROJECT}/ryft"
CA = "/etc/ryft/supabase-ca.crt"
PLACEHOLDER = "FILL_ME_IN"

# config.env: key -> (required, full-match regex, what it is). Order is the file's.
KEYS = {
    "PRIVACY_POLICY_VERSION": (True, r"[0-9]{4}-[0-9]{2}-[0-9]{2}", "a YYYY-MM-DD date"),
    "LOG_LEVEL": (True, r"debug|info|warn|error", "debug, info, warn or error"),
    "GCP_PROJECT_NUMBER": (True, r"[1-9][0-9]{5,19}", "the project number, digits only"),
    "PUSH_SA_ID": (True, r"[1-9][0-9]{11,24}", "ryft-push's unique ID, digits only"),
    "SUPABASE_PROJECT_REF": (True, r"[a-z]{20}", "the Supabase project ref, 20 lower-case letters"),
    "SUPABASE_POOLER_HOST": (True, r"aws-[0-9]+-eu-central-1\.pooler\.supabase\.com",
                             "aws-<n>-eu-central-1.pooler.supabase.com"),
    "APPLE_KEY_ID": (True, r"[A-Z0-9]{10}", "10 upper-case letters or digits"),
    "APNS_KEY_ID": (True, r"[A-Z0-9]{10}", "10 upper-case letters or digits"),
    "OTLP_ENDPOINT": (False, r"https://otlp-gateway-[a-z0-9-]+\.grafana\.net/otlp",
                      "empty, or https://otlp-gateway-<region>.grafana.net/otlp"),
    "OTLP_USER": (False, r"[1-9][0-9]{0,11}", "empty, or the Grafana Cloud instance ID (digits)"),
}
# The template placeholders, the only names ever substituted.
VARS = ("IMAGE", "DATABASE_URL", "PUSH_AUDIENCE", "PRIVACY_POLICY_VERSION", "LOG_LEVEL", "SUPABASE_PROJECT_REF",
        "PUSH_SA_ID", "OTLP_ENDPOINT", "OTLP_USER", "APPLE_KEY_ID", "APNS_KEY_ID")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}")
LINE = re.compile(r"([A-Z][A-Z0-9_]*)=(.*)")
PLACE = re.compile(r"\$\{([A-Za-z0-9_]*)\}")


class Finding(Exception):
    pass


def say(msg):
    prefix = "::error title=cloudrun render::" if os.environ.get("GITHUB_ACTIONS") == "true" else ""
    print(f"{prefix}cloudrun_render: {msg}", file=sys.stderr)


def read_config(path, lint):
    """The values of config.env, every key checked. Returns (values, placeholders left)."""
    try:
        text = Path(path).read_text(encoding="utf-8")
    except OSError as e:
        raise Finding(f"cannot read {path}: {e.strerror}")
    vals, errs, left = {}, [], []
    for n, raw in enumerate(text.split("\n"), 1):
        line = raw.rstrip("\r")
        if line == "" or line.startswith("#"):
            continue
        m = LINE.fullmatch(line)
        if not m:
            errs.append(f"{path}:{n}: not KEY=value (no spaces, quotes or inline comments)")
            continue
        k, v = m.groups()
        if k not in KEYS:
            errs.append(f"{path}:{n}: {k} is not a key of deploy/run/config.env ({', '.join(KEYS)})")
        elif k in vals:
            errs.append(f"{path}:{n}: {k} is set twice")
        else:
            vals[k] = v
    for k, (required, rx, what) in KEYS.items():
        if k not in vals:
            errs.append(f"{path}: {k} is missing")
            continue
        v = vals[k]
        if v == PLACEHOLDER:
            if required:
                left.append(k)
                if not lint:
                    errs.append(f"{path}: {k} is still {PLACEHOLDER}: fill it in (a config PR) before a deploy")
            else:
                errs.append(f"{path}: {k} is {PLACEHOLDER}: leave it empty or set it ({what})")
            continue
        if v == "" and not required:
            continue
        if v == "":
            errs.append(f"{path}: {k} is empty: {what}")
        elif not re.fullmatch(rx, v):
            errs.append(f"{path}: {k}={v!r} does not have the shape {what}")
        elif k == "PRIVACY_POLICY_VERSION":
            try:
                datetime.date.fromisoformat(v)
            except ValueError:
                errs.append(f"{path}: PRIVACY_POLICY_VERSION={v} is not a calendar date (the services refuse it)")
    if (vals.get("OTLP_ENDPOINT", "") == "") != (vals.get("OTLP_USER", "") == ""):
        errs.append(f"{path}: OTLP_ENDPOINT and OTLP_USER are set together or both left empty")
    if errs:
        raise Finding("\n".join(errs))
    return vals, left


def values_for(name, cfg, digest):
    """The placeholder values of one template. migrate has no audience: it is no push target."""
    ref, host = cfg["SUPABASE_PROJECT_REF"], cfg["SUPABASE_POOLER_HOST"]
    v = {k: cfg[k] for k in VARS if k in cfg}
    v["IMAGE"] = f"{REGISTRY}/{name}@{digest}"
    if name == "migrate":
        v["DATABASE_URL"] = f"postgresql://postgres.{ref}@{host}:5432/postgres?sslmode=verify-full&sslrootcert={CA}"
    else:
        v["DATABASE_URL"] = (f"postgresql://ryft_{name}.{ref}@{host}:6543/postgres?sslmode=verify-full"
                             f"&sslrootcert={CA}&default_query_exec_mode=exec")
        v["PUSH_AUDIENCE"] = f"https://{name}-{cfg['GCP_PROJECT_NUMBER']}.{REGION}.run.app"
    return v


def template(templates, name):
    path = Path(templates) / f"{name}.yaml"
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as e:
        raise Finding(f"cannot read {path}: {e.strerror}")
    used = PLACE.findall(text)
    bad = sorted({u for u in used if u not in VARS})
    if bad:
        raise Finding(f"{path}: placeholder(s) outside the fixed list: {', '.join(bad)} (allowed: {', '.join(VARS)})")
    if "PUSH_AUDIENCE" in used and name == "migrate":
        raise Finding(f"{path}: migrate is no push target, it has no PUSH_AUDIENCE")
    rest = PLACE.sub("", text)
    if "$" in rest:
        raise Finding(f"{path}: a '$' that is not a ${{NAME}} placeholder (line {rest[:rest.index('$')].count(chr(10)) + 1})")
    return path, text


def render(text, path, vals):
    def sub(m):
        if m.group(1) not in vals:
            raise Finding(f"{path}: no value for ${{{m.group(1)}}}")
        return vals[m.group(1)]
    out = PLACE.sub(sub, text)
    if "$" in out:  # a value never holds one (the shapes above), so this is a bug, not an input
        raise Finding(f"{path}: a '$' is left after rendering")
    return out


def main(argv):
    p = argparse.ArgumentParser(description="Render deploy/run for gcloud run services/jobs replace.")
    p.add_argument("--config", default=str(ROOT / "deploy/run/config.env"))
    p.add_argument("--templates", default=str(ROOT / "deploy/run"))
    p.add_argument("--out")
    p.add_argument("--digest", action="append", default=[], metavar="SVC=sha256:HEX")
    p.add_argument("--lint", action="store_true")
    try:
        a = p.parse_args(argv)
    except SystemExit as e:
        return 2 if e.code else 0
    if a.lint == bool(a.out):
        say("give exactly one of --out <dir> (render) or --lint")
        return 2
    try:
        cfg, left = read_config(a.config, a.lint)
        tpl = {n: template(a.templates, n) for n in FILES}
        if a.lint:
            if a.digest:
                say("--lint takes no --digest")
                return 2
            note = f"; {PLACEHOLDER} left (fill before the first deploy): {', '.join(left)}" if left else ""
            print(f"cloudrun_render: {a.config} and {len(tpl)} templates lint clean{note}")
            return 0
        digests = {}
        for d in a.digest:
            name, sep, dg = d.partition("=")
            if not sep or name not in FILES:
                raise Finding(f"--digest {d!r}: want <{'|'.join(FILES)}>=sha256:<64 hex>")
            if name in digests:
                raise Finding(f"--digest {name} given twice")
            if not DIGEST.fullmatch(dg):
                raise Finding(f"--digest {name}: {dg!r} is not sha256:<64 lower-case hex>")
            digests[name] = dg
        missing = [n for n in FILES if n not in digests]
        if missing:
            raise Finding(f"no --digest for {', '.join(missing)}: every image is deployed by digest")
        out = Path(a.out)
        out.mkdir(parents=True, exist_ok=True)
        for n in FILES:
            path, text = tpl[n]
            (out / f"{n}.yaml").write_text(render(text, path, values_for(n, cfg, digests[n])), encoding="utf-8")
        print(f"cloudrun_render: {', '.join(f'{n}.yaml' for n in FILES)} in {out}")
        return 0
    except Finding as f:
        for line in str(f).split("\n"):
            say(line)
        return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
