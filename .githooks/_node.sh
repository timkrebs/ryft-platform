# Sourced by the hooks: make sure `node` is on PATH. GUI clients (VS Code, Xcode) start git
# without the shell's PATH, so look where Node usually lives. RYFT_HOOKS_NODE_DIRS (a
# space-separated list, may be empty) replaces the default places; the tests use it.
# Only absolute folders count, and the one found is appended to PATH (never shadows git).
# Of several nvm versions the one matching web/.nvmrc's major comes first, then newest first.
ensure_node() {
  command -v node >/dev/null 2>&1 && return 0
  if [ -n "${RYFT_HOOKS_NODE_DIRS+set}" ]; then
    dirs=$RYFT_HOOKS_NODE_DIRS
  else
    dirs="/opt/homebrew/bin /usr/local/bin $HOME/.volta/bin"
    major=$(sed -n 's/^v\{0,1\}\([0-9]\{1,\}\).*/\1/p;q' web/.nvmrc 2>/dev/null)
    pref=""
    rest=""
    for d in $(ls -d "${NVM_DIR:-$HOME/.nvm}"/versions/node/v*/bin 2>/dev/null | sort -rV); do
      case $d in
        */versions/node/v"$major".*/bin) pref="$pref $d" ;; # the major of .nvmrc
        *) rest="$rest $d" ;;
      esac
    done
    dirs="$pref $rest $dirs"
  fi
  for d in $dirs; do
    case $d in /*) ;; *) continue ;; esac
    if [ -x "$d/node" ]; then
      PATH="$PATH:$d"
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
