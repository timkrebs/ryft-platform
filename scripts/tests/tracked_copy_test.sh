#!/usr/bin/env bash
# shellcheck disable=SC1091
# scripts/lib/tracked_copy.sh and the refusals of scripts/lib/tf_values.sh (make test-scripts): only
# files git tracks are copied (an ignored tfplan or secrets.yaml and an untracked file are not), a
# symbolic link or a directory that is no git checkout is refused, and a module with a provisioner,
# data "external" or data "http" is refused before terraform runs. No terraform provider is run.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(cd "$(mktemp -d)" && pwd -P)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ tracked_copy_test: $*" >&2; exit 1; }
. "$HERE/scripts/lib/tracked_copy.sh"
g() { git -C "$tmp/t" -c user.name=t -c user.email=t@example.invalid -c commit.gpgsign=false "$@"; }

mkdir -p "$tmp/t/deploy/k8s" "$tmp/t/infra/aws/kubernetes"
git -C "$tmp/t" init -q
printf 'tfplan\nsecrets.yaml\n.terraform/\n' > "$tmp/t/.gitignore"
echo 'a: 1' > "$tmp/t/deploy/k8s/a.yaml"
echo 'resource "terraform_data" "x" {}' > "$tmp/t/infra/aws/kubernetes/main.tf"
echo gone > "$tmp/t/deploy/k8s/gone.yaml"
g add -A >/dev/null; g commit -qm one
echo 'a: 2' > "$tmp/t/deploy/k8s/a.yaml"                       # a tracked file edited, not committed
rm "$tmp/t/deploy/k8s/gone.yaml"                                 # a tracked file deleted, not committed
echo secret > "$tmp/t/deploy/k8s/secrets.yaml"                   # ignored
echo plan > "$tmp/t/infra/aws/kubernetes/tfplan"                 # ignored
echo untracked > "$tmp/t/deploy/k8s/new.yaml"                    # untracked
mkdir -p "$tmp/t/infra/aws/kubernetes/.terraform"; echo p > "$tmp/t/infra/aws/kubernetes/.terraform/x"

tracked_copy "$tmp/t" "$tmp/out" deploy infra/aws/kubernetes 2>/dev/null || fail "tracked_copy failed on a plain tree"
[ "$(cat "$tmp/out/deploy/k8s/a.yaml")" = "a: 2" ] || fail "the working-tree content of a tracked file is not what was copied"
[ -f "$tmp/out/infra/aws/kubernetes/main.tf" ] || fail "a tracked file was not copied"
for f in deploy/k8s/secrets.yaml infra/aws/kubernetes/tfplan deploy/k8s/new.yaml infra/aws/kubernetes/.terraform/x deploy/k8s/gone.yaml; do
  [ ! -e "$tmp/out/$f" ] || fail "$f was copied"
done

# a link is refused, a directory that is no checkout is refused, so is a path with no tracked file
ln -s /etc/hosts "$tmp/t/deploy/k8s/link.yaml"; g add -f deploy/k8s/link.yaml >/dev/null
if tracked_copy "$tmp/t" "$tmp/out2" deploy >/dev/null 2>&1; then fail "a tracked symbolic link was copied"; fi
g rm -qf --cached deploy/k8s/link.yaml >/dev/null; rm "$tmp/t/deploy/k8s/link.yaml"
mkdir -p "$tmp/plain/deploy"; echo x > "$tmp/plain/deploy/x.yaml"
if tracked_copy "$tmp/plain" "$tmp/out3" deploy >/dev/null 2>&1; then fail "a directory that is no git checkout was copied"; fi
mkdir -p "$tmp/t/sub"; echo y > "$tmp/t/sub/y"
if tracked_copy "$tmp/t/sub" "$tmp/out4" . >/dev/null 2>&1; then fail "a sub directory of a checkout was accepted as the top level"; fi
if tracked_copy "$tmp/t" "$tmp/out5" nothing-here >/dev/null 2>&1; then fail "a path without a tracked file was accepted"; fi

# tf_values: the module is judged before terraform runs (a stub terraform proves it never ran)
mkdir -p "$tmp/bin"
printf '#!/bin/sh\necho ran > "%s/terraform-ran"\nexit 1\n' "$tmp" > "$tmp/bin/terraform"; chmod +x "$tmp/bin/terraform"
try() { # <name> <main.tf content> -> expects exit 2 and no terraform run
  rm -f "$tmp/terraform-ran"
  printf '%s\n' "$2" > "$tmp/t/infra/aws/kubernetes/main.tf"; g add -A >/dev/null; g commit -qm "$1" --allow-empty
  if PATH="$tmp/bin:$PATH" "$HERE/scripts/lib/tf_values.sh" "$tmp/t" argocd_values "$tmp/o.json" >/dev/null 2>&1; then fail "$1: expected a refusal"; fi
  [ ! -e "$tmp/terraform-ran" ] || fail "$1: terraform ran on a refused module"
}
mkdir -p "$tmp/t/tests"
try provisioner 'resource "terraform_data" "x" {
  provisioner "local-exec" { command = "id" }
}'
try external 'data "external" "x" { program = ["sh"] }'
try http 'data  "http"  "x" { url = "https://example.com" }'
rm -f "$tmp/t/infra/aws/kubernetes/main.tf"
echo '{}' > "$tmp/t/infra/aws/kubernetes/extra.tf.json"; echo 'locals {}' > "$tmp/t/infra/aws/kubernetes/main.tf"; g add -A >/dev/null; g commit -qm json
rm -f "$tmp/terraform-ran"
if PATH="$tmp/bin:$PATH" "$HERE/scripts/lib/tf_values.sh" "$tmp/t" argocd_values "$tmp/o.json" >/dev/null 2>&1; then fail "a .tf.json was accepted"; fi
[ ! -e "$tmp/terraform-ran" ] || fail "terraform ran on a module with a .tf.json"

echo "✓ tracked_copy_test"
