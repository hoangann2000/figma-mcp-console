#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."          # về repo root

OUT=npm/bin
rm -rf "$OUT"
echo "→ build 6 binary..."
for t in "darwin arm64" "darwin amd64" "linux arm64" "linux amd64" "windows amd64" "windows arm64"; do
  set -- $t; GOOS=$1; GOARCH=$2; ext=""
  [ "$GOOS" = "windows" ] && ext=".exe"
  echo "   $GOOS-$GOARCH"
  CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH \
    go build -trimpath -ldflags="-s -w" \
    -o "$OUT/${GOOS}-${GOARCH}/figma-mcp${ext}" ./cmd/figma-mcp
done

echo "→ copy plugin..."
mkdir -p "$OUT/plugin"
cp plugin/code.js plugin/ui.html plugin/manifest.json "$OUT/plugin/"
echo "✅ xong → npm/bin/"