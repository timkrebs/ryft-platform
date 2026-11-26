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
SMOKE_USER_ID (a lower-case UUID) and SMOKE_SA_ID (digits) are the smoke identity of api and ingest (D286), required like PUSH_SA_ID.
Fails closed: a FILL_ME_IN left in config.env (the config PR before the first deploy; the error names the key
and where its value comes from), a value of the
wrong shape, a missing, unknown or repeated key (OTLP_ENDPOINT and OTLP_USER among the unknown since
D236), an account twice in APP_ATTEST_EXEMPT_USERS, a provider in CONNECT_PROVIDERS twice, fake or not
one Cloud Run serves (D257; those two keys' values may be empty), a CONNECT_MIN_POLICY_VERSION that is no
calendar date or is after PRIVACY_POLICY_VERSION (D259; empty is the policy in force). --lint checks the same but lets FILL_ME_IN pass (and names what is left), so CI catches a typo
in a config PR before the deploy does.
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
UUID = r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"

# config.env: key -> (required, full-match regex, what it is). Order is the file's.
KEYS = {
    "PRIVACY_POLICY_VERSION": (True, r"[0-9]{4}-[0-9]{2}-[0-9]{2}", "a YYYY-MM-DD date"),
    "LOG_LEVEL": (True, r"debug|info|warn|error", "debug, info, warn or error"),
    # The beta gate (D87, D238), api and ingest only: lower case, as committed; unset is no option here,
    # so the mode in force is always the one in git, never the code's default.
    "BETA_GATE": (True, r"off|warn|on", "off, warn or on"),
    # App Attest (D245, D247), api and ingest only, as committed like the beta gate. The exempt accounts'
    # line is required like every key's, its value may be empty (required False: no FILL_ME_IN either);
    # an id twice is refused below. check_cloudrun.py holds the rules between keys and files (the ring's
    # pin under warn and on, off -> on against the base).
    "APP_ATTEST": (True, r"off|warn|on", "off, warn or on"),
    "APP_ATTEST_EXEMPT_USERS": (False, rf"{UUID}(?:,{UUID}){{0,4}}",
                                "empty, or 1 to 5 lower-case UUIDs, comma-separated, no spaces"),
    # The X-Forwarded-For entries our proxies append (D53, D240), api and ingest only: the edge limit's
    # client is that many entries from the right. Required, so the count in force is the one in git.
    "XFF_TRUSTED_HOPS": (True, r"[1-8]", "1 to 8, the proxies in front of api and ingest"),
    # Direct connections (D257, design §9): the providers api and ingest serve (the line required, its value
    # may be empty: phase 1), and Garmin's user cap (required, 0 until phase 2). Only CONNECT_ON_CLOUD_RUN
    # may be listed, each once; fake is compose only and refused by name below. check_cloudrun.py holds
    # the rules between keys and files (garmin only with its secret pin, its client id and a cap above 0).
    "CONNECT_PROVIDERS": (False, r"[a-z][a-z0-9]*(?:,[a-z][a-z0-9]*)*",
                          "empty, or a comma list of provider ids (garmin), each once, no spaces"),
    "CONNECT_GARMIN_USER_CAP": (True, r"0|[1-9][0-9]{0,6}", "0 or a whole number of accounts, no leading zero"),
    # The consent cutoff (D259), ingest only: the line required, its value empty (the policy in force) or a
    # calendar date not after PRIVACY_POLICY_VERSION (checked below, after every key's shape).
    "CONNECT_MIN_POLICY_VERSION": (False, r"[0-9]{4}-[0-9]{2}-[0-9]{2}",
                                   "empty, or a YYYY-MM-DD date not after PRIVACY_POLICY_VERSION"),
    "GCP_PROJECT_NUMBER": (True, r"[1-9][0-9]{5,19}", "the project number, digits only"),
    "PUSH_SA_ID": (True, r"[1-9][0-9]{11,24}", "ryft-push's unique ID, digits only"),
    "SUPABASE_PROJECT_REF": (True, r"[a-z]{20}", "the Supabase project ref, 20 lower-case letters"),
    "SUPABASE_POOLER_HOST": (True, r"aws-[0-9]+-eu-central-1\.pooler\.supabase\.com",
                             "aws-<n>-eu-central-1.pooler.supabase.com"),
    "APPLE_KEY_ID": (True, r"[A-Z0-9]{10}", "10 upper-case letters or digits"),
    "APNS_KEY_ID": (True, r"[A-Z0-9]{10}", "10 upper-case letters or digits"),
    # The production smoke's identity (D286), api and ingest only: the dedicated account's Supabase user id and
    # the unique ID of ryft-smoke, the `sub` of its Google ID tokens. Required like PUSH_SA_ID, no secret.
    "SMOKE_USER_ID": (True, UUID, "the smoke account's Supabase user id, a lower-case UUID"),
    "SMOKE_SA_ID": (True, r"[1-9][0-9]{11,24}", "ryft-smoke's unique ID, digits only"),
}
# Where a required key's value comes from, for the error of a FILL_ME_IN left (the hints of config.env's own comments).
GENERIC_WHERE = "fill it in (a config PR) before a deploy; the comment above the key in deploy/run/config.env says where its value comes from"
WHERE = {
    "GCP_PROJECT_NUMBER": "set it to `gcloud projects describe ryftlab-prod --format='value(projectNumber)'`",
    "PUSH_SA_ID": "set it to `terraform -chdir=infra/gcp output -raw push_sa_unique_id`",
    "SMOKE_SA_ID": "set it to `terraform -chdir=infra/gcp output -raw smoke_sa_unique_id`",
    "SMOKE_USER_ID": "set it to the dedicated smoke account's Supabase UUID, the one bound by scripts/sql/bind_smoke_account.sql",
    "SUPABASE_PROJECT_REF": "set it to the project ID in Supabase -> Project Settings -> General",
    "SUPABASE_POOLER_HOST": "set it to the transaction pooler's host in Supabase -> Connect (aws-<n>-eu-central-1.pooler.supabase.com)",
    "APPLE_KEY_ID": "set it to the Sign in with Apple key's ID in Apple Developer -> Keys",
    "APNS_KEY_ID": "set it to the APNs key's ID in Apple Developer -> Keys",
}
# The providers Cloud Run may serve (D257). fake is the compose-only test provider (CONNECT_FAKE=on).
CONNECT_ON_CLOUD_RUN = ("garmin",)
# The template placeholders, the only names ever substituted.
VARS = ("IMAGE", "DATABASE_URL", "PUSH_AUDIENCE", "PRIVACY_POLICY_VERSION", "LOG_LEVEL", "SUPABASE_PROJECT_REF",
        "PUSH_SA_ID", "APPLE_KEY_ID", "APNS_KEY_ID", "BETA_GATE", "XFF_TRUSTED_HOPS", "APP_ATTEST",
        "APP_ATTEST_EXEMPT_USERS", "CONNECT_PROVIDERS", "CONNECT_GARMIN_USER_CAP", "CONNECT_MIN_POLICY_VERSION",
        "SMOKE_USER_ID", "SMOKE_SA_ID")
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
                    errs.append(f"{path}: {k} is {PLACEHOLDER}: {WHERE.get(k, GENERIC_WHERE)}")
            else:
                errs.append(f"{path}: {k} is {PLACEHOLDER}: leave it empty or set it ({what})")
            continue
        if v == "" and not required:
            continue
        if v == "":
            errs.append(f"{path}: {k} is empty: {what}")
        elif not re.fullmatch(rx, v):
            errs.append(f"{path}: {k}={v!r} does not have the shape {what}")
        elif k == "APP_ATTEST_EXEMPT_USERS" and len(set(v.split(","))) != v.count(",") + 1:
            errs.append(f"{path}: APP_ATTEST_EXEMPT_USERS names an account twice: each id once")
        elif k == "CONNECT_PROVIDERS" and "fake" in v.split(","):
            errs.append(f"{path}: CONNECT_PROVIDERS names fake: the fake provider is compose only (CONNECT_FAKE=on, "
                        "which the services refuse on Cloud Run, D257)")
        elif k == "CONNECT_PROVIDERS" and set(v.split(",")) - set(CONNECT_ON_CLOUD_RUN):
            unknown = sorted(set(v.split(",")) - set(CONNECT_ON_CLOUD_RUN))
            errs.append(f"{path}: CONNECT_PROVIDERS names {', '.join(unknown)}: Cloud Run serves only "
                        f"{', '.join(CONNECT_ON_CLOUD_RUN)} (D257)")
        elif k == "CONNECT_PROVIDERS" and len(set(v.split(","))) != v.count(",") + 1:
            errs.append(f"{path}: CONNECT_PROVIDERS names a provider twice: each id once")
        elif k in ("PRIVACY_POLICY_VERSION", "CONNECT_MIN_POLICY_VERSION"):
            try:
                datetime.date.fromisoformat(v)
            except ValueError:
                errs.append(f"{path}: {k}={v} is not a calendar date (the services refuse it)")
    # D259: the cutoff never after the policy in force (ISO dates compare as strings); only when both are dates.
    cut, pol = vals.get("CONNECT_MIN_POLICY_VERSION", ""), vals.get("PRIVACY_POLICY_VERSION", "")
    date = re.compile(r"[0-9]{4}-[0-9]{2}-[0-9]{2}")
    if date.fullmatch(cut) and date.fullmatch(pol) and cut > pol:
        errs.append(f"{path}: CONNECT_MIN_POLICY_VERSION={cut} is after PRIVACY_POLICY_VERSION={pol}: the cutoff is "
                    "the oldest consent version still accepted, never a later one (D259)")
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
