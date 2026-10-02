#!/usr/bin/env bash
# Run BDP's Read conformance suite against `yard bdp serve`.
#
#   BDP_REPO=~/src/bdp conformance/bdp/run.sh
#
# BDP_REPO is a gastownhall/bdp clone after `pnpm install && pnpm build`.
# Needs go, python3 and pnpm. Everything runs in a temp dir on loopback;
# your ~/.yard is never touched.
set -euo pipefail
: "${BDP_REPO:?set BDP_REPO to a built gastownhall/bdp clone}"
BDP_REPO=$(cd "$BDP_REPO" && pwd)
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
work=$(mktemp -d)   # short path: the yard server's unix socket lives here
scope=https://scope.example/acme/
port=${BDP_PORT:-18899}

export YARD_HOME=$work/home
cleanup() {
  [[ -n ${serve_pid:-} ]] && kill "$serve_pid" 2>/dev/null || true
  "$work/yard" server stop >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

(cd "$root" && go build -o "$work/yard" .)
# The graph BDP's bd-backed conformance lane seeds (seedBdWorkspace).
python3 "$here/seed.py" "$BDP_REPO/packages/conformance/fixtures/read-reference-v1.json" "$work/yard" >/dev/null
"$work/yard" bdp serve --addr "127.0.0.1:$port" --scope "$scope" >"$work/serve.log" 2>&1 &
serve_pid=$!
for _ in $(seq 50); do curl -fs -o /dev/null -H 'Host: scope.example' "http://127.0.0.1:$port/acme/bdp.json" && break; sleep 0.1; done

# bd's fixture with yard's link ids and revisions; see make-fixture.py.
python3 "$here/make-fixture.py" "$BDP_REPO/packages/conformance/fixtures/read-bdpbd-v1.json" \
  "$scope" "127.0.0.1:$port" "$work/fixture.json"

cd "$BDP_REPO"
BDP_REPO=$BDP_REPO BDP_TARGET_SCOPE=$scope BDP_TARGET_DIAL=127.0.0.1:$port \
BDP_FIXTURE=$work/fixture.json BDP_REPORT=${BDP_REPORT:-$work/report.json} \
  pnpm exec vitest run --config "$here/vitest.config.mts"
