# Shared helpers for the leash demos. Source this from a demo script:
#
#   source "$(dirname "$0")/_lib.sh"
#   demo_build
#   demo_bg "$FAKEUP" --listen 127.0.0.1:9099
#   demo_wait 127.0.0.1:9099
#
# It builds leash and the fake upstream into a temp dir, tracks background
# processes, and tears everything down on exit. Nothing real is billed.
set -euo pipefail

DEMO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DEMO_TMP="$(mktemp -d)"
DEMO_PIDS=()

demo_cleanup() {
  local p
  for p in "${DEMO_PIDS[@]:-}"; do kill "$p" >/dev/null 2>&1 || true; done
  rm -rf "$DEMO_TMP"
}
trap demo_cleanup EXIT INT TERM

LEASH="$DEMO_TMP/leash"
FAKEUP="$DEMO_TMP/fakeupstream"
DB="$DEMO_TMP/leash.db"
PRICES="$DEMO_ROOT/examples/prices.demo.json"

# Uncommon ports, to avoid colliding with a real gateway or the compose demo.
GATE=127.0.0.1:18088
ADMIN=127.0.0.1:19090
UPSTREAM=127.0.0.1:19099

# demo_require_free HOST:PORT  - fail early if something is already listening,
# so a stale process can never silently hijack the demo.
demo_require_free() {
  local host="${1%:*}" port="${1##*:}"
  if (exec 3<>"/dev/tcp/$host/$port") 2>/dev/null; then
    exec 3>&- 3<&-
    echo "port $1 is already in use; stop whatever is listening and retry" >&2
    exit 1
  fi
}

demo_build() {
  echo "# building leash + fakeupstream (one-time) ..."
  ( cd "$DEMO_ROOT" && go build -o "$LEASH" ./cmd/leash && go build -o "$FAKEUP" ./examples/fakeupstream )
}

# demo_bg CMD ARGS...  - run a command in the background, tracked for cleanup.
# Output goes to a log so a startup failure can be surfaced by demo_wait.
demo_bg() { "$@" >>"$DEMO_TMP/bg.log" 2>&1 & DEMO_PIDS+=("$!"); }

# demo_wait HOST:PORT  - block until the TCP port accepts a connection.
demo_wait() {
  local host="${1%:*}" port="${1##*:}" i
  for i in $(seq 1 100); do
    if (exec 3<>"/dev/tcp/$host/$port") 2>/dev/null; then exec 3>&- 3<&-; return 0; fi
    sleep 0.1
  done
  echo "timed out waiting for $1; server log:" >&2
  cat "$DEMO_TMP/bg.log" >&2 || true
  return 1
}

demo_banner() { printf '\n=== %s ===\n' "$*"; }
demo_run() { printf '$ %s\n' "$*"; }

# demo_post URL TOKEN LOOPID BODY  - POST and print "HTTP <code>  <body>".
# TOKEN or LOOPID may be empty to omit that header.
demo_post() {
  local url="$1" tok="$2" loop="$3" body="$4" code
  local args=(-s -o "$DEMO_TMP/resp" -w '%{http_code}' -X POST "$url" -d "$body")
  [ -n "$tok" ] && args+=(-H "X-Leash-Token: $tok")
  [ -n "$loop" ] && args+=(-H "X-Loop-Id: $loop")
  code=$(curl "${args[@]}")
  printf 'HTTP %s  %s\n' "$code" "$(head -c 200 "$DEMO_TMP/resp")"
}
