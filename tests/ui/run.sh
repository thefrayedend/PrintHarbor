#!/bin/bash
# Runs the browser tests against a freshly built PrintHarbor on a throw-away data folder.
#
#   tests/ui/run.sh            build, start, run every test, compare the slicer with baseline.json
#   tests/ui/run.sh --record   the same, then save this run as the new baseline.json
#                              (after a change that is MEANT to alter what the slicer page does)
#
# Needs Go, Node, Python 3 (the pretend slicer) and Playwright with its Chromium:
#   npm install -g playwright && npx playwright install chromium
# Nothing here touches your real PrintHarbor, its data, or the internet.
set -u
cd "$(dirname "$0")/../.."
OUT=${OUT:-/tmp/printharbor-ui-test}
PORT=${PORT:-7791}
BASE=http://127.0.0.1:$PORT
export NO_PROXY=127.0.0.1,localhost no_proxy=127.0.0.1,localhost
rm -rf "$OUT"; mkdir -p "$OUT"
echo "== building"
go build -o "$OUT/printharbor" ./cmd/printharbor || exit 1
tests/ui/mkfixture.sh "$OUT/fx" "$PORT" || exit 1
(cd "$OUT/fx" && exec "$OUT/printharbor" -config "$OUT/fx/config.json" -no-tray > "$OUT/server.log" 2>&1) &
SERVER=$!
trap 'kill $SERVER 2>/dev/null' EXIT
for i in $(seq 1 50); do curl -s --noproxy '*' "$BASE/api/info" > /dev/null && break; sleep 0.2; done
curl -s --noproxy '*' -H "Origin: $BASE" -H 'Content-Type: application/json' \
  -d '{"name":"Mini","driver":"mock","address":"60","machine_preset":"Acme Mini 0.4 nozzle","slots":[{"material":"PLA","color":"#ff0000"},{"material":"PLA","color":"#0000ff"}]}' \
  "$BASE/api/printers" > /dev/null || { echo "PrintHarbor did not start; see $OUT/server.log"; exit 1; }
FAIL=0
echo "== the app window's script (boot.js)"
node tests/ui/boot.test.js || FAIL=1
echo "== the slicer page"
node tests/ui/slicer.scenario.js "$PORT" "$OUT/slicer.json" "$OUT/fx" || FAIL=1
if [ "${1:-}" = "--record" ]; then
  cp "$OUT/slicer.json" tests/ui/baseline.json && echo "saved as the new baseline"
elif [ -f tests/ui/baseline.json ]; then
  node tests/ui/compare.js tests/ui/baseline.json "$OUT/slicer.json" || { FAIL=1; echo "The slicer page no longer does what the baseline recorded. If that was intended, run: tests/ui/run.sh --record"; }
fi
echo "== the main page"
node tests/ui/ui.test.js "$PORT" "$OUT/fx" || FAIL=1
echo
if [ $FAIL = 0 ]; then echo "ALL BROWSER TESTS PASSED"; else echo "SOME BROWSER TESTS FAILED (server log: $OUT/server.log)"; fi
exit $FAIL
