#!/usr/bin/env python3
"""What deploy/run must hold before deploy.yml may apply it (CI's manifests job, make cloudrun-check).

  python3 -I scripts/check_cloudrun.py [--dir deploy/run]

It lints the committed config.env (FILL_ME_IN may be left; a wrong shape fails), then renders every
template with fake values of the right shape and fake digests (scripts/cloudrun_render.py, the
renderer deploy.yml uses), parses the result with yq (mikefarah v4) and asserts, per file:
  - design §3's limits: request timeout, concurrency, memory, max instances, 1 vCPU, min instances 0,
    request-based billing (cpu-throttling true) and startup CPU boost; the migrate job's 600 s, 512 MiB,
    one task and no retry (maxRetries 0: a retry is a second failed owner login, and failed logins
    feed Supavisor's circuit breaker);
  - ingress all on api and ingest, internal on scoring and notify; the service account
    ryft-<svc>@ryftlab-prod.iam.gserviceaccount.com; one port, h2c 8080; the startup and the liveness
    probe both on /health/live, the startup probe within 30 s (an instance must start while the
    database is down, so its tick logs result error; /health/ready is the uptime checks' target);
    all traffic to the latest revision;
  - XFF_TRUSTED_HOPS other than 1 only with ingress internal-and-cloud-load-balancing on api and ingest
    (D240: behind the load balancer; with ingress all a client reaches run.app and writes that entry);
  - the env exactly as ticket-b.md's tables (names and the values the renderer builds; api may add the
    pin of posthog-project-key, D231; api and ingest carry BETA_GATE, D238, and XFF_TRUSTED_HOPS, D240,
    from config.env), every
    secret pinned by a version number (never latest), each secret only in the services of the accessor
    table, and migrate's DB_PASSWORD_<SVC> pin equal to PGPASSWORD's in <svc>.yaml (design §4);
  - P5 (design §5.1): none of AUTH_MODE=apikey, ALLOW_INSECURE_APIKEY_AUTH, API_KEY, USER_ID,
    ALLOW_INSECURE_PUSH or PORT anywhere;
  - keys only from an allowlist (no envFrom, command, args, volumes, extra annotations such as
    default-url-disabled or invoker-iam-disabled), no anchors or aliases (as yq parses them: an `&`
    or `*` inside a value or a comment is text), merge keys or duplicate keys.
Exit 0 clean, 1 a finding, 2 it cannot run (no yq, no python3 renderer). The fake values are built
here at run time; none of them is a credential.
"""
import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
RENDER = ROOT / "scripts/cloudrun_render.py"
SERVICES = ("api", "ingest", "scoring", "notify")
FILES = SERVICES + ("migrate",)
PROJECT = "ryftlab-prod"
REGISTRY = f"europe-west1-docker.pkg.dev/{PROJECT}/ryft"
EXPECT = {  # design §3
    "api": dict(timeout=60, concurrency=40, memory="512Mi", max_scale="3", ingress="all"),
    "ingest": dict(timeout=120, concurrency=4, memory="1Gi", max_scale="2", ingress="all"),
    "scoring": dict(timeout=300, concurrency=4, memory="512Mi", max_scale="1", ingress="internal"),
    "notify": dict(timeout=60, concurrency=10, memory="256Mi", max_scale="1", ingress="internal"),
}
MIGRATE = dict(timeout=600, memory="512Mi")
# Plain env per file: the fixed values; None means "the renderer's value" (compared below).
SHARED = {"GCP_PROJECT": PROJECT, "PUBSUB_ENDPOINT": "https://europe-west1-pubsub.googleapis.com",
          "PUSH_AUDIENCE": None, "PUSH_SA_EMAIL": f"ryft-push@{PROJECT}.iam.gserviceaccount.com",
          "PUSH_SA_ID": None, "LOG_LEVEL": None, "DATABASE_URL": None}
# BETA_GATE (D238) and XFF_TRUSTED_HOPS (D240) are the rows ticket-b.md's tables lack, both from
# config.env, both in the two public services only: the beta gate's mode (scoring reads only the consent
# mode) and the proxies the edge limit trusts (scoring and notify are internal and run no edge limit).
PUBLIC = {"AUTH_MODE": "supabase", "SUPABASE_PROJECT_REF": None, "PRIVACY_POLICY_VERSION": None,
          "PRIVACY_POLICY_URL": "https://ryftlab.de/privacy", "BETA_GATE": None, "XFF_TRUSTED_HOPS": None}
# The only ingress under which a hop count other than 1 may run: then Cloud Run takes requests from the
# load balancer alone, which appends the entry a second hop trusts.
LB_INGRESS = "internal-and-cloud-load-balancing"
PLAIN = {
    "api": {**SHARED, **PUBLIC, "ADMIN_WEB_ORIGINS": "https://ryftlab.de", "APPLE_TEAM_ID": "M77SKHKG63",
            "APPLE_KEY_ID": None, "APPLE_CLIENT_ID": "dev.timkrebs.ryft"},
    "ingest": {**SHARED, **PUBLIC},
    "scoring": dict(SHARED),
    "notify": {**SHARED, "APNS_TOPIC": "dev.timkrebs.ryft", "APNS_KEY_ID": None, "APNS_TEAM_ID": "M77SKHKG63"},
    "migrate": {"DATABASE_URL": None, "LOG_LEVEL": None},
}
# Secret env per file: env name -> Secret Manager secret (ticket-b.md → Secrets). OTLP_TOKEN went with
# Grafana Cloud (D236): no file pins otlp-token any more.
SECRET_ENV = {
    "api": {"PGPASSWORD": "db-password-api", "SUPABASE_SERVICE_ROLE_KEY": "supabase-secret-key",
            "ADMIN_API_KEY": "admin-api-key", "APPLE_KEY_P8": "apple-key-p8", "DATA_ENCRYPTION_KEY": "data-key"},
    "ingest": {"PGPASSWORD": "db-password-ingest"},
    "scoring": {"PGPASSWORD": "db-password-scoring"},
    "notify": {"PGPASSWORD": "db-password-notify", "APNS_KEY_P8": "apns-key-p8"},
    "migrate": {"PGPASSWORD": "db-password-owner", **{f"DB_PASSWORD_{s.upper()}": f"db-password-{s}" for s in SERVICES}},
}
# Secret env a file may hold or leave out (env name -> secret), checked like the rest when present.
# POSTHOG_PROJECT_KEY (D231): without it api serves analytics null. Its pin comes once the secret has a
# version, because a revision that reads a missing version does not start and the deploy goes red.
OPTIONAL_SECRET_ENV = {"api": {"POSTHOG_PROJECT_KEY": "posthog-project-key"}}
# The only service accounts that can access each secret (ticket-b.md, the accessor table; infra/gcp/secrets.tf).
# otlp-token stays until its own removal PR (docs/OPERATIONS.md → GCP, step 11.6): unread, still bound.
ACCESSORS = {**{f"db-password-{s}": {s, "migrate"} for s in SERVICES}, "db-password-owner": {"migrate"},
             "supabase-secret-key": {"api"}, "admin-api-key": {"api"}, "apple-key-p8": {"api"},
             "apns-key-p8": {"notify"}, "data-key": {"api"}, "otlp-token": set(SERVICES),
             "posthog-project-key": {"api"}}
P5 = ("ALLOW_INSECURE_APIKEY_AUTH", "API_KEY", "USER_ID", "ALLOW_INSECURE_PUSH", "PORT")
P5_RE = re.compile(r"(?<![A-Za-z0-9_])(" + "|".join(P5) + r")(?![A-Za-z0-9_])")
VERSION = re.compile(r"[1-9][0-9]*")
SVC_ANN = {"autoscaling.knative.dev/minScale", "autoscaling.knative.dev/maxScale",
           "run.googleapis.com/cpu-throttling", "run.googleapis.com/startup-cpu-boost"}
PROBE_KEYS = {"httpGet", "initialDelaySeconds", "periodSeconds", "timeoutSeconds", "failureThreshold"}
# The startup probe reads /health/live and gives up after this many seconds: a process that does not
# listen by then will not, and every failed start is billed (min instances 0 starts one per request).
STARTUP_BUDGET = 30

errs = []


def fail(where, msg):
    errs.append(f"{where}: {msg}")


def keys_exactly(where, obj, want, optional=()):
    if not isinstance(obj, dict):
        fail(where, f"is not a mapping ({type(obj).__name__})")
        return False
    extra = sorted(set(obj) - set(want) - set(optional))
    missing = sorted(set(want) - set(obj))
    if extra:
        fail(where, f"keys not allowed here: {', '.join(extra)} (scripts/check_cloudrun.py holds the allowlist)")
    if missing:
        fail(where, f"missing: {', '.join(missing)}")
    return not extra and not missing


def fakes(committed):
    """A config.env with every value replaced by a fake of the right shape (built here, at run time)."""
    digits = "".join(str(i % 10) for i in range(1, 22))
    v = {"GCP_PROJECT_NUMBER": digits[:12], "PUSH_SA_ID": digits, "SUPABASE_PROJECT_REF": "q" * 20,
         "SUPABASE_POOLER_HOST": "aws-0-eu-central-1.pooler.supabase.com", "APPLE_KEY_ID": "A" * 10,
         "APNS_KEY_ID": "N" * 10, "LOG_LEVEL": "info"}
    m = re.search(r"^PRIVACY_POLICY_VERSION=(.*)$", committed, re.M)
    v["PRIVACY_POLICY_VERSION"] = m.group(1) if m else "2026-01-01"
    # The committed gate mode (the lint before this has checked its shape), so the render is what deploys.
    m = re.search(r"^BETA_GATE=(.*)$", committed, re.M)
    v["BETA_GATE"] = m.group(1) if m else "off"
    # The committed hop count likewise: check_service compares it with the ingress.
    m = re.search(r"^XFF_TRUSTED_HOPS=(.*)$", committed, re.M)
    v["XFF_TRUSTED_HOPS"] = m.group(1) if m else "1"
    return v


def jsons(raw, dups):
    """Every JSON value yq printed (one per YAML document); a key twice in one object goes to dups."""
    def pairs(ps):
        d = {}
        for k, val in ps:
            if k in d:
                dups.append(k)
            d[k] = val
        return d
    dec = json.JSONDecoder(object_pairs_hook=pairs)
    i, out = 0, []
    while i < len(raw):
        if raw[i].isspace():
            i += 1
            continue
        obj, i = dec.raw_decode(raw, i)
        out.append(obj)
    return out


def yq_json(yq, expr, path):
    r = subprocess.run([yq, "-o=json", "-I=0", expr, str(path)], capture_output=True, text=True)
    if r.returncode != 0:
        fail(path.name, f"yq cannot parse it: {r.stderr.strip()}")
        return None
    dups = []
    try:
        out = jsons(r.stdout, dups)
    except json.JSONDecodeError as e:
        fail(path.name, f"yq printed no JSON ({e})")
        return None
    for k in dups:
        fail(path.name, f"the key {k!r} appears twice in one mapping (gcloud keeps one of them)")
    return out


def yq_print(yq, expr, path):
    """yq's YAML print of the document after expr, comments included; None when yq fails."""
    r = subprocess.run([yq, "-o=yaml", expr, str(path)], capture_output=True, text=True)
    return r.stdout if r.returncode == 0 else None


def anchor_names(yq, path):
    """The anchors yq parsed (`anchor` is a node's own, "" for none), for the message only."""
    r = subprocess.run([yq, "-o=json", "-I=0", '[... | select(anchor != "") | anchor] | unique', str(path)],
                       capture_output=True, text=True)
    try:
        names = json.loads(r.stdout) if r.returncode == 0 else []
    except json.JSONDecodeError:
        return []
    return [n for n in names if isinstance(n, str)] if isinstance(names, list) else []


def load(yq, path):
    """One YAML document as JSON, refusing anchors, aliases, merge keys, custom tags and duplicates."""
    docs = yq_json(yq, ".", path)
    if docs is None:
        return None
    if len(docs) != 1:
        fail(path.name, f"{len(docs)} YAML documents: exactly one")
        return None
    # `...` visits the keys too: a merge key (`<<`, tag !!merge) is a key, and `..` would miss it.
    tags = yq_json(yq, "[... | tag] | unique", path) or [[]]
    bad = sorted({t for t in tags[0] if t not in ("!!map", "!!seq", "!!str", "!!int", "!!bool", "!!null")})
    if bad:
        fail(path.name, f"YAML tags not allowed: {', '.join(bad)} (merge keys, binary and custom tags)")
    # Anchors and aliases are events of the YAML parser, not characters: the `&` of DATABASE_URL's query
    # string, or `*.run.app` in a quoted value or a comment, is text. explode() replaces every alias with
    # what it names, resolves merge keys and drops every anchor, and touches nothing else, so a document
    # without any prints the same before and after it. (yq's `alias` is no test: on a scalar it returns
    # the scalar's value, so `alias != ""` held for every template. An alias without its anchor does not
    # parse at all: yq_json above has reported it.)
    as_read, exploded = yq_print(yq, ".", path), yq_print(yq, "explode(.)", path)
    if as_read is None or exploded is None or as_read != exploded:
        names = anchor_names(yq, path)
        fail(path.name, "anchors and aliases are not allowed (gcloud and yq may read them differently)"
             + (f": {', '.join('&' + n for n in names)}" if names else ""))
    return docs[0]


def check_env(name, env, want_plain, cfg):
    """The env list: names unique, plain values exact, secrets by number. Returns {env: (secret, version)}."""
    where = f"{name}.yaml env"
    if not isinstance(env, list):
        fail(where, "is not a list")
        return {}
    seen, plain, secrets = set(), {}, {}
    for e in env:
        if not isinstance(e, dict) or not isinstance(e.get("name"), str):
            fail(where, f"an entry without a name: {e!r}")
            continue
        n = e["name"]
        if n in seen:
            fail(where, f"{n} is set twice (Cloud Run keeps one, which one is not ours to guess)")
        seen.add(n)
        if P5_RE.fullmatch(n):
            fail(where, f"{n} is never set on Cloud Run (P5, design §5.1; PORT is Cloud Run's own)")
        if "value" in e and "valueFrom" not in e and set(e) == {"name", "value"}:
            if not isinstance(e["value"], str):
                fail(where, f"{n}: the value must be a string, not {type(e['value']).__name__}")
            plain[n] = e["value"]
        elif set(e) == {"name", "valueFrom"} and isinstance(e["valueFrom"], dict) and set(e["valueFrom"]) == {"secretKeyRef"}:
            ref = e["valueFrom"]["secretKeyRef"]
            if not keys_exactly(f"{where} {n} secretKeyRef", ref, ("name", "key")):
                continue
            ver = ref["key"]
            if not isinstance(ver, str) or not VERSION.fullmatch(ver):
                fail(where, f"{n} pins {ref['name']} at {ver!r}: a version number, never latest (design §4)")
            secrets[n] = (ref["name"], ver)
        else:
            fail(where, f"{n}: only {{name, value}} or {{name, valueFrom: {{secretKeyRef: {{name, key}}}}}}")
    if plain.get("AUTH_MODE", "supabase") != "supabase":
        fail(where, f"AUTH_MODE={plain['AUTH_MODE']}: on Cloud Run only supabase (P5)")
    for n in sorted(set(plain) - set(want_plain)):
        fail(where, f"{n} is not in ticket-b.md's env table for {name}")
    for n in sorted(set(want_plain) - set(plain)):
        fail(where, f"{n} is missing (ticket-b.md's env table)")
    for n, want in want_plain.items():
        if n in plain:
            exp = want if want is not None else cfg[n]
            if plain[n] != exp:
                fail(where, f"{n}={plain[n]!r}, want {exp!r}")
    want_sec, may_sec = SECRET_ENV[name], OPTIONAL_SECRET_ENV.get(name, {})
    known = {**may_sec, **want_sec}
    for n in sorted(set(secrets) - set(known)):
        fail(where, f"{n} from {secrets[n][0]} is not in ticket-b.md's secret table for {name}")
    for n in sorted(set(want_sec) - set(secrets)):
        fail(where, f"{n} (secret {want_sec[n]}) is missing")
    for n, (sec, _) in secrets.items():
        if n in known and sec != known[n]:
            fail(where, f"{n} reads secret {sec}, want {known[n]}")
        if name not in ACCESSORS.get(sec, set()):
            fail(where, f"{name} reads {sec}, which only {', '.join(sorted(ACCESSORS.get(sec, set()))) or 'nobody'} can access")
    return secrets


def check_probe(where, probe, path, startup):
    if not keys_exactly(where, probe, ("httpGet", "periodSeconds", "timeoutSeconds", "failureThreshold"),
                        optional=("initialDelaySeconds",)):
        return
    hg = probe["httpGet"]
    if not isinstance(hg, dict) or hg.get("path") != path or hg.get("port", 8080) != 8080 or set(hg) - {"path", "port"}:
        fail(where, f"httpGet must be {{path: {path}, port: 8080}}, is {hg!r}")
    nums = {k: probe.get(k, 0) for k in PROBE_KEYS - {"httpGet"}}
    if not all(type(x) is int and x >= 0 for x in nums.values()):
        fail(where, "the timings must be whole seconds")
    elif nums["timeoutSeconds"] < 1 or nums["timeoutSeconds"] > nums["periodSeconds"] or nums["failureThreshold"] < 1:
        fail(where, "1 <= timeoutSeconds <= periodSeconds, failureThreshold >= 1")
    elif startup and nums["initialDelaySeconds"] + nums["periodSeconds"] * nums["failureThreshold"] > STARTUP_BUDGET:
        # Cloud Run's own limit is 240 s; ours is the short budget of a probe that waits only for the port.
        fail(where, f"initialDelaySeconds + periodSeconds * failureThreshold must stay <= {STARTUP_BUDGET} "
                    "(the startup probe waits for /health/live only; each failed start is billed)")


def check_service(name, doc, cfg, digest):
    e = EXPECT[name]
    w = f"{name}.yaml"
    if not keys_exactly(w, doc, ("apiVersion", "kind", "metadata", "spec")):
        return {}
    if doc["apiVersion"] != "serving.knative.dev/v1" or doc["kind"] != "Service":
        fail(w, "must be a serving.knative.dev/v1 Service")
    md = doc["metadata"]
    if keys_exactly(f"{w} metadata", md, ("name", "annotations")):
        if md["name"] != name:
            fail(w, f"metadata.name is {md['name']!r}, want {name!r} (the file's name)")
        if keys_exactly(f"{w} metadata.annotations", md["annotations"], ("run.googleapis.com/ingress",)):
            ingress = md["annotations"]["run.googleapis.com/ingress"]
            if ingress != e["ingress"]:
                fail(w, f"ingress {ingress!r}, want {e['ingress']!r}")
            hops = cfg.get("XFF_TRUSTED_HOPS", "1")
            if "XFF_TRUSTED_HOPS" in PLAIN[name] and hops != "1" and ingress != LB_INGRESS:
                fail(w, f"XFF_TRUSTED_HOPS={hops} with ingress {ingress!r}: a count above 1 trusts the load balancer's "
                        f"entry, so it needs ingress {LB_INGRESS!r} (D240); with any other a client reaches run.app "
                        "directly and writes the entry the edge limit would take")
    spec = doc["spec"]
    if not keys_exactly(f"{w} spec", spec, ("template", "traffic")):
        return {}
    if spec["traffic"] != [{"percent": 100, "latestRevision": True}]:
        fail(w, "traffic must be [{percent: 100, latestRevision: true}] (a deploy ends a rollback's pin)")
    t = spec["template"]
    if not keys_exactly(f"{w} template", t, ("metadata", "spec")):
        return {}
    if keys_exactly(f"{w} template.metadata", t["metadata"], ("annotations",)) and \
            keys_exactly(f"{w} template annotations", t["metadata"]["annotations"], SVC_ANN):
        ann = t["metadata"]["annotations"]
        want = {"autoscaling.knative.dev/minScale": "0", "autoscaling.knative.dev/maxScale": e["max_scale"],
                "run.googleapis.com/cpu-throttling": "true", "run.googleapis.com/startup-cpu-boost": "true"}
        for k, v in want.items():
            if ann[k] != v:
                fail(w, f"{k}: {ann[k]!r}, want {v!r} (design §3)")
    s = t["spec"]
    if not keys_exactly(f"{w} template.spec", s, ("serviceAccountName", "containerConcurrency", "timeoutSeconds", "containers")):
        return {}
    if s["serviceAccountName"] != f"ryft-{name}@{PROJECT}.iam.gserviceaccount.com":
        fail(w, f"runs as {s['serviceAccountName']!r}, want ryft-{name}@{PROJECT}.iam.gserviceaccount.com")
    if s["containerConcurrency"] != e["concurrency"]:
        fail(w, f"concurrency {s['containerConcurrency']!r}, want {e['concurrency']} (design §3)")
    if s["timeoutSeconds"] != e["timeout"]:
        fail(w, f"request timeout {s['timeoutSeconds']!r}, want {e['timeout']} (design §3)")
    cs = s["containers"]
    if not isinstance(cs, list) or len(cs) != 1:
        fail(w, "exactly one container")
        return {}
    c = cs[0]
    if not keys_exactly(f"{w} container", c, ("name", "image", "ports", "resources", "startupProbe", "livenessProbe", "env")):
        return {}
    if c["name"] != name:
        fail(w, f"the container is named {c['name']!r}, want {name!r}")
    if c["image"] != f"{REGISTRY}/{name}@{digest}":
        fail(w, f"image {c['image']!r}: want {REGISTRY}/{name}@<digest> (deployed by digest)")
    if c["ports"] != [{"name": "h2c", "containerPort": 8080}]:
        fail(w, f"ports must be exactly [{{name: h2c, containerPort: 8080}}], are {c['ports']!r}")
    if c["resources"] != {"limits": {"cpu": "1", "memory": e["memory"]}}:
        fail(w, f"resources {c['resources']!r}, want limits cpu \"1\", memory {e['memory']} (design §3)")
    check_probe(f"{w} startupProbe", c["startupProbe"], "/health/live", True)
    check_probe(f"{w} livenessProbe", c["livenessProbe"], "/health/live", False)
    return check_env(name, c["env"], PLAIN[name], cfg)


def check_migrate(doc, cfg, digest):
    w = "migrate.yaml"
    if not keys_exactly(w, doc, ("apiVersion", "kind", "metadata", "spec")):
        return {}
    if doc["apiVersion"] != "run.googleapis.com/v1" or doc["kind"] != "Job":
        fail(w, "must be a run.googleapis.com/v1 Job")
    if doc["metadata"] != {"name": "migrate"}:
        fail(w, "metadata must be exactly {name: migrate}")
    try:
        ex = doc["spec"]["template"]["spec"]
        task = ex["template"]["spec"]
    except (KeyError, TypeError):
        fail(w, "spec.template.spec.template.spec is missing")
        return {}
    if not keys_exactly(f"{w} spec", doc["spec"], ("template",)) or \
            not keys_exactly(f"{w} spec.template", doc["spec"]["template"], ("spec",)):
        return {}
    if keys_exactly(f"{w} execution", ex, ("parallelism", "taskCount", "template")):
        if ex["parallelism"] != 1 or ex["taskCount"] != 1:
            fail(w, "one task, parallelism 1 (design §3)")
    if not keys_exactly(f"{w} task", task, ("serviceAccountName", "timeoutSeconds", "maxRetries", "containers")):
        return {}
    if task["serviceAccountName"] != f"ryft-migrate@{PROJECT}.iam.gserviceaccount.com":
        fail(w, f"runs as {task['serviceAccountName']!r}, want ryft-migrate@{PROJECT}.iam.gserviceaccount.com")
    if task["timeoutSeconds"] != MIGRATE["timeout"]:
        fail(w, f"task timeout {task['timeoutSeconds']!r}, want {MIGRATE['timeout']} (design §3)")
    if type(task["maxRetries"]) is not int or task["maxRetries"] != 0:
        fail(w, f"maxRetries {task['maxRetries']!r}, want 0: a retry is a second failed owner login, and failed logins "
                "trip Supavisor's circuit breaker for the role")
    cs = task["containers"]
    if not isinstance(cs, list) or len(cs) != 1:
        fail(w, "exactly one container")
        return {}
    c = cs[0]
    if not keys_exactly(f"{w} container", c, ("name", "image", "resources", "env")):
        return {}
    if c["name"] != "migrate" or c["image"] != f"{REGISTRY}/migrate@{digest}":
        fail(w, f"container migrate with image {REGISTRY}/migrate@<digest>, is {c['name']!r} {c['image']!r}")
    if c["resources"] != {"limits": {"cpu": "1", "memory": MIGRATE["memory"]}}:
        fail(w, f"resources {c['resources']!r}, want limits cpu \"1\", memory {MIGRATE['memory']} (design §3)")
    return check_env("migrate", c["env"], PLAIN["migrate"], cfg)


def main(argv):
    p = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    p.add_argument("--dir", default=str(ROOT / "deploy/run"))
    a = p.parse_args(argv)
    d = Path(a.dir)
    yq = shutil.which("yq")
    ver = subprocess.run([yq, "--version"], capture_output=True, text=True) if yq else None
    if not ver or "mikefarah" not in (ver.stdout + ver.stderr):
        print("check_cloudrun: yq (mikefarah, v4) is not installed, so the rendered YAML cannot be parsed. CI installs "
              "the pinned release (scripts/install_yq.sh); locally: brew install yq", file=sys.stderr)
        return 2
    want = {f"{n}.yaml" for n in FILES} | {"config.env"}
    have = {f.name for f in d.iterdir() if not f.name.startswith(".")} if d.is_dir() else set()
    for f in sorted(have - want):
        fail(f"{d}/{f}", "not a file deploy/run may hold (the five templates and config.env); nothing renders it")
    for f in sorted(want - have):
        fail(f"{d}/{f}", "missing")
    if errs:
        return report()
    # P5 in config.env, by name (the renderer refuses unknown keys too, with a less specific message).
    for n, line in enumerate((d / "config.env").read_text(encoding="utf-8").split("\n"), 1):
        if not line.startswith("#") and (P5_RE.search(line.split("=", 1)[0]) or re.match(r"AUTH_MODE=", line)):
            fail(f"config.env:{n}", f"{line.split('=', 1)[0]} is never set on Cloud Run (P5, design §5.1)")
    lint = subprocess.run([sys.executable, "-I", str(RENDER), "--lint", "--config", str(d / "config.env"),
                           "--templates", str(d)], capture_output=True, text=True)
    if lint.returncode != 0:
        fail("config.env / templates", (lint.stderr or lint.stdout).strip())
        return report()
    committed = (d / "config.env").read_text(encoding="utf-8")
    cfg = fakes(committed)
    pn, ref, host = cfg["GCP_PROJECT_NUMBER"], cfg["SUPABASE_PROJECT_REF"], cfg["SUPABASE_POOLER_HOST"]
    digests = {n: "sha256:" + format(i + 1, "x") * 64 for i, n in enumerate(FILES)}
    with tempfile.TemporaryDirectory() as tmp:
        fake = Path(tmp) / "config.env"
        fake.write_text("".join(f"{k}={v}\n" for k, v in cfg.items()), encoding="utf-8")
        out = Path(tmp) / "out"
        r = subprocess.run([sys.executable, "-I", str(RENDER), "--config", str(fake), "--templates", str(d),
                            "--out", str(out)] + [f"--digest={n}={g}" for n, g in digests.items()],
                           capture_output=True, text=True)
        if r.returncode != 0:
            fail("render", (r.stderr or r.stdout).strip())
            return report()
        pins = {}
        for n in FILES:
            per = dict(cfg)
            if n == "migrate":
                per["DATABASE_URL"] = (f"postgresql://postgres.{ref}@{host}:5432/postgres"
                                       "?sslmode=verify-full&sslrootcert=/etc/ryft/supabase-ca.crt")
            else:
                per["DATABASE_URL"] = (f"postgresql://ryft_{n}.{ref}@{host}:6543/postgres?sslmode=verify-full"
                                       "&sslrootcert=/etc/ryft/supabase-ca.crt&default_query_exec_mode=exec")
                per["PUSH_AUDIENCE"] = f"https://{n}-{pn}.europe-west1.run.app"
            doc = load(yq, out / f"{n}.yaml")
            if doc is None:
                continue
            pins[n] = check_migrate(doc, per, digests[n]) if n == "migrate" else check_service(n, doc, per, digests[n])
    # design §4: a role's pin in migrate equals the service's own, or the next deploy sets the role back.
    for s in SERVICES:
        mine, theirs = pins.get(s, {}).get("PGPASSWORD"), pins.get("migrate", {}).get(f"DB_PASSWORD_{s.upper()}")
        if mine and theirs and mine[1] != theirs[1]:
            fail(f"{s}.yaml / migrate.yaml", f"db-password-{s} is pinned at {mine[1]} in {s}.yaml and at {theirs[1]} in "
                 f"migrate.yaml: move both in one commit (scripts/rotate_db_password.sh {s} <version>)")
    return report()


def report():
    gha = os.environ.get("GITHUB_ACTIONS") == "true"
    for e in errs:
        print(f"{'::error title=deploy/run::' if gha else '✗ '}{e}", file=sys.stderr)
    if errs:
        return 1
    print("check_cloudrun: api, ingest, scoring, notify and migrate render; limits, ingress, accounts, h2c, probes, env, "
          "secret pins (by number, migrate = the services), accessors and P5 hold")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
