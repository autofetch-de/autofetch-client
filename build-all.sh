#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

APP="autofetch"
OUTDIR="$(cd "$ROOT/.." && pwd)/autofetch-build/dist"
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo "dev")"
MAIN_PKG="./cmd/autofetch"
MODULE_PATH="$(go list -m)"
BUILD_COMMIT="$(git rev-parse --short=7 HEAD 2>/dev/null || echo unknown)"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-s -w -X ${MODULE_PATH}/internal/buildinfo.Version=${VERSION#v} -X ${MODULE_PATH}/internal/buildinfo.BuildCommit=$BUILD_COMMIT -X ${MODULE_PATH}/internal/buildinfo.BuildDate=$BUILD_DATE"

echo "==> Cleaning dist/"
rm -rf "$OUTDIR"
mkdir -p "$OUTDIR"

echo "==> go mod tidy / download"
go mod tidy
go mod download

build() {
  local target_goos="${1:-}"
  local target_goarch="${2:-}"
  local suffix="${3:-}"
  local target_goarm="${4:-}"

  if [[ -z "$target_goos" || -z "$target_goarch" ]]; then
    echo "ERROR: build requires GOOS and GOARCH"
    return 1
  fi

  local name="$APP-$target_goos-$target_goarch"
  if [[ -n "$target_goarm" ]]; then
    name="$name-goarm$target_goarm"
  fi

  local outdir="$OUTDIR/$name"
  local outfile="$outdir/$APP$suffix"

  mkdir -p "$outdir"

  echo "==> Building $name"

  local -a env_args=(
    "CGO_ENABLED=0"
    "GOOS=$target_goos"
    "GOARCH=$target_goarch"
  )
  if [[ -n "$target_goarm" ]]; then
    env_args+=("GOARM=$target_goarm")
  fi

  env "${env_args[@]}" \
    go build -trimpath -ldflags "$LDFLAGS -X ${MODULE_PATH}/internal/buildinfo.Variant=headless" -o "$outfile" "$MAIN_PKG"
}

# macOS (Intel)
build darwin amd64 ""

# Raspberry Pi 2 (ARMv7)
build linux arm "" 7

# Raspberry Pi 4 (64-bit OS)
build linux arm64 ""

# Windows
build windows 386 ".exe"
build windows amd64 ".exe"

echo "==> Build done: $OUTDIR"
