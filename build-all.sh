#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

APP="autofetch"
OUTDIR="${OUTDIR:-$(cd "$ROOT/.." && pwd)/autofetch-build/dist}"
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo "dev")"
MAIN_PKG="./cmd/autofetch"
MODULE_PATH="$(go list -m)"
BUILD_COMMIT="$(git rev-parse --short=7 HEAD 2>/dev/null || echo unknown)"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
BASE_LDFLAGS="-s -w -X ${MODULE_PATH}/internal/buildinfo.Version=${VERSION#v} -X ${MODULE_PATH}/internal/buildinfo.BuildCommit=$BUILD_COMMIT -X ${MODULE_PATH}/internal/buildinfo.BuildDate=$BUILD_DATE -X ${MODULE_PATH}/internal/buildinfo.Variant=headless"

rm -rf "$OUTDIR"
mkdir -p "$OUTDIR"
build() {
  local target_goos="$1" target_goarch="$2" suffix="$3" target_goarm="${4:-}" language="$5"
  local name="$APP-$target_goos-$target_goarch"
  [[ -n "$target_goarm" ]] && name="$name-goarm$target_goarm"
  name="$name-$language"
  local outdir="$OUTDIR/$name"
  mkdir -p "$outdir"

  local -a env_args=("CGO_ENABLED=0" "GOOS=$target_goos" "GOARCH=$target_goarch")
  [[ -n "$target_goarm" ]] && env_args+=("GOARM=$target_goarm")
  echo "==> Building $name"
  env "${env_args[@]}" go build -trimpath \
    -ldflags "$BASE_LDFLAGS -X ${MODULE_PATH}/internal/buildinfo.Language=$language" \
    -o "$outdir/$APP-$language$suffix" "$MAIN_PKG"
}

for language in de en; do
  build darwin amd64 "" "" "$language"
  build darwin arm64 "" "" "$language"
  build linux amd64 "" "" "$language"
  build linux arm "" 7 "$language"
  build linux arm64 "" "" "$language"
  build windows 386 ".exe" "" "$language"
  build windows amd64 ".exe" "" "$language"
done

echo "==> Build done: $OUTDIR"
