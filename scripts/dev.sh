#!/usr/bin/env bash
# Run h3 studio on its own, under `helm dev` — helmstudio's platform API
# without helmstudio: the studio gets its sessions, assets and gallery through
# the runtime SDK, and its page gets helm-css, the theme stream and this
# studio's hue through the /helm/ proxy, exactly as it does under the app.
#
#   [H3_MODEL=<MiniMax-H3 dir>] [H3_H3C=<h3 binary>] [HELM=<helm>] [H3_DEBUG=1] \
#     bash scripts/dev.sh [extra helm dev flags]
#   bash scripts/dev.sh stop
#
# helm comes from helmstudio's installer, and helm-runtime-sdk from the Go
# module proxy — neither needs a helmstudio checkout. What this script makes
# itself (the pid file) lives in .cache/h3-studio, beside the .helm that
# helm dev keeps.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

RUN="$PWD/.cache/h3-studio"
HELM="${HELM:-helm}"
PIDFILE="$RUN/h3-studio.pid"
MODEL="${H3_MODEL:-$PWD/../MiniMax-H3}"

# Ends the helm dev this script last started, which stops the studio before it
# exits. A run starts by doing the same, so starting again is a restart.
stop() {
  local pid
  pid="$(cat "$PIDFILE" 2>/dev/null || true)"
  if [ -n "$pid" ] && ps -p "$pid" -o command= 2>/dev/null | grep -q "dev -f helmstudio.yaml"; then
    echo "helm dev: stopping the h3 studio started before ($pid)"
    kill -TERM "$pid" 2>/dev/null || true
    while kill -0 "$pid" 2>/dev/null; do sleep 0.2; done
  fi
  rm -f "$PIDFILE"
}

if [ "${1:-}" = "stop" ]; then stop; exit 0; fi
stop
mkdir -p "$RUN"

if ! command -v "$HELM" >/dev/null 2>&1; then
  echo "h3 studio: no helm on PATH. Install it with helmstudio's one-line installer:" >&2
  echo '  /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/janishar/helmstudio/main/installer/install.sh)"' >&2
  echo "or point HELM at one. Then run this again." >&2
  exit 1
fi

if [ ! -d "$MODEL" ]; then
  echo "h3 studio: no MiniMax-H3 checkpoint at $MODEL. Set H3_MODEL to where yours is." >&2
  exit 1
fi

# helm dev runs no build[] — the checkout is the developer's, and it says so
# in its own source. So the binary the manifest launches is built here, and a
# stale one is the difference between the /helm/ proxy working and 404ing.
# H3_DEBUG keeps the optimiser and inliner out of the way so Delve can attach
# and step through what the source actually says.
build=(go build -o dist/h3studio)
if [ -n "${H3_DEBUG:-}" ]; then build+=(-gcflags 'all=-N -l'); fi
echo "h3 studio: building dist/h3studio${H3_DEBUG:+ (for the debugger)}"
"${build[@]}" .

# -link points a weight at a checkpoint that is already on this machine, so
# nothing is downloaded and the directory itself is never written to.
echo "h3 studio: starting under helm dev"
"$HELM" dev -f helmstudio.yaml -link "fl2va=$MODEL" ${H3_H3C:+} "$@" &
echo $! > "$PIDFILE"
wait $!
