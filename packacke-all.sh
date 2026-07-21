#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

APP="autofetch"
DIST="${DIST:-$(cd "$ROOT/.." && pwd)/autofetch-build/dist}"
PKG="${PKG:-$(cd "$ROOT/.." && pwd)/autofetch-build/packages}"
README="$ROOT/README.md"
README_DE="$ROOT/README.de.md"

if [[ ! -f "$README" || ! -f "$README_DE" ]]; then
  echo "README.md or README.de.md not found in repo root"
  exit 1
fi
if [[ ! -d "$DIST" ]]; then
  echo "Build directory not found: $DIST"
  exit 1
fi

rm -rf "$PKG"
mkdir -p "$PKG"

for dir in "$DIST"/*; do
  [[ -d "$dir" ]] || continue
  name="$(basename "$dir")"
  echo "==> Packaging $name"
  tmp="$(mktemp -d)"
  find "$dir" -maxdepth 1 -type f \( -name "$APP*" -o -name "*.sha256" \) -exec cp {} "$tmp/" \;
  cp "$README" "$tmp/README.md"
  cp "$README_DE" "$tmp/README.de.md"

  if [[ "$name" == *windows* ]]; then
    (cd "$tmp" && zip -9 -r "$PKG/$name.zip" .)
  else
    (cd "$tmp" && tar czf "$PKG/$name.tar.gz" .)
  fi
  rm -rf "$tmp"
done

echo "==> Packages created in: $PKG"
