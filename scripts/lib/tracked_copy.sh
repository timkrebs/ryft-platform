#!/usr/bin/env bash
# shellcheck shell=bash
# Sourced by scripts/lib/tf_values.sh and scripts/admission_e2e.sh.
#
# tracked_copy <tree> <dest> <path>...
#   Copies the files that git TRACKS under the given paths of <tree> to <dest>/<path> (the working
#   tree's content, so uncommitted edits of tracked files count; untracked files and ignored files
#   do not: a tfplan, a terraform.tfvars, a .terraform directory or a git-ignored deploy/k8s/secrets.yaml
#   never reach a rendering, a Terraform run or the git server of the kind cluster). <tree> must be the
#   top level of a git work tree. A tracked symbolic link is refused (it could point out of the tree).
#   A tracked file that is deleted in the working tree is skipped. Exit status: 0, or 1 with a message.
tracked_copy() {
  local tree=$1 dest=$2 top want f n=0
  shift 2
  want=$(cd "$tree" 2>/dev/null && pwd -P) || { echo "tracked_copy: $tree is not a directory" >&2; return 1; }
  top=$(git -C "$want" -c core.fsmonitor=false rev-parse --show-toplevel 2>/dev/null) || top=
  top=$(cd "$top" 2>/dev/null && pwd -P) || top=
  if [ -z "$top" ] || [ "$top" != "$want" ]; then
    echo "tracked_copy: $tree is not the top level of a git work tree; only tracked files are read, so the tree under test must be a git checkout (git worktree add <dir> <ref>)" >&2
    return 1
  fi
  mkdir -p "$dest" || return 1
  while IFS= read -r -d '' f; do
    [ -e "$want/$f" ] || [ -L "$want/$f" ] || continue
    if [ -L "$want/$f" ]; then echo "tracked_copy: $f is a symbolic link: refused" >&2; return 1; fi
    [ -f "$want/$f" ] || continue
    mkdir -p "$dest/$(dirname "$f")" || return 1
    cp -p "$want/$f" "$dest/$f" || return 1
    n=$((n + 1))
  done < <(git -C "$want" -c core.fsmonitor=false ls-files -z --cached -- "$@")
  [ "$n" -gt 0 ] || { echo "tracked_copy: no tracked file under $* in $tree" >&2; return 1; }
}
