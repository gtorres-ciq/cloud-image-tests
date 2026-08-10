#!/bin/bash
# tools/cell-parity.sh — enumerate the (image, shape, suite) cells a
# run_tests.sh-shaped script would launch, without docker.
#
# Usage: tools/cell-parity.sh <script> [args...]
# Prints sorted "CELL <image> <shape> <suite>" lines.
#
# Transformation: replace the docker-run launch line with an echo, neutralize
# sleeps, and point the pause-file check at a path that never exists. Runs in
# a clean temp dir so the resume check (ls of *.xml in cwd) never skips cells.
set -euo pipefail

src="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
shift || true
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

sed -e 's|^\( *\)/bin/bash -c "docker run.*|\1echo CELL "$image" "$shape" "$testrun"|' \
    -e 's/sleep 5/:/g' \
    -e 's/sleep 45/:/g' \
    -e 's|/tmp/pause.txt|/nonexistent/pause|g' \
    "$src" >"$tmp/script.sh"
chmod +x "$tmp/script.sh"

(cd "$tmp" && ./script.sh "$@") 2>/dev/null | grep '^CELL ' | LC_ALL=C sort
