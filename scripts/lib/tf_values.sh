#!/usr/bin/env bash
# Writes the value of a local of infra/aws/kubernetes as JSON, evaluated by Terraform itself
# (the Argo CD chart's values are HCL: argocd.tf, local.argocd_values), offline:
#   scripts/lib/tf_values.sh <repo-tree> <local-name> <out.json>
# The module is copied to a temporary directory (the tree is never written to; deploy/ is
# linked, because the module reads files from it), the providers are the mocks of
# tests/addons.tftest.hcl (its header, up to the first `run`), and one terraform_data resource
# writes jsonencode(local.<name>) with a local-exec provisioner during `terraform test`.
# Nothing is created anywhere and no cloud credential is read. `terraform init -backend=false`
# needs the providers of the module's lock file (TF_PLUGIN_CACHE_DIR keeps them between runs;
# CI's `terraform` job warms the same cache). TF_PLUGIN_DIR=<dir> installs them from a local
# directory instead (init -plugin-dir).
# The mocked random_password gives a fixed bcrypt hash: the export is for the RBAC and
# the settings, never a secret.
set -euo pipefail
tree=${1:?usage: tf_values.sh <repo-tree> <local-name> <out.json>}
name=${2:?usage: tf_values.sh <repo-tree> <local-name> <out.json>}
out=${3:?usage: tf_values.sh <repo-tree> <local-name> <out.json>}
case "$name" in *[!a-z0-9_]*|"") echo "tf_values: odd local name '$name'" >&2; exit 2 ;; esac
src="$tree/infra/aws/kubernetes"
[ -d "$src" ] || { echo "tf_values: $src is not a directory" >&2; exit 2; }
command -v terraform >/dev/null || { echo "tf_values: terraform is not installed" >&2; exit 2; }
out=$(cd "$(dirname "$out")" && pwd)/$(basename "$out")

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/infra/aws"
cp -R "$src" "$work/infra/aws/kubernetes"
rm -rf "$work/infra/aws/kubernetes/.terraform" "$work/infra/aws/kubernetes/tests" \
  "$work/infra/aws/kubernetes/admission-celtest" "$work/infra/aws/kubernetes/terraform.tfvars"
ln -s "$(cd "$tree" && pwd)/deploy" "$work/deploy"
mod="$work/infra/aws/kubernetes"

# The mocks, from the module's own test (a test file of its own: terraform test runs the files
# of tests/ in the module). The header ends at the first `run` block.
header="$src/tests/addons.tftest.hcl"
[ -f "$header" ] || { echo "tf_values: $header (the mock providers) is missing" >&2; exit 2; }
mkdir -p "$mod/tests"
awk '/^run /{exit} {print}' "$header" > "$mod/tests/zz_export.tftest.hcl"
cat >> "$mod/tests/zz_export.tftest.hcl" <<'HCL'

run "export" {
  command = apply
}
HCL

cat > "$mod/zz_export.tf" <<HCL
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

cd "$mod"
# The module sources are fetched over https by git (the job-wide GIT_ALLOW_PROTOCOL=file of the
# other CI jobs would refuse them): this step alone lifts it.
unset GIT_ALLOW_PROTOCOL
export TF_IN_AUTOMATION=true TF_INPUT=0 TF_VAR_zz_export_path="$out"
init_args=(-backend=false -input=false -no-color)
[ -z "${TF_PLUGIN_DIR:-}" ] || init_args+=(-plugin-dir="$TF_PLUGIN_DIR")
terraform init "${init_args[@]}" >"$work/init.log" 2>&1 || { cat "$work/init.log" >&2; echo "tf_values: terraform init failed" >&2; exit 1; }
terraform test -no-color >"$work/test.log" 2>&1 || { cat "$work/test.log" >&2; echo "tf_values: terraform test failed" >&2; exit 1; }
[ -s "$out" ] || { cat "$work/test.log" >&2; echo "tf_values: nothing was written to $out" >&2; exit 1; }
echo "tf_values: local.$name written to $out"
