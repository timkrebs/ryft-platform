# Sourced by the hooks: make sure `node` is on PATH. GUI clients (VS Code, Xcode) start git
# without the shell's PATH, so look where Node usually lives. RYFT_HOOKS_NODE_DIRS (a
# space-separated list, may be empty) replaces the default places; the tests use it.
ensure_node() {
  command -v node >/dev/null 2>&1 && return 0
  if [ -n "${RYFT_HOOKS_NODE_DIRS+set}" ]; then
    dirs=$RYFT_HOOKS_NODE_DIRS
  else
    dirs="/opt/homebrew/bin /usr/local/bin $HOME/.volta/bin"
    for d in "${NVM_DIR:-$HOME/.nvm}"/versions/node/*/bin; do [ -x "$d/node" ] && dirs="$dirs $d"; done
  fi
  for d in $dirs; do
    if [ -x "$d/node" ]; then
      PATH="$d:$PATH"
      export PATH
      return 0
    fi
  done
  return 1
}

# Fail closed: a message or a file that slips through can't be taken back (no force-push).
no_node() {
  echo "$1: node not found, so the brand check can't run." >&2
  echo "Install Node (web/.nvmrc), or commit with --no-verify deliberately." >&2
  exit 1
}
