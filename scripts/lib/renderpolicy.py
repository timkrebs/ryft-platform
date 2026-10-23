"""A SMALL set of production invariants over what an Argo CD application RENDERS (check_argocd.py feeds it
the output of `kubectl kustomize <path>` for every application). The key and tag allowlists read the
files; a config.env edit, a generator `merge`, a nested patch or an included Application is only visible
here, so this is a backstop that does not care how the change was written. It is NOT an admission policy.

Checked, for every application:
  - no object of the group argoproj.io (an Application or AppProject rendered by an app's path would be a
    second, unchecked app of apps; only deploy/argocd/apps/ holds those, and the schema reads them).
Checked, for applications of the project `ryft` (the workloads):
  - every `image:` anywhere is one of our six images with a 7-hex tag (ghcr.io/timkrebs/ryft/<name>:abcdef0),
    or a docker.io/library/rabbitmq image pinned by digest (compared by pattern, not against the pin);
  - authentication mode: no ConfigMap or Secret key (data decoded, stringData) and no container env named
    ALLOW_INSECURE_APIKEY_AUTH, API_KEY or USER_ID; every AUTH_MODE (ConfigMap, Secret, env) is
    `supabase`; no `valueFrom` for those names or AUTH_MODE; no `envFrom[].prefix` (a prefix turns a
    harmless key into one of them); the services in CONSENT_SERVICE_NAMESPACES, when they have workloads,
    have a ConfigMap that says AUTH_MODE=supabase;
  - every ryft-* namespace that holds a workload (with a metadata.namespace) has the default-deny
    NetworkPolicy intact (podSelector {}, policyTypes Ingress and Egress, no ingress or egress rule);
  - no NetworkPolicy rule that allows every peer: an empty rule, `from: []` / `to: []`, or a rule with
    only `ports`.
NOT checked (follow-up: an admission policy, docs/STATUS): pod security (privileged, host*, hostPath),
Namespace objects and Pod Security labels, which images run in which namespace, command and args, the
broker image beyond its pattern, NetworkPolicy rule shapes beyond the above (ipBlock, namespaceSelector),
workloads without metadata.namespace, Vault Secrets Operator objects (VaultConnection address,
transformation templates), the power of the operators project's cluster roles, and project.yaml itself
(it is read from the pull request).
"""
import base64
import re

OUR_IMAGE = re.compile(r"ghcr\.io/timkrebs/ryft/(api|ingest|scoring|notify|migrate|web):[0-9a-f]{7}")
BROKER_IMAGE = re.compile(r"docker\.io/library/rabbitmq:[0-9A-Za-z._-]+@sha256:[0-9a-f]{64}")
FORBIDDEN_NAMES = {"ALLOW_INSECURE_APIKEY_AUTH", "API_KEY", "USER_ID"}
NO_VALUE_FROM = FORBIDDEN_NAMES | {"AUTH_MODE"}
WORKLOADS = {"Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "Pod", "ReplicaSet", "RabbitmqCluster"}
CONSENT_SERVICE_NAMESPACES = ("ryft-api", "ryft-ingest")


def _secret_values(o):
    """{key: text} of a Secret: data decoded from base64, stringData as is."""
    out = dict((o.get("stringData") or {}))
    for k, v in (o.get("data") or {}).items():
        try:
            out.setdefault(k, base64.b64decode(v, validate=True).decode("utf-8", "replace"))
        except Exception:
            out.setdefault(k, "")
    return out


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
                values = _secret_values(o) if kind == "Secret" else dict(o.get("data") or {})
                for key, val in values.items():
                    if key in FORBIDDEN_NAMES:
                        self.err(app, f"{who} has the key {key}: production does not run the shared API key")
                    if key == "AUTH_MODE" and val != "supabase":
                        self.err(app, f"{who} sets AUTH_MODE={val!r}: production is supabase")
                if kind == "ConfigMap" and values.get("AUTH_MODE") == "supabase":
                    self.auth_ok_ns.add(ns)
            if kind == "NetworkPolicy":
                self._netpol(app, who, ns, o.get("spec") or {})

    def _netpol(self, app, who, ns, spec):
        for direction, peers in (("ingress", "from"), ("egress", "to")):
            for rule in spec.get(direction) or []:
                if not isinstance(rule, dict) or not rule:
                    self.err(app, f"{who} has an empty {direction} rule, which allows everything")
                elif not rule.get(peers):
                    self.err(app, f"{who} has an {direction} rule without peers ({peers} missing or empty): it allows every peer")
        if spec.get("podSelector") == {} and set(spec.get("policyTypes") or []) == {"Ingress", "Egress"} \
                and "ingress" not in spec and "egress" not in spec:
            self.deny_ns.add(ns)

    def _walk(self, app, who, x):
        if isinstance(x, dict):
            n = x.get("name")
            if n in NO_VALUE_FROM and "valueFrom" in x:
                self.err(app, f"{who} takes the environment variable {n} from valueFrom: set it as a plain value")
            if n in FORBIDDEN_NAMES and "value" in x:
                self.err(app, f"{who} sets the environment variable {n}")
            if n == "AUTH_MODE" and "value" in x and x["value"] != "supabase":
                self.err(app, f"{who} sets AUTH_MODE={x['value']!r}: production is supabase")
            for k, v in x.items():
                if k == "envFrom" and isinstance(v, list):
                    for ref in v:
                        if isinstance(ref, dict) and "prefix" in ref:
                            self.err(app, f"{who} uses envFrom with a prefix: a prefix renames the keys the checks look for")
                if k == "image":
                    if not (isinstance(v, str) and (OUR_IMAGE.fullmatch(v) or BROKER_IMAGE.fullmatch(v))):
                        self.err(app, f"{who} runs the image {v!r}: only our six images with a 7-hex tag (or a digest-pinned rabbitmq)")
                else:
                    self._walk(app, who, v)
        elif isinstance(x, list):
            for v in x:
                self._walk(app, who, v)

    def finish(self):
        for ns in sorted(self.workload_ns):
            if ns.startswith("ryft-") and ns not in self.deny_ns:
                self.errs.append(f"namespace {ns} has a workload but no intact default-deny NetworkPolicy (podSelector {{}}, Ingress and Egress, no rules)")
        for ns in CONSENT_SERVICE_NAMESPACES:
            if ns in self.workload_ns and ns not in self.auth_ok_ns:
                self.errs.append(f"namespace {ns} has a workload but no ConfigMap with AUTH_MODE=supabase")
        return self.errs
