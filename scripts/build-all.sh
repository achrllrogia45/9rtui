#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SHARE="${NRTUI_PC_SHARE:-/home/hilman/pc-share/9rtui}"
OWNER="${NRTUI_OWNER:-hilman:hilman}"
cd "$ROOT"
VERSION="${NRTUI_VERSION:-v0.2.0-beta.1}"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-X main.version=$VERSION -X main.commit=$COMMIT -X main.buildDate=$BUILD_DATE"

if ! command -v go >/dev/null 2>&1; then
  echo "ERROR: go not found" >&2
  exit 1
fi

echo "==> test"
go test ./...

echo "==> build linux: $ROOT/9rtui"
go build -ldflags "$LDFLAGS" -o "$ROOT/9rtui" .

echo "==> build windows: $ROOT/9rtui.exe"
GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$ROOT/9rtui.exe" .

echo "==> copy windows exe to share: $SHARE/9rtui.exe"
mkdir -p "$SHARE" 2>/dev/null || sudo mkdir -p "$SHARE"
COPY_OK=1
if ! cp -f "$ROOT/9rtui.exe" "$SHARE/9rtui.exe" 2>/dev/null; then
  if ! sudo cp -f "$ROOT/9rtui.exe" "$SHARE/9rtui.exe"; then
    COPY_OK=0
    echo "WARNING: copy failed; Windows/share likely locked $SHARE/9rtui.exe" >&2
  fi
fi

echo "==> fix project ownership: $OWNER"
if command -v sudo >/dev/null 2>&1; then
  sudo chown -R "$OWNER" "$ROOT"
else
  chown -R "$OWNER" "$ROOT" || true
fi

echo "==> verify"
file "$ROOT/9rtui" "$ROOT/9rtui.exe"
if [ "$COPY_OK" -eq 1 ] && [ -f "$SHARE/9rtui.exe" ]; then
  file "$SHARE/9rtui.exe"
fi
if find "$ROOT" -user root -o -group root | grep -q .; then
  echo "WARNING: root-owned project paths remain:" >&2
  find "$ROOT" -user root -o -group root | head -50 >&2
else
  echo "ownership OK: no root-owned project paths"
fi

if [ "$COPY_OK" -eq 1 ]; then
  echo "DONE"
else
  echo "DONE, but share copy failed; close Windows 9rtui.exe or fix share permissions, then rerun this script" >&2
fi
