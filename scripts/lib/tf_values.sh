#!/usr/bin/env bash
# Writes the value of a local of infra/aws/kubernetes as JSON, evaluated by Terraform itself
# (the Argo CD chart's values are HCL: argocd.tf, local.argocd_values), offline:
#   scripts/lib/tf_values.sh <repo-tree> <local-name> <out.json>
#
# The tree is CODE. Terraform runs what it finds there, so this script limits what it can find and
# what the run can reach:
#   - only files git tracks are copied (tracked_copy.sh): never a tfplan, a tfvars, a .terraform
#     directory or any other ignored file; <repo-tree> must be a git checkout;
#   - the module is refused if it holds a provisioner, a data "external" or a data "http", or any
#     *.tf.json (the one provisioner that runs is this script's own export resource, added after
#     the check); the same check runs on the modules `terraform init` downloads, and after init the
#     lock file may name only the module's own providers (hashicorp/aws, helm, kubernetes, random);
#   - terraform runs under `env -i` with a throwaway HOME and no cloud, Kubernetes or HCP Terraform
#     credential (no AWS_*, TF_TOKEN_*, KUBECONFIG; the CLI config file is /dev/null);
#   - the providers are the mocks of this checkout's tests/addons.tftest.hcl (its header, up to the
#     first `run`), not the tree's own test file; the tree's tests and tfvars are dropped.
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
command -v terraform >/dev/null || { echo "tf_values: terraform is not installed" >&2; exit 2; }
out=$(cd "$(dirname "$out")" && pwd)/$(basename "$out")

work=$(mktemp -d)
work=$(cd "$work" && pwd -P)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/home"
tracked_copy "$tree" "$work" deploy infra/aws/kubernetes || { echo "tf_values: could not copy the tracked files of $tree" >&2; exit 2; }
mod="$work/infra/aws/kubernetes"
[ -f "$mod/main.tf" ] || { echo "tf_values: $tree has no infra/aws/kubernetes/main.tf" >&2; exit 2; }
rm -rf "$mod/tests" "$mod/admission-celtest" "$mod/admission-e2e"
find "$mod" \( -name '*.tftest.hcl' -o -name '*.tfvars' -o -name '*.tfvars.json' -o -name '*.tfvars.example' \) -type f -delete

# What the module may not carry (the tree's own files, then the downloaded modules').
bad_hcl() { # <dir>: the offending lines, or nothing
  find "$1" -type f \( -name '*.tf' -o -name '*.tf.json' -o -name '*.tftest.hcl' \) -print0 \
    | xargs -0 grep -nE '^[[:space:]]*provisioner[[:space:]]*"|^[[:space:]]*data[[:space:]]+"(external|http)"|^[[:space:]]*"?(provisioner|local-exec)"?[[:space:]]*[{:]' 2>/dev/null || true
  find "$1" -type f -name '*.tf.json' | sed 's/$/: *.tf.json is not read by this check, so it is refused/'
}
found=$(bad_hcl "$mod")
if [ -n "$found" ]; then
  printf '%s\n' "$found" >&2
  echo "tf_values: the tree's module holds a provisioner, data \"external\", data \"http\" or a .tf.json: refused (this script runs terraform on it)" >&2
  exit 2
fi

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
tf() {
  local envs=(PATH="$PATH" HOME="$work/home" TMPDIR="$work" KUBECONFIG=/dev/null AWS_CONFIG_FILE=/dev/null
    AWS_SHARED_CREDENTIALS_FILE=/dev/null AWS_EC2_METADATA_DISABLED=true TF_CLI_CONFIG_FILE=/dev/null
    TF_IN_AUTOMATION=true TF_INPUT=0 TF_VAR_zz_export_path="$out")
  local v
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
  found=$(bad_hcl "$mod/.terraform/modules")
  if [ -n "$found" ]; then
    printf '%s\n' "$found" >&2
    echo "tf_values: a downloaded module holds a provisioner, data \"external\", data \"http\" or a .tf.json: refused" >&2
    exit 2
  fi
fi
unknown=$(sed -nE 's/^provider "([^"]+)".*/\1/p' "$mod/.terraform.lock.hcl" | grep -vxE 'registry\.terraform\.io/hashicorp/(aws|helm|kubernetes|random)' || true)
if [ -n "$unknown" ]; then
  echo "tf_values: the lock file names providers this check does not run: $unknown" >&2
  exit 2
fi

mv "$work/zz_export.tf.new" "$mod/zz_export.tf"
tf test -no-color >"$work/test.log" 2>&1 || { cat "$work/test.log" >&2; echo "tf_values: terraform test failed" >&2; exit 1; }
[ -s "$out" ] || { cat "$work/test.log" >&2; echo "tf_values: nothing was written to $out" >&2; exit 1; }
echo "tf_values: local.$name written to $out"
