#!/usr/bin/env bash
# Proves a running PrintHarbor can really slice: adds a simulated printer,
# uploads a 20 mm cube and waits for OrcaSlicer to turn it into G-code.
#   smoke-test.sh [http://127.0.0.1:7621] ["Bambu Lab A1 0.4 nozzle"]
set -euo pipefail
B=${1:-http://127.0.0.1:7621}; MACHINE=${2:-Bambu Lab A1 0.4 nozzle}
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

for i in $(seq 1 90); do curl -fsS "$B/api/info" -o "$tmp/info.json" 2>/dev/null && break; sleep 2; done
[ -s "$tmp/info.json" ] || { echo "PrintHarbor did not answer at $B" >&2; exit 1; }
jq -r '"PrintHarbor \(.version) build \(.build_number // "?"): Orca found=\(.orca_found), \(.printer_profiles) printer profiles"' "$tmp/info.json"
[ "$(jq -r .orca_found "$tmp/info.json")" = true ] || { echo "OrcaSlicer was not found" >&2; exit 1; }
[ "$(jq -r .printer_profiles "$tmp/info.json")" -gt 100 ] || { echo "no printer profiles" >&2; exit 1; }

# a 20 mm cube as an ASCII STL
python3 - "$tmp/cube.stl" <<'PY'
import sys
v=[(0,0,0),(20,0,0),(20,20,0),(0,20,0),(0,0,20),(20,0,20),(20,20,20),(0,20,20)]
f=[(0,2,1),(0,3,2),(4,5,6),(4,6,7),(0,1,5),(0,5,4),(1,2,6),(1,6,5),(2,3,7),(2,7,6),(3,0,4),(3,4,7)]
with open(sys.argv[1],'w') as o:
    o.write('solid cube\n')
    for a,b,c in f:
        o.write('facet normal 0 0 0\nouter loop\n')
        for i in (a,b,c): o.write('vertex %d %d %d\n'%v[i])
        o.write('endloop\nendfacet\n')
    o.write('endsolid cube\n')
PY

post() { curl -fsS -H 'Content-Type: application/json' -d "$2" "$B$1"; }
pid=$(post /api/printers "$(jq -n --arg m "$MACHINE" '{name:"Smoke test",driver:"mock",address:"1",machine_preset:$m}')" | jq -r '.id // .ID')
proj=$(curl -fsS -F "file=@$tmp/cube.stl" "$B/api/projects" | jq -r '.id // .ID')
echo "printer $pid, project $proj"
sug=$(curl -fsS "$B/api/projects/$proj/suggest?printer=$pid")
echo "profiles: $(echo "$sug" | jq -c '{process:.process_preset, filaments:.filament_presets}')"
job=$(post /api/jobs "$(echo "$sug" | jq --arg p "$proj" --arg pr "$pid" '{project_id:$p,plate:1,printer_id:$pr,process_preset:.process_preset,filament_presets:.filament_presets,send:false,start:false}')" | jq -r '.id // .ID')
for i in $(seq 1 150); do
  st=$(curl -fsS "$B/api/jobs" | jq -c --arg j "$job" '.[] | select((.id // .ID)==$j) | {state:(.state // .State), error:(.error // .Error)}')
  case "$(echo "$st" | jq -r .state)" in
    sliced|ready|done|waiting|preview) echo "sliced: $st"; curl -fsS -X DELETE "$B/api/printers/$pid" >/dev/null || true; exit 0 ;;
    failed) echo "slice failed: $st" >&2; exit 1 ;;
  esac
  sleep 2
done
echo "the slice did not finish in 5 minutes: $st" >&2; exit 1
