#!/usr/bin/env bash
# scripts/check_chart_bump.sh in a throwaway repository (make test-scripts): a change to the admission
# chart's templates or values needs a higher version in Chart.yaml; nothing else does.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_chart_bump_test: $*" >&2; exit 1; }
C=infra/aws/kubernetes/charts/admission-policy
mkdir -p "$tmp/r/scripts" "$tmp/r/$C/templates"
cp "$HERE/scripts/check_chart_bump.sh" "$tmp/r/scripts/"
cd "$tmp/r"
git init -q -b main && git config user.email t@example.com && git config user.name test
printf 'apiVersion: v2\nname: admission-policy\nversion: 0.2.2  # bump on every template change\n' > "$C/Chart.yaml"
echo 'a: 1' > "$C/values.yaml"; echo 'p: 1' > "$C/templates/p1.yaml"; echo x > other.txt
git add -A; git commit -qm base; base=$(git rev-parse HEAD)
run() { ./scripts/check_chart_bump.sh "$base" "$(git rev-parse HEAD)" 2>/dev/null; }
yes() { run >/dev/null || fail "$1: expected ok"; }
no() { if run >/dev/null; then fail "$1: expected a refusal"; fi; }
reset() { git reset -q --hard "$base"; }

echo y > other.txt; git commit -qam other; yes "a change elsewhere"
reset; echo 'p: 2' > "$C/templates/p1.yaml"; git commit -qam tpl; no "a template changed, version not"
reset; echo 'a: 2' > "$C/values.yaml"; git commit -qam val; no "values changed, version not"
reset; echo 'p: 3' > "$C/templates/p2.yaml"; git add -A; git commit -qm newtpl; no "a template added, version not"
reset; git rm -q "$C/templates/p1.yaml"; git commit -qm rmtpl; no "a template removed, version not"
reset; echo 'p: 2' > "$C/templates/p1.yaml"; sed -i.bak 's/^version: 0.2.2/version: 0.2.3/' "$C/Chart.yaml"; rm "$C/Chart.yaml.bak"; git commit -qam bump; yes "a template and a bump"
reset; echo 'a: 2' > "$C/values.yaml"; sed -i.bak 's/^version: 0.2.2/version: 0.2.10/' "$C/Chart.yaml"; rm "$C/Chart.yaml.bak"; git commit -qam bump10; yes "0.2.2 to 0.2.10 is higher"
reset; echo 'a: 2' > "$C/values.yaml"; sed -i.bak 's/^version: 0.2.2/version: 0.2.1/' "$C/Chart.yaml"; rm "$C/Chart.yaml.bak"; git commit -qam lower; no "a lower version"
reset; echo 'a: 2' > "$C/values.yaml"; sed -i.bak 's/^version: 0.2.2 .*/version: 0.2.2/' "$C/Chart.yaml"; rm "$C/Chart.yaml.bak"; git commit -qam comment; no "only the comment of the version line changed"
reset; sed -i.bak 's/^name: admission-policy/name: admission-policy2/' "$C/Chart.yaml"; rm "$C/Chart.yaml.bak"; git commit -qam name; yes "Chart.yaml changed without templates"
reset; echo 'a: 2' > "$C/values.yaml"; git commit -qam val2
if ./scripts/check_chart_bump.sh "$base" nosuchrev >/dev/null 2>&1; then fail "an unknown revision was accepted"; fi
echo "✓ check_chart_bump_test"
