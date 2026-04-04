#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

APP="autofetch"
DIST="$ROOT/dist"
PKG="$ROOT/packages"
README="$ROOT/README.md"

if [[ ! -f "$README" ]]; then
  echo "README.md not found in repo root"
  exit 1
fi

echo "==> Cleaning packages/"
rm -rf "$PKG"
mkdir -p "$PKG"

for dir in "$DIST"/*; do
  [[ -d "$dir" ]] || continue

  name="$(basename "$dir")"
  echo "==> Packaging $name"

  tmp="$(mktemp -d)"
  cp "$dir/$APP"* "$tmp/"
  cp "$README" "$tmp/README.md"

  if [[ "$name" == *windows* ]]; then
    (cd "$tmp" && zip -9 -r "$PKG/$name.zip" .)
  else
    (cd "$tmp" && tar czf "$PKG/$name.tar.gz" .)
  fi

  rm -rf "$tmp"
done

echo "==> Packages created in: $PKG"

