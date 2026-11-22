#!/usr/bin/env bash
# Builds everything a release carries into ./out:
#   the server for Windows and Linux, the Windows desktop window, and
#   printharbor.json (build number + SHA-256 of each file), which running
#   copies of PrintHarbor read to see that a newer build is out.
set -euo pipefail
cd "$(dirname "$0")/.."
OUT=${OUT:-out}; rm -rf "$OUT"; mkdir -p "$OUT"
build=$(sed -n 's/^const Build = \([0-9]*\).*/\1/p' internal/api/app.go)
version=$(sed -n 's/^const Version = "\(.*\)".*/\1/p' internal/api/*.go | head -1)
appbuild=$(sed -n 's/^const Build = \([0-9]*\).*/\1/p' internal/appver/*.go)
[ -n "$build" ] && [ -n "$appbuild" ] || { echo "could not read the build numbers" >&2; exit 1; }
export CGO_ENABLED=0
b() { GOOS=$1 GOARCH=$2 go build -trimpath -ldflags "-s -w $5" -o "$OUT/$3" "$4"; }
b windows amd64 printharbor-windows-amd64.exe ./cmd/printharbor ""
b windows amd64 printharbor-app.exe ./cmd/printharbor-app "-H windowsgui"
b linux amd64 printharbor-linux-amd64 ./cmd/printharbor ""
b linux arm64 printharbor-linux-arm64 ./cmd/printharbor ""
entry() { printf '{"name":"%s","sha256":"%s","size":%s}' "$1" "$(sha256sum "$OUT/$1" | cut -d' ' -f1)" "$(stat -c %s "$OUT/$1")"; }
notes=""; [ -f release-notes.txt ] && notes=$(cat release-notes.txt)
jq -n --argjson build "$build" --arg version "$version" --argjson app "$appbuild" --arg notes "$notes" \
  --argjson w "$(entry printharbor-windows-amd64.exe)" --argjson aw "$(entry printharbor-app.exe)" \
  --argjson l "$(entry printharbor-linux-amd64)" --argjson la "$(entry printharbor-linux-arm64)" \
  '{build:$build,version:$version,app_build:$app,notes:$notes,files:{"windows-amd64":$w,"app-windows-amd64":$aw,"linux-amd64":$l,"linux-arm64":$la}}' > "$OUT/printharbor.json"
echo "$build" > "$OUT/.build"
cat "$OUT/printharbor.json"; ls -lh "$OUT"
