#!/usr/bin/env bash
# Run BDP's Read conformance suite against `yardr bdp serve`.
#
#   BDP_REPO=~/src/bdp conformance/bdp/run.sh
#
# BDP_REPO is a gastownhall/bdp clone after
# `pnpm install --frozen-lockfile && pnpm build`. Last verified against bdp
# 182f1fc: 31/31 Read scenarios pass (BDP_REPORT keeps the per-scenario states).
# Needs go, python3 and pnpm. Everything runs in a temp dir on loopback;
# your ~/.yardr (or ~/.yard) is never touched.
set -euo pipefail
: "${BDP_REPO:?set BDP_REPO to a built gastownhall/bdp clone}"
BDP_REPO=$(cd "$BDP_REPO" && pwd)
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
work=$(mktemp -d)   # short path: the yardr server's unix socket lives here
scope=https://scope.example/acme/
port=${BDP_PORT:-18899}

export YARDR_HOME=$work/home
# Run from inside a yardr session (an agent's pane), the session's variables
# would make every seeded move a stale move of that session's bead.
for v in BEAD SESSION STAGE GROUP ACTOR; do unset "YARDR_$v" "YARD_$v"; done
unset YARD_HOME
cleanup() {
  [[ -n ${serve_pid:-} ]] && kill "$serve_pid" 2>/dev/null || true
  "$work/yardr" server stop >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

(cd "$root" && go build -o "$work/yardr" .)
# The graph BDP's bd-backed conformance lane seeds (seedBdWorkspace).
python3 "$here/seed.py" "$BDP_REPO/packages/conformance/fixtures/read-reference-v1.json" "$work/yardr" >/dev/null
"$work/yardr" bdp serve --addr "127.0.0.1:$port" --scope "$scope" >"$work/serve.log" 2>&1 &
serve_pid=$!
for _ in $(seq 50); do curl -fs -o /dev/null -H 'Host: scope.example' "http://127.0.0.1:$port/acme/bdp.json" && break; sleep 0.1; done

# bd's fixture with yard's link ids and revisions; see make-fixture.py.
python3 "$here/make-fixture.py" "$BDP_REPO/packages/conformance/fixtures/read-bdpbd-v1.json" \
  "$scope" "127.0.0.1:$port" "$work/fixture.json"

cd "$BDP_REPO"
BDP_REPO=$BDP_REPO BDP_TARGET_SCOPE=$scope BDP_TARGET_DIAL=127.0.0.1:$port \
BDP_FIXTURE=$work/fixture.json BDP_REPORT=${BDP_REPORT:-$work/report.json} \
  pnpm exec vitest run --config "$here/vitest.config.mts"
