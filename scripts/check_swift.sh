#!/usr/bin/env bash
# Contract check from the iOS side (macOS + Swift toolchain; not part of smoke/CI).
# 1. Compiles the "Swift mirror" block of docs/API.md and decodes every endpoint of a
#    running stack with the app's decoder settings.
# 2. If the app checkout exists (APP_DIR, default ../app/Ryft), compiles the app's
#    current wire models (APP_SOURCES, relative to APP_DIR) and decodes every endpoint
#    the app calls, asserting the lenient decoders did not silently drop data.
#   API_URL=http://localhost:8082 API_KEY=... ./scripts/check_swift.sh [YYYY-MM-DD]
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(dirname "$HERE")
TODAY=${1:-$(date +%F)}
APP_DIR=${APP_DIR:-$ROOT/../app/Ryft}
APP_SOURCES=${APP_SOURCES:-Networking/Models.swift Networking/SleepMath.swift App/Formatting.swift}
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

python3 - "$ROOT/docs/API.md" "$WORK/Mirror.swift" <<'PY'
import re, sys
doc = open(sys.argv[1]).read().split("## Swift mirror", 1)[1]
block = re.search(r"```swift\n(.*?)```", doc, re.S)
open(sys.argv[2], "w").write(block.group(1))
PY

echo "→ Swift mirror from docs/API.md"
mkdir "$WORK/mirror"
cp "$HERE/swift/CheckMirror.swift" "$WORK/mirror/main.swift"
swiftc -swift-version 5 -o "$WORK/mirror/check" "$WORK/Mirror.swift" "$HERE/swift/Support.swift" "$WORK/mirror/main.swift"
"$WORK/mirror/check" "$TODAY"

if [ -f "$APP_DIR/Networking/Models.swift" ]; then
  echo "→ the app's current models ($APP_DIR: $APP_SOURCES)"
  mkdir "$WORK/app"
  cp "$HERE/swift/CheckApp.swift" "$WORK/app/main.swift"
  srcs=()
  for f in $APP_SOURCES; do srcs+=("$APP_DIR/$f"); done
  swiftc -swift-version 5 -o "$WORK/app/check" "${srcs[@]}" "$HERE/swift/Support.swift" "$WORK/app/main.swift"
  "$WORK/app/check" "$TODAY"
else
  echo "→ $APP_DIR/Networking/Models.swift not found, skipping the app compatibility check"
fi
