#!/usr/bin/env bash
set -euo pipefail

# Find repo root by walking up from the script location until go.mod is found
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$SCRIPT_DIR"

while [[ "$ROOT" != "/" && ! -f "$ROOT/go.mod" ]]; do
  ROOT="$(cd "$ROOT/.." && pwd)"
done

if [[ ! -f "$ROOT/go.mod" ]]; then
  echo "ERROR: could not find go.mod above script dir: $SCRIPT_DIR"
  exit 1
fi

cd "$ROOT"

# Put artifacts OUTSIDE the repo by default (one level up)
OUTDIR="$(cd "$ROOT/.." && pwd)/autofetch-build/dist"

VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo "dev")"
LDFLAGS="-s -w -X main.Version=$VERSION"

HOST_GOOS="$(go env GOOS)"
HOST_GOARCH="$(go env GOARCH)"

# Optional icon for fyne package / metadata
ICON_PATH=""
if [[ -f "$ROOT/Icon.png" ]]; then
  ICON_PATH="$ROOT/Icon.png"
elif [[ -f "$ROOT/icon.png" ]]; then
  ICON_PATH="$ROOT/icon.png"
fi

GUI_DIR="$ROOT/cmd/autofetch-gui"

HAS_FYNE_APP_TOML=0
if [[ -f "$ROOT/FyneApp.toml" ]]; then
  HAS_FYNE_APP_TOML=1
fi

echo "==> Repo root: $ROOT"
echo "==> Output:    $OUTDIR"
echo "==> Host:      $HOST_GOOS/$HOST_GOARCH"

echo "==> Cleaning dist/"
rm -rf "$OUTDIR"
mkdir -p "$OUTDIR"

echo "==> go mod tidy / download"
go mod tidy
go mod download

linux_native_gui_env_args() {
  local -a env_args=()

  if [[ "$HOST_GOOS" != "linux" || "$HOST_GOARCH" != "amd64" ]]; then
    printf '%s\n'
    return 0
  fi

  local pkg_config_bin="/usr/bin/pkg-config"
  local cc_bin="/usr/bin/gcc"
  local multiarch_dir=""
  local pkg_config_path=""

  if [[ -x "$cc_bin" ]]; then
    multiarch_dir="$("$cc_bin" -dumpmachine 2>/dev/null || true)"
  fi

  if [[ -x "$pkg_config_bin" ]]; then
    pkg_config_path="$("$pkg_config_bin" --variable pc_path pkg-config 2>/dev/null || true)"
  fi

  if [[ -n "$multiarch_dir" && -d "/usr/lib/$multiarch_dir/pkgconfig" ]]; then
    if [[ -n "$pkg_config_path" ]]; then
      pkg_config_path="$pkg_config_path:/usr/lib/$multiarch_dir/pkgconfig"
    else
      pkg_config_path="/usr/lib/$multiarch_dir/pkgconfig"
    fi
  fi

  if [[ -d "/usr/lib/x86_64-linux-gnu/pkgconfig" ]]; then
    if [[ -n "$pkg_config_path" ]]; then
      pkg_config_path="$pkg_config_path:/usr/lib/x86_64-linux-gnu/pkgconfig"
    else
      pkg_config_path="/usr/lib/x86_64-linux-gnu/pkgconfig"
    fi
  fi

  if [[ -d "/usr/share/pkgconfig" ]]; then
    if [[ -n "$pkg_config_path" ]]; then
      pkg_config_path="$pkg_config_path:/usr/share/pkgconfig"
    else
      pkg_config_path="/usr/share/pkgconfig"
    fi
  fi

  if [[ -x "$pkg_config_bin" ]]; then
    env_args+=("PKG_CONFIG=$pkg_config_bin")
  fi
  if [[ -n "$pkg_config_path" ]]; then
    env_args+=("PKG_CONFIG_PATH=$pkg_config_path")
  fi
  if [[ -x "$cc_bin" ]]; then
    env_args+=("CC=$cc_bin")
  fi

  env_args+=(
    "CGO_CFLAGS=-I/usr/include"
    "CGO_CPPFLAGS=-I/usr/include"
  )

  printf '%s\n' "${env_args[@]}"
}

build_headless() {
  local target_goos="${1:-}"
  local target_goarch="${2:-}"
  local suffix="${3:-}"
  local outdir="${4:-}"
  local target_goarm="${5:-}"

  if [[ -z "$target_goos" || -z "$target_goarch" || -z "$outdir" ]]; then
    echo "ERROR: build_headless requires GOOS, GOARCH and OUTDIR"
    return 1
  fi

  echo "==> Building headless: autofetch ($target_goos/$target_goarch${target_goarm:+/goarm$target_goarm})"

  local -a env_args=(
    "CGO_ENABLED=0"
    "GOOS=$target_goos"
    "GOARCH=$target_goarch"
  )
  if [[ -n "$target_goarm" ]]; then
    env_args+=("GOARM=$target_goarm")
  fi

  env "${env_args[@]}" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$outdir/autofetch$suffix" ./cmd/autofetch
}

should_build_gui() {
  local target_goos="${1:-}"
  local target_goarch="${2:-}"
  [[ "$target_goos" == "$HOST_GOOS" && "$target_goarch" == "$HOST_GOARCH" ]]
}

package_macos_app() {
  local outdir="${1:-}"

  if [[ "$HOST_GOOS" != "darwin" ]]; then
    return 0
  fi

  if ! command -v fyne >/dev/null 2>&1; then
    echo "WARNING: fyne CLI not found; skipping .app packaging"
    echo "         Install with: go install fyne.io/tools/cmd/fyne@latest"
    return 0
  fi

  if [[ "$HAS_FYNE_APP_TOML" -eq 0 && -z "$ICON_PATH" ]]; then
    echo "WARNING: no FyneApp.toml and no Icon.png/icon.png found; skipping .app packaging"
    return 0
  fi

  echo "==> Packaging macOS app bundle"
  rm -rf "$outdir/autofetch.app" "$GUI_DIR/autofetch.app" "$ROOT/autofetch.app"
  rm -f "$outdir/autofetch-macos-app.zip"

  local -a fyne_args=(package -os darwin -name autofetch -release)
  if [[ -n "$ICON_PATH" ]]; then
    fyne_args+=(-icon "$ICON_PATH")
  fi
  if [[ "$HAS_FYNE_APP_TOML" -eq 0 ]]; then
    fyne_args+=(-app-id de.autofetch.client)
  fi

  if ! (
    cd "$GUI_DIR"
    fyne "${fyne_args[@]}"
  ); then
    echo "WARNING: fyne package failed; continuing without .app bundle"
    return 0
  fi

  local packaged_app=""
  if [[ -d "$GUI_DIR/autofetch.app" ]]; then
    packaged_app="$GUI_DIR/autofetch.app"
  elif [[ -d "$ROOT/autofetch.app" ]]; then
    packaged_app="$ROOT/autofetch.app"
  fi

  if [[ -n "$packaged_app" ]]; then
    mv "$packaged_app" "$outdir/autofetch.app"
    echo "==> Packaged: $outdir/autofetch.app"

    if command -v ditto >/dev/null 2>&1; then
      echo "==> Creating macOS app archive"
      ditto -c -k --sequesterRsrc --keepParent "$outdir/autofetch.app" "$outdir/autofetch-macos-app.zip"
      echo "==> Archived: $outdir/autofetch-macos-app.zip"
    else
      echo "WARNING: ditto not found; skipping macOS app zip archive"
    fi
  else
    echo "WARNING: fyne package did not produce autofetch.app"
  fi
}

build_gui() {
  local target_goos="${1:-}"
  local target_goarch="${2:-}"
  local suffix="${3:-}"
  local outdir="${4:-}"
  local target_goarm="${5:-}"

  if ! should_build_gui "$target_goos" "$target_goarch"; then
    echo "==> Skipping GUI: autofetch-gui ($target_goos/$target_goarch${target_goarm:+/goarm$target_goarm})"
    echo "    Reason: native GUI is only built for host platform $HOST_GOOS/$HOST_GOARCH"
    return 0
  fi

  echo "==> Building GUI: autofetch-gui ($target_goos/$target_goarch${target_goarm:+/goarm$target_goarm})"

  local -a env_args=(
    "CGO_ENABLED=1"
    "GOOS=$target_goos"
    "GOARCH=$target_goarch"
  )

  if [[ "$target_goos" == "linux" && "$target_goarch" == "amd64" && "$target_goos" == "$HOST_GOOS" && "$target_goarch" == "$HOST_GOARCH" ]]; then
    while IFS= read -r extra_env; do
      if [[ -n "$extra_env" ]]; then
        env_args+=("$extra_env")
      fi
    done < <(linux_native_gui_env_args)
  fi

  if [[ -n "$target_goarm" ]]; then
    env_args+=("GOARM=$target_goarm")
  fi

  env "${env_args[@]}" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$outdir/autofetch-gui$suffix" ./cmd/autofetch-gui

  if [[ "$target_goos" == "darwin" ]]; then
    package_macos_app "$outdir"
  fi
}

generate_checksums() {
  echo "==> Generating SHA256 checksums"

  while IFS= read -r -d '' file; do
    [[ -f "$file" ]] || continue

    echo "   -> $file"

    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum "$file" > "$file.sha256"
    else
      shasum -a 256 "$file" > "$file.sha256"
    fi
  done < <(find "$OUTDIR" -type f ! -name "*.sha256" ! -name "SHA256SUMS" -print0)

  local sums_file="$OUTDIR/SHA256SUMS"
  : > "$sums_file"

  while IFS= read -r -d '' file; do
    [[ -f "$file" ]] || continue

    local rel="${file#$OUTDIR/}"

    if command -v sha256sum >/dev/null 2>&1; then
      local sum
      sum="$(sha256sum "$file" | awk '{print $1}')"
      echo "$sum  $rel" >> "$sums_file"
    else
      local sum
      sum="$(shasum -a 256 "$file" | awk '{print $1}')"
      echo "$sum  $rel" >> "$sums_file"
    fi
  done < <(find "$OUTDIR" -type f ! -name "*.sha256" ! -name "SHA256SUMS" -print0)
}

build_pkg() {
  local target_goos="${1:-}"
  local target_goarch="${2:-}"
  local suffix="${3:-}"
  local target_goarm="${4:-}"

  local target="$target_goos-$target_goarch"
  if [[ -n "$target_goarm" ]]; then
    target="$target-goarm$target_goarm"
  fi

  local outdir="$OUTDIR/$target"
  mkdir -p "$outdir"

  echo "==> Building target: $target"

  build_headless "$target_goos" "$target_goarch" "$suffix" "$outdir" "$target_goarm"
  build_gui "$target_goos" "$target_goarch" "$suffix" "$outdir" "$target_goarm"
}

# Build native host target
build_pkg "$HOST_GOOS" "$HOST_GOARCH" ""

# Raspberry Pi 2 (ARMv7)
build_pkg linux arm "" 7

# Raspberry Pi 4 (64-bit OS)
build_pkg linux arm64 ""

# Linux Standard PCs (Intel/AMD)
build_pkg linux amd64 ""

# Windows
build_pkg windows 386 ".exe"
build_pkg windows amd64 ".exe"

# Generate checksums for all built artifacts
generate_checksums

echo "==> Build done: $OUTDIR"