#!/usr/bin/env bash
# shellcheck disable=SC2016
# scripts/lib/tf_values.sh and scripts/lib/tf_scan.py (make test-scripts), with a stub terraform that
# never leaves the temporary directory: the TRIPWIRE that judges a tree's module before Terraform runs
# on it. A bypass found by review is a case here: an aliased provider with exec / credential_process /
# postrender, config_path, shared_config_files, a provisioner after a comment or on one line, a
# \u-escaped label, a *.tftest.json or *.tfmock.* in the module root, and a downloaded module that
# carries one. The optional container run (TF_VALUES_CONTAINER) is tested with a fake docker.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(cd "$(mktemp -d)" && pwd -P)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ tf_values_test: $*" >&2; exit 1; }
T="$tmp/t"
MOD=infra/aws/kubernetes
g() { git -C "$T" -c user.name=t -c user.email=t@example.invalid -c commit.gpgsign=false "$@"; }
mkdir -p "$T/$MOD" "$tmp/bin"
git -C "$T" init -q
printf 'locals {}\n' > "$T/$MOD/main.tf"
mkdir -p "$T/deploy"; echo 'a: 1' > "$T/deploy/a.yaml"
g add -A >/dev/null; g commit -qm base

# The stub: init may plant a downloaded module (STUB_MODULE_TF) and a lock file; test lists the test and
# mock files it can see and writes the export.
cat > "$tmp/bin/terraform" <<'STUB'
#!/bin/sh
echo "$*" >> "@LOG@"
case "$1" in
  init)
    if [ -s "@MOD@" ]; then mkdir -p .terraform/modules/m; cp "@MOD@" .terraform/modules/m/main.tf; fi
    if [ -s "@MOD@.ex" ]; then mkdir -p .terraform/modules/m/examples/x; cp "@MOD@.ex" .terraform/modules/m/examples/x/main.tf; fi
    printf 'provider "registry.terraform.io/hashicorp/aws" {\n}\n' > .terraform.lock.hcl ;;
  test)
    find . -path ./.terraform -prune -o \( -name '*.tftest.*' -o -name '*.tfmock.*' \) -print | sort > "@SEEN@"
    printf '{"a":1}' > "$TF_VAR_zz_export_path" ;;
esac
exit 0
STUB
STUB_LOG="$tmp/stub.log" STUB_SEEN="$tmp/seen" STUB_MOD="$tmp/stub-module.tf"   # env -i hides variables from the stub: paths are baked in
sed -i.bak -e "s|@LOG@|$STUB_LOG|" -e "s|@MOD@|$STUB_MOD|g" -e "s|@SEEN@|$STUB_SEEN|" "$tmp/bin/terraform"; rm -f "$tmp/bin/terraform.bak"
chmod +x "$tmp/bin/terraform"
: > "$STUB_MOD"

# GITHUB_ACTIONS and FORCE are the environment's: pinned here (the tree is not this checkout, so the host needs --force)
run() { env -u GITHUB_ACTIONS TF_VALUES_FORCE="${RUN_FORCE-1}" PATH="$tmp/bin:$PATH" "$HERE/scripts/lib/tf_values.sh" "$T" argocd_values "$tmp/o.json" >"$tmp/run.out" 2>&1; }
setmain() { rm -f "$T/$MOD"/*.tf "$T/$MOD"/*.tf.json; printf '%s\n' "$2" > "$T/$MOD/main.tf"; g add -A >/dev/null; g commit -qm "$1" --allow-empty; : > "$STUB_LOG"; rm -f "$tmp/o.json"; }
# refused <name> <main.tf>: exit 2, terraform never ran.
refused() {
  setmain "$1" "$2"
  if run; then fail "$1: expected a refusal"; else rc=$?; fi
  [ "$rc" = 2 ] || fail "$1: expected exit 2, got $rc: $(cat "$tmp/run.out")"
  [ ! -s "$STUB_LOG" ] || fail "$1: terraform ran on a refused module"
}
# accepted <name> <main.tf>: the tripwire lets it through (the stub then runs init and test).
accepted() {
  setmain "$1" "$2"
  run || fail "$1: expected to pass: $(cat "$tmp/run.out")"
  grep -q '^test' "$STUB_LOG" || fail "$1: terraform test did not run: $(cat "$STUB_LOG" "$tmp/run.out")"
}

# --- what was refused before, and still is
refused provisioner 'resource "terraform_data" "x" {
  provisioner "local-exec" { command = "id" }
}'
refused external 'data "external" "x" { program = ["sh"] }'
refused http 'data  "http"  "x" { url = "https://example.com" }'

# --- the bypasses
refused alias-exec-kubernetes 'provider "kubernetes" {
  alias = "x"
  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "sh"
  }
}
data "kubernetes_namespace_v1" "n" {
  provider = kubernetes.x
  metadata { name = "default" }
}'
refused alias-only 'provider "kubernetes" { alias = "x" }'
refused helm-postrender 'resource "helm_release" "x" {
  name = "x"
  chart = "y"
  postrender = { binary_path = "/tmp/evil" }
}'
refused aws-shared-config 'provider "aws" {
  alias               = "p"
  shared_config_files = ["/tmp/evil-config"]
}'
refused aws-shared-credentials 'provider "aws" { shared_credentials_files = ["/Users/x/.aws/credentials"] }'
refused aws-credential-process 'provider "aws" { credential_process = "/tmp/evil" }'
refused kube-config-path 'provider "kubernetes" { config_path = "/Users/x/.kube/config" }'
refused provisioner-after-block-comment '/* a comment */ provisioner "local-exec" { command = "id" }'
refused provisioner-one-line 'resource "terraform_data" "x" { provisioner "local-exec" { command = "id" } }'
refused provisioner-after-multiline-comment 'resource "terraform_data" "x" {
  /* a
     comment */ provisioner "local-exec" {
    command = "id"
  }
}'
refused external-after-comment '/* x */ data "external" "x" { program = ["sh"] }'
refused external-escaped-label 'data "external" "x" { program = ["sh"] }'
refused local-exec-label-in-object 'locals { p = { type = "local-exec" } }'
refused exec-after-hash-in-string 'locals { u = "a # b" }
provider "kubernetes" {
  exec { command = "sh" }
}'
refused exec-after-heredoc 'locals { h = <<-EOT
  # not a comment, inside a heredoc
  EOT
}
provider "kubernetes" { exec { command = "sh" } }'
refused unterminated-comment 'locals {}
/* never closed
provisioner "local-exec" { command = "id" }'
setmain tfjson 'locals {}'; printf '{}\n' > "$T/$MOD/extra.tf.json"; g add -A >/dev/null; g commit -qm json; : > "$STUB_LOG"
if run; then fail "a .tf.json was accepted"; fi
[ ! -s "$STUB_LOG" ] || fail "terraform ran on a module with a .tf.json"
rm -f "$T/$MOD/extra.tf.json"

# --- what is not a finding (a comment, a string, a longer identifier): the stub then runs
accepted comments '# exec, alias and provisioner "local-exec" in a hash comment
// provisioner in a slash comment
/* alias = "x"
   exec { command = "sh" } */
locals { a = 1 }'
accepted strings 'locals {
  description = "exec alias provisioner postrender"
  url         = "https://example.com/a#b//c"
  tpl         = "${lower("EXEC")} and ${upper("alias")}"
  kms         = "alias/ryft"
}
resource "aws_kms_alias" "x" {
  name = "alias/x"
}'
accepted heredoc 'locals { h = <<-EOT
  exec alias provisioner "local-exec"
  EOT
}'

# --- test and mock files of the tree are dropped before terraform runs
setmain dropped 'locals {}'
printf '{}\n' > "$T/$MOD/evil.tftest.json"; printf 'x = 1\n' > "$T/$MOD/evil.tfmock.hcl"; printf '{}\n' > "$T/$MOD/evil.tfmock.json"
mkdir -p "$T/$MOD/tests"; printf 'run "x" {}\n' > "$T/$MOD/tests/evil.tftest.hcl"; printf '{}\n' > "$T/$MOD/tests/evil.tftest.json"
g add -A >/dev/null; g commit -qm tests
: > "$STUB_LOG"; run || fail "a tree with test files was refused: $(cat "$tmp/run.out")"
[ "$(cat "$STUB_SEEN")" = "./tests/zz_export.tftest.hcl" ] || fail "test or mock files of the tree reached terraform: $(cat "$STUB_SEEN")"
cmp -s "$tmp/o.json" <(printf '{"a":1}') || fail "the export did not reach the output file"
git -C "$T" rm -rq --cached "$MOD/tests" "$MOD/evil.tftest.json" "$MOD/evil.tfmock.hcl" "$MOD/evil.tfmock.json"; rm -rf "$T/$MOD/tests" "$T/$MOD"/evil.*; g commit -qm "drop test files"

# --- what reads a local file into a value is refused on the host, unless the file is this checkout's
refused file-func 'locals { a = file("/etc/passwd") }'
refused file-func-space 'locals { a = file ("/etc/passwd") }'
refused templatefile 'locals { a = templatefile("/etc/hosts", {}) }'
refused filebase64 'locals { a = filebase64("/etc/hosts") }'
refused filesha256 'locals { a = filesha256("/etc/hosts") }'
refused fileset 'locals { a = fileset("/", "*") }'
refused fileexists 'locals { a = fileexists("/etc/hosts") }'
refused pathexpand 'locals { a = pathexpand("~/.aws/credentials") }'
refused abspath 'locals { a = abspath("../../..") }'
refused core-namespace 'locals { a = core::file("/etc/hosts") }'
refused provider-function 'locals { a = provider::local::read("/etc/hosts") }'
refused path-cwd 'locals { a = path.cwd }'
refused data-local-file 'data "local_file" "x" { filename = "/etc/hosts" }'
refused data-local-sensitive-file 'data "local_sensitive_file" "x" { filename = "/etc/hosts" }'
refused data-remote-state 'data "terraform_remote_state" "x" { backend = "local" }'
accepted file-in-text '# file("/etc/hosts") and fileset() in a comment
locals {
  a = "file(/etc/hosts) provider::x"
  profile_file = "x"
  arn = "arn:aws:iam::123:role/x"
}'
# byte-identical to this checkout's: exempt for the file functions only; edited: refused
setmain files-identical 'locals {}'
cp "$HERE/$MOD/admission.tf" "$HERE/$MOD/variables.tf" "$T/$MOD/"; g add -A >/dev/null; g commit -qm "admission.tf"; : > "$STUB_LOG"
run || fail "this checkout's own admission.tf and variables.tf were refused: $(cat "$tmp/run.out")"
printf '# edited\n' >> "$T/$MOD/admission.tf"; g add -A >/dev/null; g commit -qm "admission.tf edited"; : > "$STUB_LOG"
if run; then fail "an edited admission.tf that reads files was accepted"; fi
[ ! -s "$STUB_LOG" ] || fail "terraform ran on an edited admission.tf"
setmain plain 'locals {}'
# a tree that is not this checkout needs the container or --force on the host; this checkout needs neither
: > "$STUB_LOG"
if RUN_FORCE='' run; then fail "another tree ran on the host without a container or --force"; fi
grep -q 'TF_VALUES_CONTAINER' "$tmp/run.out" || fail "the refusal does not name TF_VALUES_CONTAINER: $(cat "$tmp/run.out")"
[ ! -s "$STUB_LOG" ] || fail "terraform ran for a tree that was refused"
RUN_FORCE=1 run || fail "--force did not let another tree run: $(cat "$tmp/run.out")"
grep -q '!!!!' "$tmp/run.out" || fail "--force without the loud warning: $(cat "$tmp/run.out")"
: > "$STUB_LOG"
env -u GITHUB_ACTIONS PATH="$tmp/bin:$PATH" "$HERE/scripts/lib/tf_values.sh" "$HERE" argocd_values "$tmp/o3.json" >"$tmp/run.out" 2>&1 || fail "this checkout (with its file functions) was refused: $(cat "$tmp/run.out")"
grep -q '^test' "$STUB_LOG" || fail "this checkout: terraform test did not run"
# on a GitHub runner the file functions are allowed, with a notice
setmain actions 'locals { a = file("/etc/hosts") }'
GITHUB_ACTIONS=true PATH="$tmp/bin:$PATH" "$HERE/scripts/lib/tf_values.sh" "$T" argocd_values "$tmp/o4.json" >"$tmp/run.out" 2>&1 || fail "GITHUB_ACTIONS: file() was refused: $(cat "$tmp/run.out")"
grep -q 'notice' "$tmp/run.out" || fail "GITHUB_ACTIONS: no notice"

# --- providers.tf: the real exec is allowed only as this checkout's own file, byte for byte
setmain providers 'locals {}'
cp "$HERE/$MOD/providers.tf" "$T/$MOD/providers.tf"; g add -A >/dev/null; g commit -qm providers; : > "$STUB_LOG"
run || fail "this checkout's own providers.tf was refused: $(cat "$tmp/run.out")"
printf '# edited\n' >> "$T/$MOD/providers.tf"; g add -A >/dev/null; g commit -qm "providers edited"; : > "$STUB_LOG"
if run; then fail "an edited providers.tf with exec was accepted"; fi
[ ! -s "$STUB_LOG" ] || fail "terraform ran on an edited providers.tf"
git -C "$T" rm -qf "$MOD/providers.tf"; g commit -qm "no providers"

# --- a module that terraform init downloads is scanned too
setmain downloaded 'locals {}'
printf 'provider "kubernetes" {\n  alias = "x"\n  exec { command = "sh" }\n}\n' > "$STUB_MOD"
if run; then fail "a downloaded module with an aliased exec provider was accepted"; else rc=$?; fi
[ "$rc" = 2 ] || fail "a downloaded module: expected exit 2, got $rc"
if grep -q '^test' "$STUB_LOG"; then fail "terraform test ran with a refused downloaded module"; fi
: > "$STUB_LOG"
printf 'locals { a = 1 }\n' > "$STUB_MOD"
run || fail "a clean downloaded module was refused: $(cat "$tmp/run.out")"
# its examples/ are not loaded by Terraform and may use path.cwd; its root may not
printf 'locals { a = path.cwd }\n' > "$STUB_MOD.ex"
run || fail "a downloaded module's examples/ were scanned: $(cat "$tmp/run.out")"
printf 'locals { a = file("x") }\n' > "$STUB_MOD"
if run; then fail "a downloaded module's root that reads a file was accepted"; fi
rm -f "$STUB_MOD.ex"; : > "$STUB_MOD"

# --- the optional container: init with a network, test with none, only the work directory mounted
mkdir -p "$tmp/fakebin"
cat > "$tmp/fakebin/docker" <<'FAKE'
#!/bin/bash
echo "$*" >> "$DOCKER_LOG"
for a in "$@"; do case "$a" in TF_VAR_zz_export_path=*) exp=${a#*=} ;; -w) w=next ;; esac; done
i=0; for a in "$@"; do i=$((i + 1)); if [ "$a" = "$IMAGE" ]; then sub=${*:$((i + 1)):1}; fi; done
workdir=""; prev=""; for a in "$@"; do [ "$prev" = -w ] && workdir=$a; prev=$a; done
cd "$workdir" || exit 1
case "$sub" in
  init) printf 'provider "registry.terraform.io/hashicorp/aws" {\n}\n' > .terraform.lock.hcl ;;
  test) printf '{"c":1}' > "$exp" ;;
esac
exit 0
FAKE
chmod +x "$tmp/fakebin/docker"
export DOCKER_LOG="$tmp/docker.log" IMAGE='example.invalid/terraform@sha256:0000000000000000000000000000000000000000000000000000000000000000'
setmain container 'locals { a = file("x") }'; : > "$DOCKER_LOG"   # the container sees only the work directory: file functions are fine there
PATH="$tmp/fakebin:$tmp/bin:$PATH" TF_VALUES_CONTAINER="$IMAGE" "$HERE/scripts/lib/tf_values.sh" "$T" argocd_values "$tmp/o.json" >"$tmp/run.out" 2>&1 || fail "the container run failed: $(cat "$tmp/run.out")"
[ ! -s "$STUB_LOG" ] || fail "the container run used the host's terraform"
[ "$(wc -l < "$DOCKER_LOG" | tr -d ' ')" = 2 ] || fail "expected two container runs (init, test): $(cat "$DOCKER_LOG")"
init_line=$(sed -n 1p "$DOCKER_LOG"); test_line=$(sed -n 2p "$DOCKER_LOG")
case "$init_line" in *" --network bridge "*" init "*) ;; *) fail "init did not run with a network: $init_line" ;; esac
case "$test_line" in *" --network none "*" test "*) ;; *) fail "test did not run with --network none: $test_line" ;; esac
for l in "$init_line" "$test_line"; do
  case "$l" in *"--cap-drop ALL"*"--security-opt no-new-privileges"*) ;; *) fail "capabilities were not dropped: $l" ;; esac
  [ "$(printf '%s\n' "$l" | grep -o ' -v ' | wc -l | tr -d ' ')" = 1 ] || fail "more than the work directory is mounted: $l"
  case "$l" in *"HOME=$HOME "*|*"HOME=$HOME/"*) fail "the real HOME was passed: $l" ;; esac
done
[ "$(cat "$tmp/o.json")" = '{"c":1}' ] || fail "the container's export did not reach the output file"
# asked for a container but there is no engine: stop, never fall back to the host
mkdir -p "$tmp/nobin"; ln -sf "$(command -v python3)" "$tmp/nobin/python3"; ln -sf "$(command -v dirname)" "$tmp/nobin/dirname"
: > "$STUB_LOG"
if PATH="$tmp/nobin" TF_VALUES_CONTAINER="$IMAGE" "$BASH" "$HERE/scripts/lib/tf_values.sh" "$T" argocd_values "$tmp/o2.json" >"$tmp/run.out" 2>&1; then fail "a container was asked for, no engine exists, and the run went on"; fi
grep -q 'neither docker nor podman' "$tmp/run.out" || fail "the missing engine was not named: $(cat "$tmp/run.out")"

echo "✓ tf_values_test"
