"""The pipe end of scripts/vault_diff.sh: turns secrets into salted fingerprints and compares them.

Values arrive on stdin only (from curl or kubectl, through a pipe) and leave this process only as
HMAC-SHA256(salt, key NUL value) in hex; nothing here prints, logs or writes a value, and no error
message quotes the input. The salt is the first line of stdin (64 hex digits, random per run,
never in argv). Key names are not secret: they are printed JSON-escaped (ASCII, no spaces, no
control characters), so a key cannot move the cursor or fake a line.

  vault_diff.py services <overlays>       the services with generated VSO objects, one per line
  vault_diff.py objects <overlays> <svc>  "<namespace>\\t<vault path>\\t<Secret name>" per VaultStaticSecret
  vault_diff.py hcp-address <overlays>    the HCP address of the generated VaultConnections (none when
                                          all are in-cluster; exit 3 when they name more than one)
  vault_diff.py vault <side>              stdin: salt, a Vault kv-v2 answer, "\\n<HTTP status>" (curl -w);
                                          stdout: "<key>\\t<hash>" lines, or "#absent" (404, deleted)
  vault_diff.py secret                    stdin: salt, `kubectl get secret -o json` (empty: absent)
  vault_diff.py compare <svc> <obj> <A> <B>  stdin: A's lines, "#--", B's lines; prints one line per
                                          key; exit 1 when anything is not equal

Exit 3 with a one-line reason (never the input) when it cannot work.
"""
import base64
import hashlib
import hmac
import json
import os
import re
import sys


def die(msg):
    sys.stderr.write("vault_diff: %s\n" % msg)
    sys.exit(3)


# A traceback quotes source lines, not data, but its last line can carry a repr of the input
# (a JSONDecodeError does not; a KeyError on a value would): never show one.
sys.excepthook = lambda *a: die("internal error (%s)" % a[0].__name__)

SALT = re.compile(r"^[0-9a-f]{64}$")


def read_salt(stream):
    salt = stream.readline().rstrip("\n")
    if not SALT.match(salt):
        die("no salt on stdin")
    return bytes.fromhex(salt)


def show(key):
    # Injective (JSON escaping), ASCII only, a space escaped as well so columns stay columns.
    return json.dumps(key, ensure_ascii=True)[1:-1].replace(" ", "\\u0020")


def fingerprint(salt, key, value):
    return hmac.new(salt, key.encode("utf-8") + b"\0" + value, hashlib.sha256).hexdigest()


def vault_bytes(v):
    # What VSO writes into the Secret for a kv-v2 value: a string's bytes, anything else as JSON.
    if isinstance(v, str):
        return v.encode("utf-8")
    return json.dumps(v, separators=(",", ":"), sort_keys=True, ensure_ascii=False).encode("utf-8")


def emit(salt, pairs):
    for key, value in sorted(pairs):
        sys.stdout.write("%s\t%s\n" % (show(key), fingerprint(salt, key, value)))


def mode_vault(side):
    salt = read_salt(sys.stdin)
    raw = sys.stdin.read()
    body, _, code = raw.rpartition("\n")
    code = code.strip()
    where = {"hcp": "HCP Vault", "k8s": "the in-cluster Vault"}.get(side, side)
    if code == "404":
        print("#absent")
        return
    if code == "000" or not code:
        die("%s gave no answer (address, CA, the port-forward?)" % where)
    if code in ("401", "403"):
        die("%s refused the read (HTTP %s: the token, or its namespace)" % (where, code))
    if code != "200":
        die("%s answered HTTP %s" % (where, code))
    try:
        doc = json.loads(body)
    except ValueError:
        die("%s: the answer is not JSON" % where)
    data = (doc.get("data") or {}).get("data") if isinstance(doc, dict) else None
    if data is None:
        print("#absent")  # the latest version is deleted or destroyed
        return
    if not isinstance(data, dict):
        die("%s: the answer has no key/value map" % where)
    emit(salt, [(k, vault_bytes(v)) for k, v in data.items()])


def mode_secret():
    salt = read_salt(sys.stdin)
    raw = sys.stdin.read()
    if not raw.strip():
        print("#absent")  # kubectl --ignore-not-found
        return
    try:
        doc = json.loads(raw)
    except ValueError:
        die("the Secret is not JSON")
    if not isinstance(doc, dict) or doc.get("kind") != "Secret":
        die("kubectl did not return a Secret")
    data = doc.get("data") or {}
    pairs = []
    for k, v in data.items():
        try:
            pairs.append((k, base64.b64decode(v, validate=True)))
        except (ValueError, TypeError):
            die("the Secret's key %s is not base64" % show(k))
    emit(salt, pairs)


def parse(lines):
    absent, out = False, {}
    for line in lines:
        if not line:
            continue
        if line == "#absent":
            absent = True
            continue
        key, sep, h = line.partition("\t")
        if not sep or not re.match(r"^[0-9a-f]{64}$", h):
            die("a fingerprint line is malformed")
        out[key] = h
    return absent, out


def mode_compare(svc, obj, la, lb):
    text = sys.stdin.read().split("\n")
    if "#--" not in text:
        die("compare: no separator")
    i = text.index("#--")
    absent_a, a = parse(text[:i])
    absent_b, b = parse(text[i + 1:])
    bad = 0
    keys = sorted(set(a) | set(b))
    for key in keys:
        if key in a and key in b:
            status = "equal" if hmac.compare_digest(a[key], b[key]) else "differs"
        elif key in a:
            status = "missing in " + lb
        else:
            status = "missing in " + la
        bad += status != "equal"
        print("%s  %s  %s  %s" % (svc, obj, key, status))
    if not keys:
        if absent_a and absent_b:
            print("%s  %s  -  missing in %s and %s" % (svc, obj, la, lb))
            bad += 1
        elif absent_a or absent_b:
            print("%s  %s  -  missing in %s" % (svc, obj, la if absent_a else lb))
            bad += 1
    sys.exit(1 if bad else 0)


def vso_docs(overlays, svc):
    path = os.path.join(overlays, svc, "vault.yaml")
    try:
        text = open(path, encoding="utf-8").read()
    except OSError:
        die("no generated VSO objects for %s (%s)" % (svc, path))
    # The generator's own shape (infra/vault/templates/vso.yaml.tftpl): two-space indents.
    for doc in re.split(r"(?m)^---\s*$", text):
        if not re.search(r"(?m)^kind: VaultStaticSecret\s*$", doc):
            continue
        ns = re.search(r"(?m)^metadata:\n(?:  .*\n)*?  namespace: (\S+)\s*$", doc)
        vpath = re.search(r"(?m)^spec:\n(?:  .*\n)*?  path: (\S+)\s*$", doc)
        dest = re.search(r"(?m)^  destination:\n(?:    .*\n)*?    name: (\S+)\s*$", doc)
        if not (ns and vpath and dest):
            die("%s: a VaultStaticSecret without namespace, path or destination" % path)
        yield ns.group(1), vpath.group(1), dest.group(1)


NAME = re.compile(r"^[a-z0-9]([a-z0-9-]*[a-z0-9])?$")


def mode_objects(overlays, svc):
    found = list(vso_docs(overlays, svc))
    if not found:
        die("%s: no VaultStaticSecret" % svc)
    for ns, vpath, dest in found:
        for part in (ns, vpath, dest):
            if not NAME.match(part):
                die("%s: an unexpected name in the generated objects" % svc)
        print("%s\t%s\t%s" % (ns, vpath, dest))


def mode_services(overlays):
    try:
        names = sorted(os.listdir(overlays))
    except OSError:
        die("no directory %s" % overlays)
    for name in names:
        if NAME.match(name) and os.path.isfile(os.path.join(overlays, name, "vault.yaml")):
            print(name)


IN_CLUSTER = "https://vault.vault.svc:8200"


def mode_hcp_address(overlays):
    # The one address every generated VaultConnection that is not the in-cluster one points at.
    found = set()
    try:
        names = sorted(os.listdir(overlays))
    except OSError:
        die("no directory %s" % overlays)
    for name in names:
        path = os.path.join(overlays, name, "vault.yaml")
        if not (NAME.match(name) and os.path.isfile(path)):
            continue
        text = open(path, encoding="utf-8").read()
        for doc in re.split(r"(?m)^---\s*$", text):
            if not re.search(r"(?m)^kind: VaultConnection\s*$", doc):
                continue
            m = re.search(r"(?m)^spec:\n(?:  .*\n)*?  address: (\S+)\s*$", doc)
            if not m:
                die("%s: a VaultConnection without an address" % path)
            if m.group(1) != IN_CLUSTER:
                found.add(m.group(1))
    if len(found) > 1:
        die("the generated VaultConnections name %d HCP addresses" % len(found))
    for a in found:
        if not re.match(r"^https://[a-z0-9.-]+(:[0-9]{1,5})?$", a):
            die("the generated HCP address is not a plain https address")
        print(a)


def main(argv):
    if not argv:
        die("no mode")
    mode, args = argv[0], argv[1:]
    if mode == "vault" and len(args) == 1:
        mode_vault(args[0])
    elif mode == "secret" and not args:
        mode_secret()
    elif mode == "compare" and len(args) == 4:
        mode_compare(*args)
    elif mode == "objects" and len(args) == 2:
        mode_objects(*args)
    elif mode == "services" and len(args) == 1:
        mode_services(args[0])
    elif mode == "hcp-address" and len(args) == 1:
        mode_hcp_address(args[0])
    else:
        die("usage: vault_diff.py services|objects|hcp-address|vault|secret|compare …")


main(sys.argv[1:])
