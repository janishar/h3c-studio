#!/usr/bin/env bash
# Launch h3 studio from a checkout, under `helm dev`: it keeps everything the
# studio keeps in ./.helm, passes that directory as --root, and serves helm-css,
# the theme and this studio's hue through the /helm/ proxy the server mounts.
# The studio does not start without it — there is no standalone mode.
#
#   [H3_MODEL=<MiniMax-H3 dir>] [HELM=<helm>] [H3_DLV=<port>] bash scripts/run.sh [helm dev flags]
#   bash scripts/run.sh stop
#
# helm comes from helmstudio's installer and helm-runtime-sdk from the Go
# module proxy, so neither needs a helmstudio checkout. What the script makes
# itself (the debugger's shim, its pid file) stays in .cache/h3-studio and
# dist/, beside the .helm helm dev keeps. What each variable does: README.md,
# "Running under helmstudio".
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

RUN="$PWD/.cache/h3-studio"
HELM="${HELM:-helm}"
PIDFILE="$RUN/h3-studio.pid"

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
if [ "${1:-}" = "stop" ]; then
  stop
  exit 0
fi

if ! command -v "$HELM" >/dev/null; then
  echo "helm is not installed. Install it with helmstudio's installer, or set HELM to its path:" >&2
  echo '  /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/janishar/helmstudio/main/installer/install.sh)"' >&2
  exit 1
fi
if [ ! -x h3c/h3 ]; then
  echo "h3c/h3 is not built: git submodule update --init --recursive, then make -j8 -C h3c" >&2
  exit 1
fi

if [ -n "${H3_MODEL:-}" ]; then
  if [ ! -d "$H3_MODEL" ]; then
    echo "weights: no MiniMax-H3 checkpoint at $H3_MODEL" >&2
    exit 1
  fi
  # helm dev records where a weight is linked and links per file, so the
  # directory itself is never written to and nothing is downloaded.
  echo "weights: linking $H3_MODEL as fl2va"
  set -- -link "fl2va=$H3_MODEL" "$@"
fi

# helm dev runs no build[] — the checkout is the developer's, and its own
# source says so — so it launches whatever dist/h3studio happens to be. A
# stale one is the difference between the /helm/ proxy answering and 404ing.
# A debug run leaves dist/h3studio as a shell script, and `go build -o` refuses
# to overwrite a file that is not an object file — so the last run's shim goes
# before this one builds, whichever kind of run this is.
rm -f dist/h3studio dist/h3studio.bin
BUILD=(go build -o dist/h3studio)
[ -n "${H3_DLV:-}" ] && BUILD=(go build -gcflags 'all=-N -l' -o dist/h3studio)
echo "build: dist/h3studio${H3_DLV:+ (for the debugger)}"
"${BUILD[@]}" .

if [ -n "${H3_DLV:-}" ]; then
  # helm dev hands the studio only a restricted environment, so a request to
  # listen for a debugger cannot reach it as a variable — and the manifest
  # names ./dist/h3studio, not a debugger. The binary moves aside and that
  # name becomes a shim that runs it under Delve, which listens for the
  # editor. A later run without H3_DLV builds over the shim again.
  if ! command -v dlv >/dev/null; then
    echo "dlv is not installed: go install github.com/go-delve/delve/cmd/dlv@latest" >&2
    exit 1
  fi
  # --continue matters: without it Delve halts the program at entry and waits
  # for a client, the studio never listens, and helm dev fails it on
  # health_timeout 30 seconds later. debugpy does not wait by default and
  # Delve does, which is the one place these two shims differ.
  mv dist/h3studio dist/h3studio.bin
  printf '#!/bin/sh\nexec dlv exec --headless --continue --accept-multiclient --api-version=2 --listen="127.0.0.1:%s" "%s/dist/h3studio.bin" -- "$@"\n' \
    "$H3_DLV" "$PWD" >dist/h3studio
  chmod +x dist/h3studio
  echo "dlv: the studio listens for a debugger on 127.0.0.1:$H3_DLV"
fi

stop
mkdir -p "$RUN"
echo $$ >"$PIDFILE"
exec "$HELM" dev -f helmstudio.yaml "$@"
