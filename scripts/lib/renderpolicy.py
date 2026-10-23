"""Production invariants over what an Argo CD application RENDERS (check_argocd.py feeds it the output
of `kubectl kustomize <path>` for every application). The key and tag allowlists read the files; a
config.env edit, a generator `merge`, a nested patch or an included Application is only visible
here, so this is the backstop that does not care how the change was written.

For every application:
  - no object of the group argoproj.io (an Application or AppProject rendered by an app's path would
    be a second, unchecked app of apps; only deploy/argocd/apps/ holds those, and the schema reads them).
For applications of the project `ryft` (the workloads; the operators' project has its own images):
  - every `image:` anywhere is one of our six images with a 7-hex tag (ghcr.io/timkrebs/ryft/<name>:abcdef0),
    or the broker's image from docker.io/library/rabbitmq pinned by digest;
  - no ConfigMap / Secret key and no container env named ALLOW_INSECURE_APIKEY_AUTH or API_KEY, and
    every AUTH_MODE (ConfigMap key or env) is `supabase`; ryft-api and ryft-ingest, when they have
    workloads, have a ConfigMap that says AUTH_MODE=supabase;
  - every ryft-* namespace that holds a workload has the default-deny NetworkPolicy intact
    (podSelector {}, policyTypes Ingress and Egress, no ingress or egress rule); no NetworkPolicy has an
    empty rule (`{}` allows everything).
"""
import re

OUR_IMAGE = re.compile(r"^ghcr\.io/timkrebs/ryft/(api|ingest|scoring|notify|migrate|web):[0-9a-f]{7}$")
BROKER_IMAGE = re.compile(r"^docker\.io/library/rabbitmq:[0-9A-Za-z._-]+@sha256:[0-9a-f]{64}$")
FORBIDDEN_NAMES = {"ALLOW_INSECURE_APIKEY_AUTH", "API_KEY"}
WORKLOADS = {"Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "Pod", "ReplicaSet", "RabbitmqCluster"}
AUTH_NAMESPACES = ("ryft-api", "ryft-ingest")


class Renders:
    def __init__(self):
        self.errs = []
        self.workload_ns = set()
        self.deny_ns = set()
        self.auth_ok_ns = set()

    def err(self, app, msg):
        self.errs.append(f"{app}: {msg}")

    def add(self, app, project, docs):
        for o in docs:
            if not isinstance(o, dict):
                continue
            api, kind = str(o.get("apiVersion", "")), o.get("kind")
            who = f"{kind} {(o.get('metadata') or {}).get('namespace', '')}/{(o.get('metadata') or {}).get('name', '')}"
            if api.split("/")[0] == "argoproj.io":
                self.err(app, f"{who} (argoproj.io) is rendered by the application's path: Applications and projects belong in deploy/argocd only")
            if project != "ryft":
                continue
            ns = (o.get("metadata") or {}).get("namespace")
            if kind in WORKLOADS and ns:
                self.workload_ns.add(ns)
            self._walk(app, who, o)
            if kind in ("ConfigMap", "Secret"):
                for key in list((o.get("data") or {}).keys()) + list((o.get("stringData") or {}).keys()):
                    if key in FORBIDDEN_NAMES:
                        self.err(app, f"{who} has the key {key}: production does not run the shared API key")
                am = (o.get("data") or {}).get("AUTH_MODE")
                if kind == "ConfigMap" and am is not None:
                    if am == "supabase":
                        self.auth_ok_ns.add(ns)
                    else:
                        self.err(app, f"{who} sets AUTH_MODE={am!r}: production is supabase")
            if kind == "NetworkPolicy":
                self._netpol(app, who, ns, o.get("spec") or {})

    def _netpol(self, app, who, ns, spec):
        for direction in ("ingress", "egress"):
            for rule in spec.get(direction) or []:
                if not rule:
                    self.err(app, f"{who} has an empty {direction} rule, which allows everything")
        if spec.get("podSelector") == {} and set(spec.get("policyTypes") or []) == {"Ingress", "Egress"} \
                and "ingress" not in spec and "egress" not in spec:
            self.deny_ns.add(ns)

    def _walk(self, app, who, x):
        if isinstance(x, dict):
            n = x.get("name")
            if n in FORBIDDEN_NAMES and ("value" in x or "valueFrom" in x):
                self.err(app, f"{who} sets the environment variable {n}")
            if n == "AUTH_MODE" and "value" in x and x["value"] != "supabase":
                self.err(app, f"{who} sets AUTH_MODE={x['value']!r}: production is supabase")
            for k, v in x.items():
                if k == "image":
                    if not (isinstance(v, str) and (OUR_IMAGE.match(v) or BROKER_IMAGE.match(v))):
                        self.err(app, f"{who} runs the image {v!r}: only our six images with a 7-hex tag (or the broker's digest-pinned rabbitmq)")
                else:
                    self._walk(app, who, v)
        elif isinstance(x, list):
            for v in x:
                self._walk(app, who, v)

    def finish(self):
        for ns in sorted(self.workload_ns):
            if ns.startswith("ryft-") and ns not in self.deny_ns:
                self.errs.append(f"namespace {ns} has a workload but no intact default-deny NetworkPolicy (podSelector {{}}, Ingress and Egress, no rules)")
        for ns in AUTH_NAMESPACES:
            if ns in self.workload_ns and ns not in self.auth_ok_ns:
                self.errs.append(f"namespace {ns} has a workload but no ConfigMap with AUTH_MODE=supabase")
        return self.errs
