#!/usr/bin/env bash
# shellcheck disable=SC2154,SC2015,SC2016,SC2059
# scripts/check_bytes.sh in a throwaway repository: every forbidden code point fails in a
# comment, valid text with arrows and dashes passes, and the real tree is clean.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_bytes_test: $*" >&2; exit 1; }
mkdir -p "$tmp/scripts"; mkdir -p "$tmp/scripts/lib"; cp "$HERE/scripts/check_bytes.sh" "$tmp/scripts/"; cp "$HERE/scripts/lib/charpolicy.py" "$tmp/scripts/lib/"
cd "$tmp"; git init -q -b main; git config user.email t@example.com; git config user.name test
mkdir -p deploy/k8s .github/workflows infra/vault/tests/golden web/scripts/lib backend/internal/notify
printf 'a: 1  # arrows -> \xe2\x86\x92 and dashes \xe2\x80\x94, accents \xc3\xa4, a tab\there\n' > deploy/k8s/ok.yaml
printf 'name: x\n' > .github/workflows/ci.yml
printf 'k: v\n' > infra/vault/tests/golden/g.yaml
git add -A; git commit -qm first
run() { rc=0; ./scripts/check_bytes.sh "$@" >"$tmp/out" 2>"$tmp/err" || rc=$?; }

run; [ "$rc" = 0 ] || fail "clean files must pass (rc=$rc): $(cat "$tmp/err")"
grep -q '5 files' "$tmp/out" || fail "five files expected: $(cat "$tmp/out")"

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
# The binary list: an image is not decoded anywhere except under deploy/ and .github/, where a file with
# that extension is read as text (kustomize can name any file there); its look-alike without the extension
# is always decoded.
mkdir -p web/public docs/img
printf '\x89PNG\xff\xfe\x80' > web/public/logo.png; git add web/public/logo.png; run; [ "$rc" = 0 ] || fail "a png under web/ is on the binary list: $(cat "$tmp/err")"
printf '\x89PNG\xff\xfe\x80' > docs/img/shot.png; git add docs/img/shot.png; run; [ "$rc" = 0 ] || fail "a png under docs/ is not decoded either: $(cat "$tmp/err")"
printf '\x89PNG\xff\xfe\x80' > deploy/k8s/logo.png; git add deploy/k8s/logo.png; run
[ "$rc" = 1 ] && grep -q 'binary extension is read as text' "$tmp/err" || fail "a png under deploy/ is read, with a specific message (rc=$rc): $(cat "$tmp/err")"
printf 'a: 1 # \xe2\x80\xa8 hidden\n' > .github/workflows/x.pdf; git add .github/workflows/x.pdf; run
[ "$rc" = 1 ] && grep -q 'x.pdf' "$tmp/err" || fail "a text file named .pdf in .github/ must be read (rc=$rc)"
git rm -q -f --cached web/public/logo.png docs/img/shot.png deploy/k8s/logo.png .github/workflows/x.pdf; rm -rf web/public docs deploy/k8s/logo.png .github/workflows/x.pdf
printf '\x89PNG\xff\xfe\x80' > logo; git add logo; run; [ "$rc" = 1 ] || fail "invalid UTF-8 in a file without a binary extension must fail"
git rm -q -f --cached logo; rm -f logo
# Unicode categories beyond the named ones: format (zero width), private use, a no-break space, noncharacter.
for cp in '\xe2\x80\x8b' '\xe2\x81\xa0' '\xc2\xad' '\xee\x80\x80' '\xc2\xa0' '\xe2\x80\x83' '\xe3\x80\x80' '\xef\xb7\x90' '\xef\xbf\xbe'; do
  bad "category character $cp" "$cp"
done
for cp in '\xe2\x81\xa5' '\xef\xbf\xb0' '\xef\xbf\xb8' '\xf3\xa0\x80\x81' '\xf3\xa0\x84\x80'; do
  bad "reserved default-ignorable range $cp" "$cp"
done
# U+FE0F: one, directly after a symbol, is fine (the emoji picker inserts it); anywhere else it is not.
printf 'ok: \xe2\x9c\x85\xef\xb8\x8f and \xe2\x96\xb6\xef\xb8\x8e\n' > deploy/k8s/ok2.yaml; git add deploy/k8s/ok2.yaml; run
[ "$rc" = 0 ] || fail "one variation selector after a symbol must pass: $(cat "$tmp/err")"
for v in 'a\xef\xb8\x8f' '\xe2\x9c\x85\xef\xb8\x8f\xef\xb8\x8f' '\xe2\x9c\x85\xef\xb8\x80' ' \xef\xb8\x8f'; do
  printf "x: $v\n" > deploy/k8s/ok2.yaml; run; [ "$rc" = 1 ] || fail "a variation selector in this place must fail: $v"
done
git rm -q -f --cached deploy/k8s/ok2.yaml; rm -f deploy/k8s/ok2.yaml
# Banned names, wherever they are.
for n in Kustomization kustomization.yml deploy/k8s/Kustomization deploy/k8s/kustomization.yml KUSTOMIZATION.YAML .gitattributes web/.gitattributes .gitmodules; do
  mkdir -p "$(dirname "$n")"; printf 'a: 1\n' > "$n"; git add "$n"; run
  [ "$rc" = 1 ] && grep -q 'not allowed' "$tmp/err" || fail "the name $n must be refused (rc=$rc): $(cat "$tmp/err")"
  git rm -q -f --cached "$n"; rm -f "$n"
done
printf 'k: v\n' > deploy/k8s/kustomization.yaml; git add deploy/k8s/kustomization.yaml; run; [ "$rc" = 0 ] || fail "kustomization.yaml is the right name"
git rm -q -f --cached deploy/k8s/kustomization.yaml; rm -f deploy/k8s/kustomization.yaml
# No file is exempt (brand.mjs's character class and the notification texts used to be): the
# characters are written as escapes there, and the escape (backslash, u, four digits) is plain ASCII.
bs=$(printf '\134')
for f in web/scripts/lib/brand.mjs backend/internal/notify/messages.go backend/internal/notify/notify_test.go; do
  for cp in '\xc2\xa0' '\xc2\xad' '\xe2\x80\x8b' '\xe3\x85\xa4'; do
    printf 'x = "a%sb"\n' "$(printf "$cp")" > "$f"; git add "$f"; run
    [ "$rc" = 1 ] || fail "$f may not hold a raw $cp (rc=$rc)"
  done
  printf 'x = "a%su00a0b" /[%su00AD%su200B-%su200F]/\n' "$bs" "$bs" "$bs" "$bs" > "$f"; git add "$f"; run
  [ "$rc" = 0 ] || fail "$f with escapes must pass: $(cat "$tmp/err")"
  git rm -q -f --cached "$f"; rm -f "$f"
done

# Default-ignorable characters that are not format characters, in a comment and in a file name.
for cp in '\xcd\x8f' '\xe1\x85\x9f' '\xe1\x85\xa0' '\xe1\x9e\xb4' '\xe1\x9e\xb5' '\xe1\xa0\x8b' '\xe3\x85\xa4' '\xef\xbe\xa0' '\xef\xb8\x80' '\xef\xb8\x8f' '\xf3\xa0\x84\x80' '\xe2\xa0\x80' '\xcc\xb6'; do
  bad "default-ignorable $cp" "$cp"
done
n="deploy/k8s/re$(printf '\xe3\x85\xa4')sources.yaml"; printf 'a: 1\n' > "$n"; git add "$n"; run
[ "$rc" = 1 ] && grep -q 'file NAME' "$tmp/err" || fail "an invisible character in a file name must fail (rc=$rc)"
git rm -q -f --cached "$n"; rm -f "$n"

# Links: a tracked symlink is refused wherever it is and whatever it points to or is called (a
# png-named link to a kustomization-shaped file carries text no check would read), as is a submodule entry.
printf 'a: 1\n' > target.txt
ln -s target.txt deploy/k8s/og.png; git add deploy/k8s/og.png; run
[ "$rc" = 1 ] && grep -q 'symlink is not allowed' "$tmp/err" || fail "a symlink named .png must fail (rc=$rc): $(cat "$tmp/err")"
git rm -q -f --cached deploy/k8s/og.png; rm -f deploy/k8s/og.png
ln -s target.txt web/public-link.png; git add web/public-link.png; run
[ "$rc" = 1 ] && grep -q 'symlink is not allowed' "$tmp/err" || fail "a symlink named .png under web/ must fail too (rc=$rc)"
git rm -q -f --cached web/public-link.png; rm -f web/public-link.png
ln -s ../../../etc/hostname dangling; git add dangling; run; [ "$rc" = 1 ] || fail "a dangling symlink must fail"
git rm -q -f --cached dangling; rm -f dangling
run target.txt; [ "$rc" = 0 ] || fail "a regular file named on the command line passes"
ln -s target.txt link.yaml; run link.yaml; [ "$rc" = 1 ] || fail "a symlink named on the command line must fail (rc=$rc)"
rm -f link.yaml target.txt
git update-index --add --cacheinfo 160000,1111111111111111111111111111111111111111,sub; run
[ "$rc" = 1 ] && grep -q 'submodule' "$tmp/err" || fail "a submodule entry must fail (rc=$rc): $(cat "$tmp/err")"
git rm -q -f --cached sub
# A type change (a tracked file replaced by a symlink) is seen by --staged.
printf 'a: 1\n' > deploy/k8s/swap.yaml; git add deploy/k8s/swap.yaml; git commit -qm swap
rm deploy/k8s/swap.yaml; ln -s ok.yaml deploy/k8s/swap.yaml; git add deploy/k8s/swap.yaml
run --staged; [ "$rc" = 1 ] && grep -q 'symlink is not allowed' "$tmp/err" || fail "--staged must see a type change to a symlink (rc=$rc): $(cat "$tmp/err")"
git reset -q HEAD deploy/k8s/swap.yaml; rm -f deploy/k8s/swap.yaml; git checkout -q -- deploy/k8s/swap.yaml; git rm -q -f deploy/k8s/swap.yaml; git commit -qm unswap

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
echo "✓ check_bytes_test: line breaks, controls, bidi, BOM and invalid UTF-8 fail in every tracked file, banned names, symlinks, default-ignorable characters; no file is exempt"
