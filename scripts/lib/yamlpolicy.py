"""Strict reading of the YAML that a deploy tool interprets, for check_kustomize_refs.sh and
check_argocd.sh (and the tags check of check_workflows.sh). Imported by path, not installed.

Why an ALLOWLIST. Kustomize reads field names case-insensitively with Unicode folding
(`Resources:`, `RESOURCES:`, a long s in `resources:`) and decodes `!!binary` keys and values, so
a denylist of "forbidden keys" is a list of the spellings someone has thought of. Here a
kustomization or an Argo CD application may hold exactly the keys, and the YAML tags, the
repository uses today; anything else is a finding, spelled however it is spelled:

  - every key is matched EXACTLY and case-sensitively against the schema below;
  - the only YAML tags are !!map !!seq !!str !!int !!bool !!null (so no !!binary, no custom tag,
    no float or timestamp), and `<<` (!!merge), anchors and aliases are refused by name;
  - exactly one document, a regular file (never a symlink), no duplicate key.

A new key is a change to the schema in this file, in the same PR as the use of it.

Everything is read by yq (mikefarah v4); without it nothing can be checked (callers exit 2).
"""
import json
import os
import subprocess

ALLOWED_TAGS = {"!!map", "!!seq", "!!str", "!!int", "!!bool", "!!null"}
TAG_WHY = {
    "!!merge": "a YAML merge key (<<) is read differently by yq, kustomize and Argo CD",
    "!!binary": "!!binary is decoded by kustomize: a key or value that reads differently than it looks",
    "": "an alias (*name); anchors and aliases are not used in these files",
}


# ---- the schema -------------------------------------------------------------------------------
class Str:
    pass


class Bool:
    pass


class Int:
    pass


class Lit:  # one exact string
    def __init__(self, *values):
        self.values = values


class Map:  # exactly these keys (all optional), each with its own spec
    def __init__(self, keys):
        self.keys = keys


class AnyMap:  # any keys, one spec for the values (labels, annotations)
    def __init__(self, value):
        self.value = value


class List:
    def __init__(self, item):
        self.item = item


def validate(v, spec, where, errs):
    if isinstance(spec, Map):
        if not isinstance(v, dict):
            errs.append(f"{where}: must be a mapping")
            return
        for k, x in v.items():
            if k not in spec.keys:
                errs.append(f"{where}: key {k!r} is not allowed here (allowed: {', '.join(sorted(spec.keys))}); "
                            "a new key is a change to scripts/lib/yamlpolicy.py in the same PR")
            else:
                validate(x, spec.keys[k], f"{where}.{k}" if where else k, errs)
    elif isinstance(spec, AnyMap):
        if not isinstance(v, dict):
            errs.append(f"{where}: must be a mapping")
            return
        for k, x in v.items():
            validate(x, spec.value, f"{where}.{k}", errs)
    elif isinstance(spec, List):
        if not isinstance(v, list):
            errs.append(f"{where}: must be a list")
            return
        for i, x in enumerate(v):
            validate(x, spec.item, f"{where}[{i}]", errs)
    elif isinstance(spec, Lit):
        if v not in spec.values:
            errs.append(f"{where}: must be {' or '.join(repr(x) for x in spec.values)}, not {v!r}")
    elif isinstance(spec, Str):
        if not isinstance(v, str):
            errs.append(f"{where}: must be a string (quote it), not {v!r}")
    elif isinstance(spec, Bool):
        if not isinstance(v, bool):
            errs.append(f"{where}: must be true or false, not {v!r}")
    elif isinstance(spec, Int):
        if isinstance(v, bool) or not isinstance(v, int):
            errs.append(f"{where}: must be an integer, not {v!r}")


_GENERATOR = Map({
    "name": Str(), "namespace": Str(), "behavior": Lit("merge", "replace", "create"),
    "envs": List(Str()), "literals": List(Str()),
    "options": Map({"labels": AnyMap(Str())}),
})

KUSTOMIZATION = Map({
    "apiVersion": Lit("kustomize.config.k8s.io/v1beta1"),
    "kind": Lit("Kustomization"),
    "namespace": Str(),
    "resources": List(Str()),
    "patches": List(Map({
        "path": Str(), "patch": Str(),
        "target": Map({"group": Str(), "kind": Str(), "name": Str(), "namespace": Str()}),
    })),
    "images": List(Map({"name": Str(), "newTag": Str()})),
    "labels": List(Map({"pairs": AnyMap(Str()), "includeSelectors": Bool()})),
    "configMapGenerator": List(_GENERATOR),
    "secretGenerator": List(_GENERATOR),
    "generatorOptions": Map({"disableNameSuffixHash": Bool()}),
})

# Argo CD: `spec.source` (one) only; no `sources`, no source.kustomize/helm/plugin/directory.
APPLICATION = Map({
    "apiVersion": Lit("argoproj.io/v1alpha1"),
    "kind": Lit("Application"),
    "metadata": Map({
        "name": Str(), "namespace": Lit("argocd"),
        "annotations": Map({
            "argocd.argoproj.io/compare-options": Lit("ServerSideDiff=true"),
            "argocd.argoproj.io/sync-wave": Str(),
        }),
    }),
    "spec": Map({
        "project": Str(),
        "source": Map({"repoURL": Str(), "targetRevision": Lit("main"), "path": Str()}),
        "destination": Map({"server": Lit("https://kubernetes.default.svc"), "namespace": Str()}),
        "syncPolicy": Map({
            "automated": Map({"prune": Bool(), "selfHeal": Bool()}),
            "syncOptions": List(Lit("ServerSideApply=true", "PruneLast=true", "CreateNamespace=false",
                                    "RespectIgnoreDifferences=true")),
            "retry": Map({
                "limit": Int(),
                "backoff": Map({"duration": Str(), "factor": Int(), "maxDuration": Str()}),
            }),
        }),
        "ignoreDifferences": List(Map({
            "group": Str(), "kind": Str(), "jsonPointers": List(Str()), "jqPathExpressions": List(Str()),
        })),
    }),
})


# ---- reading ----------------------------------------------------------------------------------
def _yq(args, path):
    r = subprocess.run(["yq", *args, path], capture_output=True, text=True)
    return r.returncode, r.stdout, r.stderr


def _json_stream(raw):
    def pairs(ps):  # yq prints a duplicate key twice; a JSON reader would keep one of them silently
        d = {}
        for k, v in ps:
            if k in d:
                raise ValueError(f"duplicate key {k!r}")
            d[k] = v
        return d

    dec, out, i = json.JSONDecoder(object_pairs_hook=pairs), [], 0
    while i < len(raw):
        if raw[i].isspace():
            i += 1
            continue
        o, i = dec.raw_decode(raw, i)
        out.append(o)
    return out


def tag_findings(path):
    """Findings about the YAML tags, anchors and aliases of a file, without the schema. Also usable
    for files that have no schema here (the workflows): [] when clean."""
    rc, out, err = _yq(["-o=json", "-I0", "[... | tag] | unique"], path)
    if rc != 0:
        return [f"yq cannot parse it: {err.strip()}"]
    try:
        tags = {t for arr in _json_stream(out) for t in arr}
    except ValueError as e:
        return [f"yq output unreadable: {e}"]
    errs = []
    for t in sorted(tags - ALLOWED_TAGS):
        errs.append(f"the YAML tag {t!r} is not allowed: {TAG_WHY.get(t, 'only !!map !!seq !!str !!int !!bool !!null are used here')}")
    rc, out, err = _yq(["-o=json", "-I0", '[... | select(anchor != "")] | length'], path)
    if rc == 0 and any(n for n in _json_stream(out)):
        errs.append("an anchor (&name) is not allowed here")
    return errs


def load(path, schema):
    """(document, findings). The document is None when the file cannot be read as one mapping."""
    if os.path.islink(path) or not os.path.isfile(path):
        return None, [f"{path} must be a regular file, not a symlink or anything else"]
    errs = tag_findings(path)
    if errs:
        return None, errs
    rc, out, err = _yq(["-o=json", "-I0", "."], path)
    if rc != 0 or err.strip():
        return None, [f"yq cannot read it cleanly: {err.strip()}"]
    try:
        docs = _json_stream(out)
    except ValueError as e:
        return None, [str(e)]
    if len(docs) != 1 or not isinstance(docs[0], dict):
        return None, [f"must be exactly one YAML document holding a mapping (found {len(docs)})"]
    errs = []
    validate(docs[0], schema, "", errs)
    return docs[0], errs
