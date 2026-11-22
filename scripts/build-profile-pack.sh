#!/usr/bin/env bash
# Builds a PrintHarbor "profile pack" from OrcaSlicer's profiles folder:
#   build-profile-pack.sh <resources/profiles dir> <out dir> <label> [<ref text>]
# writes <out>/orca-profiles-<label>.zip (only the .json files) and
# <out>/orca-profiles-<label>.json (what PrintHarbor reads to see if anything
# changed). The id depends only on the profiles' contents, so an unchanged
# OrcaSlicer gives an unchanged id.
set -euo pipefail
src=$(cd "$1" && pwd); out=$(mkdir -p "$2" && cd "$2" && pwd); label=$3; ref=${4:-$label}
cd "$src"
list=$(mktemp)
find . -type f -name '*.json' | sed 's|^\./||' | LC_ALL=C sort > "$list"
files=$(wc -l < "$list")
[ "$files" -ge 1000 ] || { echo "only $files profile files found in $src" >&2; exit 1; }
id=$(xargs -d '\n' sha256sum < "$list" | sha256sum | cut -d' ' -f1)
zip="orca-profiles-$label.zip"
rm -f "$out/$zip"
xargs -d '\n' touch -d '2020-01-01T00:00:00Z' < "$list"
TZ=UTC zip -q -X -9 "$out/$zip" -@ < "$list"
sum=$(sha256sum "$out/$zip" | cut -d' ' -f1)
cat > "$out/orca-profiles-$label.json" <<JSON
{"id":"$id","ref":"$ref","built":"$(date -u +%Y-%m-%dT%H:%M:%SZ)","zip":"$zip","sha256":"$sum","files":$files}
JSON
rm -f "$list"
echo "pack $label: $files files, id ${id:0:12}, $(du -h "$out/$zip" | cut -f1)"
