#!/usr/bin/env bash
# Writes the value of a local of infra/aws/kubernetes as JSON, evaluated by Terraform itself
# (the Argo CD chart's values are HCL: argocd.tf, local.argocd_values), offline:
#   scripts/lib/tf_values.sh <repo-tree> <local-name> <out.json>
#
# The tree is CODE. Terraform runs what it finds there, so this script limits what it can find and
# what the run can reach:
#   - only files git tracks are copied (tracked_copy.sh): never a tfplan, a tfvars, a .terraform
#     directory or any other ignored file; <repo-tree> must be a git checkout;
#   - the module is refused if scripts/lib/tf_scan.py finds in it (comments removed, strings blanked) a
#     provisioner, an `alias` (an aliased provider configuration is not mocked, so its exec,
#     credential_process or postrender would run), exec, config_path, shared_config_files,
#     shared_credentials_files, credential_process, postrender, local-exec, a data "external" or
#     data "http", or any *.tf.json (the one provisioner that runs is this script's own export
#     resource, added after the check); providers.tf is exempt only when it is byte-identical to this
#     checkout's (it holds the real `aws eks get-token` exec of the default providers, which the test
#     mocks); the same check runs on the modules `terraform init` downloads, and after init the lock
#     file may name only the module's own providers (hashicorp/aws, helm, kubernetes, random). This is
#     a TRIPWIRE for an accident or a lazy attack, not a refusal to rely on;
#   - terraform runs under `env -i` with a throwaway HOME and no cloud, Kubernetes or HCP Terraform
#     credential (no AWS_*, TF_TOKEN_*, KUBE*; the CLI config file is /dev/null). That does not stop a
#     program from reading ~/.aws or ~/.kube by absolute path: for that, TF_VALUES_CONTAINER=<image>
#     (a terraform image, pinned by digest, and docker or podman) runs init in a container that sees
#     only the work directory, and `terraform test` in it with --network none. Optional, never implied;
#   - the providers are the mocks of this checkout's tests/addons.tftest.hcl (its header, up to the
#     first `run`), not the tree's own test file; the tree's tests (*.tftest.hcl, *.tftest.json),
#     mocks (*.tfmock.*) and tfvars are dropped.
# Nothing is created anywhere. `terraform init -backend=false` needs the providers of the module's
# lock file (TF_PLUGIN_CACHE_DIR keeps them between runs; CI's `terraform` job warms the same cache).
# TF_PLUGIN_DIR=<dir> installs them from a local directory instead (init -plugin-dir). The mocked
# random_password gives a fixed bcrypt hash: the export is for the RBAC and the settings, never a
# secret. To an untrusted tree this is still a program you run: do it in the workflow, or on a
# machine without production credentials.
set -euo pipefail
tree=${1:?usage: tf_values.sh <repo-tree> <local-name> <out.json>}
name=${2:?usage: tf_values.sh <repo-tree> <local-name> <out.json>}
out=${3:?usage: tf_values.sh <repo-tree> <local-name> <out.json>}
case "$name" in *[!a-z0-9_]*|"") echo "tf_values: odd local name '$name'" >&2; exit 2 ;; esac
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$here/../.." && pwd)
# shellcheck source=./tracked_copy.sh
. "$here/tracked_copy.sh"
command -v python3 >/dev/null || { echo "tf_values: python3 is not installed (tf_scan.py reads the module)" >&2; exit 2; }
engine=""
if [ -n "${TF_VALUES_CONTAINER:-}" ]; then
  # Asked for a container: without an engine, stop (never fall back to the host silently).
  engine=$(command -v docker || command -v podman || true)
  [ -n "$engine" ] || { echo "tf_values: TF_VALUES_CONTAINER is set but neither docker nor podman is installed" >&2; exit 2; }
else
  command -v terraform >/dev/null || { echo "tf_values: terraform is not installed" >&2; exit 2; }
fi
out=$(cd "$(dirname "$out")" && pwd)/$(basename "$out")

# Terraform's file functions (file, templatefile, fileset, ...) read any file the user can read into a
# value. Where may a tree use them? On the host only when the tree is this checkout, or the file is
# byte-identical to this checkout's (below); in a container that sees only the work directory, and on a
# GitHub runner (no credentials of yours), yes. Any other tree on the host needs the container, or
# TF_VALUES_FORCE=1 (admission_e2e.sh --force), which says so and still refuses a changed file that uses them.
scan_opts=()
if [ -n "$engine" ]; then
  scan_opts=(--allow-files)
elif [ "${GITHUB_ACTIONS:-}" = true ]; then
  scan_opts=(--allow-files)
  echo "tf_values: notice: in GitHub Actions the file functions of the tree are allowed (the runner holds none of your credentials)" >&2
elif [ "$(cd "$tree" 2>/dev/null && pwd -P)" != "$(cd "$root" && pwd -P)" ]; then
  if [ "${TF_VALUES_FORCE:-}" = 1 ]; then
    echo "tf_values: !!!! $tree is not this checkout and no TF_VALUES_CONTAINER is set: its Terraform runs on this machine with your user's reach (--force)" >&2
  else
    echo "tf_values: $tree is not this checkout: set TF_VALUES_CONTAINER=<terraform image pinned by digest> (docker or podman) to evaluate its Terraform, or --force / FORCE=1 on a machine with no production credentials" >&2
    exit 2
  fi
fi

work=$(mktemp -d)
work=$(cd "$work" && pwd -P)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/home"
tracked_copy "$tree" "$work" deploy infra/aws/kubernetes || { echo "tf_values: could not copy the tracked files of $tree" >&2; exit 2; }
mod="$work/infra/aws/kubernetes"
[ -f "$mod/main.tf" ] || { echo "tf_values: $tree has no infra/aws/kubernetes/main.tf" >&2; exit 2; }
rm -rf "$mod/tests" "$mod/admission-celtest" "$mod/admission-e2e"
find "$mod" \( -name '*.tftest.hcl' -o -name '*.tftest.json' -o -name '*.tfmock.*' -o -name '*.tfvars' -o -name '*.tfvars.json' -o -name '*.tfvars.example' \) -type f -delete

# What the module may not carry (the tree's own files, then the downloaded modules'): tf_scan.py. The
# tree's providers.tf is skipped only when it is this checkout's, byte for byte.
trusted=()
if [ -f "$mod/providers.tf" ] && cmp -s "$mod/providers.tf" "$root/infra/aws/kubernetes/providers.tf"; then trusted=(--trusted "$mod/providers.tf"); fi
# A file that uses the file functions is exempt from that one check when it is this checkout's, byte for byte.
while IFS= read -r f; do
  if [ -f "$root/infra/aws/kubernetes/${f#"$mod"/}" ] && cmp -s "$f" "$root/infra/aws/kubernetes/${f#"$mod"/}"; then trusted+=(--files-ok "$f"); fi
done < <(find "$mod" -type f -name '*.tf')
refuse_hcl() { # <dir> <what>: exit 2 with the findings when tf_scan.py finds any
  local found rc=0
  found=$(python3 -I "$here/tf_scan.py" ${scan_opts[@]+"${scan_opts[@]}"} ${trusted[@]+"${trusted[@]}"} "$1") || rc=$?
  [ "$rc" != 0 ] || return 0
  printf '%s\n' "$found" >&2
  echo "tf_values: $2 holds something that can run a program or read a credential file (provisioner, alias, exec, config_path, shared_config_files, credential_process, postrender, local-exec, data \"external\"/\"http\"/\"local_file\", file functions, a .tf.json): refused (this script runs terraform on it)" >&2
  exit 2
}
refuse_hcl "$mod" "the tree's module"

# The mocks, from this checkout's own test (a test file of its own: terraform test runs the files of
# tests/ in the module). The header ends at the first `run` block.
header="$root/infra/aws/kubernetes/tests/addons.tftest.hcl"
[ -f "$header" ] || { echo "tf_values: $header (the mock providers) is missing" >&2; exit 2; }
mkdir -p "$mod/tests"
awk '/^run /{exit} {print}' "$header" > "$mod/tests/zz_export.tftest.hcl"
cat >> "$mod/tests/zz_export.tftest.hcl" <<'HCL'

run "export" {
  command = apply
}
HCL

cat > "$work/zz_export.tf.new" <<HCL
variable "zz_export_path" {
  type = string
}

resource "terraform_data" "zz_export" {
  provisioner "local-exec" {
    command = "printf '%s' \"\$EXPORT_VALUE\" > \"\$EXPORT_PATH\""
    environment = {
      EXPORT_VALUE = jsonencode(local.${name})
      EXPORT_PATH  = var.zz_export_path
    }
  }
}
HCL

# Terraform, with nothing of this machine's credentials. The module sources are fetched over https by
# git (the job-wide GIT_ALLOW_PROTOCOL=file of the other CI jobs would refuse them): env -i drops it.
# With TF_VALUES_CONTAINER=<image> it runs in a container (docker, else podman) that mounts only the
# work directory (so ~/.aws and ~/.kube do not exist there), with no capability and no new privileges;
# tf_net is that container's network: the default for init (providers and modules are downloaded),
# "none" for the test.
tf_net=bridge
export_file="$work/export.json"
tf() {
  local envs=(HOME="$work/home" TMPDIR="$work" KUBECONFIG=/dev/null AWS_CONFIG_FILE=/dev/null
    AWS_SHARED_CREDENTIALS_FILE=/dev/null AWS_EC2_METADATA_DISABLED=true TF_CLI_CONFIG_FILE=/dev/null
    TF_IN_AUTOMATION=true TF_INPUT=0 TF_VAR_zz_export_path="$export_file")
  local v
  if [ -n "$engine" ]; then
    local a=(run --rm --network "$tf_net" --cap-drop ALL --security-opt no-new-privileges --user "$(id -u):$(id -g)" -v "$work:$work" -w "$mod")
    for v in HTTPS_PROXY HTTP_PROXY NO_PROXY https_proxy http_proxy no_proxy; do
      [ -z "${!v:-}" ] || envs+=("$v=${!v}")
    done
    for v in "${envs[@]}"; do a+=(-e "$v"); done
    "$engine" "${a[@]}" "$TF_VALUES_CONTAINER" "$@"
    return
  fi
  envs+=(PATH="$PATH")
  # A version manager (tenv) keeps the binaries under the real home: point it there, nothing else of it.
  if [ -z "${TENV_ROOT:-}" ] && [ -d "${HOME:-/nonexistent}/.tenv" ]; then envs+=("TENV_ROOT=$HOME/.tenv"); fi
  for v in TENV_ROOT TF_PLUGIN_CACHE_DIR HTTPS_PROXY HTTP_PROXY NO_PROXY https_proxy http_proxy no_proxy SSL_CERT_FILE SSL_CERT_DIR LANG LC_ALL; do
    [ -z "${!v:-}" ] || envs+=("$v=${!v}")
  done
  ( cd "$mod" && env -i "${envs[@]}" terraform "$@" )
}

init_args=(-backend=false -input=false -no-color)
[ -z "${TF_PLUGIN_DIR:-}" ] || init_args+=(-plugin-dir="$TF_PLUGIN_DIR")
tf init "${init_args[@]}" >"$work/init.log" 2>&1 || { cat "$work/init.log" >&2; echo "tf_values: terraform init failed" >&2; exit 1; }

# After init: the modules it downloaded and the providers it locked.
if [ -d "$mod/.terraform/modules" ]; then
  trusted=() scan_opts=()
  refuse_hcl "$mod/.terraform/modules" "a downloaded module"
fi
unknown=$(sed -nE 's/^provider "([^"]+)".*/\1/p' "$mod/.terraform.lock.hcl" | grep -vxE 'registry\.terraform\.io/hashicorp/(aws|helm|kubernetes|random)' || true)
if [ -n "$unknown" ]; then
  echo "tf_values: the lock file names providers this check does not run: $unknown" >&2
  exit 2
fi

mv "$work/zz_export.tf.new" "$mod/zz_export.tf"
tf_net=none
tf test -no-color >"$work/test.log" 2>&1 || { cat "$work/test.log" >&2; echo "tf_values: terraform test failed" >&2; exit 1; }
[ -s "$export_file" ] || { cat "$work/test.log" >&2; echo "tf_values: nothing was written to $out" >&2; exit 1; }
cp "$export_file" "$out" || { echo "tf_values: could not write $out" >&2; exit 1; }
echo "tf_values: local.$name written to $out"
