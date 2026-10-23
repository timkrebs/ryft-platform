"""The body of scripts/check_argocd.sh (see the header there)."""
import fnmatch
import os
import re
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import yamlpolicy  # noqa: E402

REPO = "https://github.com/timkrebs/ryft-platform.git"
APPS_DIR = "deploy/argocd/apps"
ROOT_PATH = APPS_DIR  # the one application whose path is a plain directory of manifests
bad, warn = [], []


def project_namespaces():
    """{project name: [destination namespace patterns]} from project.yaml, read by yq."""
    f = "deploy/argocd/project.yaml"
    for e in yamlpolicy.tag_findings(f):
        bad.append(f"{f}: {e}")
    out = subprocess.run(["yq", "-o=json", "-I0", ".", f], capture_output=True, text=True)
    if out.returncode != 0:
        bad.append(f"{f}: yq cannot read it: {out.stderr.strip()}")
        return {}
    projects = {}
    for d in yamlpolicy._json_stream(out.stdout):
        if isinstance(d, dict) and d.get("kind") == "AppProject":
            projects[d["metadata"]["name"]] = [x.get("namespace", "") for x in d.get("spec", {}).get("destinations", [])]
    return projects


def path_findings(path):
    """Why `path` is not a plain path below deploy/ (symlinks resolved)."""
    if not re.fullmatch(r"deploy/[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*", path or "") or ".." in path.split("/"):
        return [f"path {path!r} must be a plain relative path below deploy/"]
    cur = ""
    for part in path.split("/"):
        cur = os.path.join(cur, part)
        if os.path.islink(cur):
            return [f"path {path!r} goes through the symlink {cur}"]
    real = os.path.realpath(path)
    if not (real + os.sep).startswith(os.path.realpath("deploy") + os.sep):
        return [f"path {path!r} resolves outside deploy/"]
    return []


def main():
    projects = project_namespaces()
    # Argo's directory source applies *.yaml, *.yml, *.json and *.jsonnet from apps/ (the checks and the
    # kubeconform loop in the Makefile read *.yaml only): nothing else may be in that directory, and not
    # a kustomization.yaml (it would turn the directory into a kustomize app).
    for e in sorted(os.listdir(APPS_DIR)):
        full = os.path.join(APPS_DIR, e)
        if not (e.endswith(".yaml") and not e.startswith(".") and os.path.isfile(full) and not os.path.islink(full)):
            bad.append(f"{full}: apps/ holds only regular *.yaml files (Argo applies .yml, .json and .jsonnet there too, and nothing else here is checked)")
        elif e == "kustomization.yaml":
            bad.append(f"{full}: apps/ is a directory of plain manifests; a kustomization.yaml would turn it into a kustomize app")
    apps = sorted(f for f in (os.path.join(APPS_DIR, e) for e in os.listdir(APPS_DIR))
                  if f.endswith(".yaml") and os.path.isfile(f) and not os.path.islink(f) and not os.path.basename(f).startswith(".")
                  and os.path.basename(f) != "kustomization.yaml") + ["deploy/argocd/root.yaml"]
    checked = set()
    for f in apps:
        doc, errs = yamlpolicy.load(f, yamlpolicy.APPLICATION)
        bad.extend(f"{f}: {e}" for e in errs)
        if doc is None:
            continue
        spec = doc.get("spec", {})
        src, dest = spec.get("source", {}), spec.get("destination", {})
        name, project, path, dest_ns = doc.get("metadata", {}).get("name"), spec.get("project"), src.get("path"), dest.get("namespace", "")
        if doc.get("kind") != "Application" or not name or not src:
            bad.append(f"{f}: not an Application with a name and a spec.source")
            continue
        if src.get("repoURL") != REPO:
            bad.append(f"{f}: repoURL is not {REPO}")
        if project not in projects:
            bad.append(f"{f}: project {project} is not in project.yaml")
            continue
        if not any(fnmatch.fnmatchcase(dest_ns, p) for p in projects[project]):
            bad.append(f"{f}: destination namespace {dest_ns!r} is not admitted by project {project} ({projects[project]})")
        ann = doc.get("metadata", {}).get("annotations", {})
        if "/apps/" in f:
            if "argocd.argoproj.io/sync-wave" not in ann:
                bad.append(f"{f}: no sync wave")
            if ann.get("argocd.argoproj.io/compare-options") != "ServerSideDiff=true":
                bad.append(f"{f}: no server-side diff (StatefulSets drift without it)")
        pf = path_findings(path)
        if pf:
            bad.extend(f"{f}: {e}" for e in pf)
            continue
        if not os.path.isdir(path):
            (warn if path.startswith("deploy/k8s/overlays/vault-namespaces/") else bad).append(f"{f}: {path} does not exist")
            continue
        k = os.path.join(path, "kustomization.yaml")
        if path == ROOT_PATH:
            continue  # plain manifests, checked one by one above
        if os.path.islink(k) or not os.path.isfile(k):
            bad.append(f"{f}: {path} has no regular kustomization.yaml (Argo would apply the directory's raw files)")
            continue
        if path in checked:
            continue
        checked.add(path)
        r = subprocess.run(["./scripts/check_kustomize_refs.sh", path], capture_output=True, text=True)
        if r.returncode != 0:
            bad.append(f"{f}: the kustomizations under {path} fail scripts/check_kustomize_refs.sh: {(r.stderr or r.stdout).strip()[:600]}")
            continue
        r = subprocess.run(["kubectl", "kustomize", path], capture_output=True, text=True)
        if r.returncode != 0:
            bad.append(f"{f}: {path} does not render: {r.stderr.strip()[:200]}")
    for w in warn:
        print(f"! check_argocd: {w} (generated by the next infra/vault apply)")
    for b in bad:
        print(f"✗ check_argocd: {b}")
    if bad:
        return 1
    print(f"✓ {len(apps)} Argo CD applications: one spec.source each (no sources, kustomize, helm, plugin or directory overrides), this repository, a path below deploy/ whose kustomizations pass the reference check and render, their projects' namespaces")
    return 0


sys.exit(main())
