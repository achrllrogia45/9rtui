#!/usr/bin/env bash
set -euo pipefail

# 9rtui installer (dummy repo placeholder)
# Usage: curl -fsSL https://raw.githubusercontent.com/OWNER/9rtui/main/install.sh | bash

REPO="${NINETUI_REPO:-OWNER/9rtui}"
INSTALL_DIR="${NINETUI_INSTALL_DIR:-$HOME/.9rtui}"
BIN_DIR="${NINETUI_BIN_DIR:-$HOME/.local/bin}"
API_BASE="${NINETUI_API:-http://localhost:20128}"
DB_PATH="${NINETUI_DB:-$HOME/.9router/db/data.sqlite}"

say() { printf '%s\n' "$*"; }
fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "missing dependency: $1"; }

need curl
need chmod
need mkdir
need mv
need ln

uname_s="$(uname -s 2>/dev/null || echo unknown)"
uname_m="$(uname -m 2>/dev/null || echo unknown)"

case "$uname_s" in
  Linux*) os="linux" ;;
  Darwin*) os="darwin" ;;
  *) fail "unsupported OS: $uname_s" ;;
esac

case "$uname_m" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) fail "unsupported architecture: $uname_m" ;;
esac

asset="9rtui-${os}-${arch}"
exe="$INSTALL_DIR/9rtui"
link="$BIN_DIR/9rtui"

say "Installing 9rtui"
say "  repo:        $REPO"
say "  asset:       $asset"
say "  install dir: $INSTALL_DIR"

mkdir -p "$INSTALL_DIR/.accounts" \
         "$INSTALL_DIR/.tui-logs/full-backups" \
         "$INSTALL_DIR/.dev" \
         "$INSTALL_DIR/.reports" \
         "$BIN_DIR"

env_file="$INSTALL_DIR/.env"
if [ ! -f "$env_file" ]; then
  printf 'WEB_PASS=\n' > "$env_file"
  chmod 600 "$env_file" || true
fi

ini="$INSTALL_DIR/9rtui.ini"
if [ ! -f "$ini" ]; then
  cat > "$ini" <<EOF
# 9rtui settings
[paths]
project_dir = $INSTALL_DIR
db_path = $DB_PATH
log_dir = $INSTALL_DIR/.tui-logs
api_base = $API_BASE
accounts_path = $INSTALL_DIR/.accounts/
dev_mode = false
EOF
  chmod 600 "$ini" || true
fi

api_url="https://api.github.com/repos/${REPO}/releases/latest"
asset_url="$(curl -fsSL "$api_url" \
  | grep 'browser_download_url' \
  | grep "${asset}" \
  | cut -d '"' -f 4 \
  | head -n 1 || true)"

if [ -z "$asset_url" ]; then
  fail "release asset not found: $asset (dummy repo? set NINETUI_REPO=owner/repo)"
fi

tmp="$exe.tmp"
curl -fL --progress-bar "$asset_url" -o "$tmp"
chmod 0755 "$tmp"

if [ -f "$exe" ]; then
  mv -f "$exe" "$exe.previous"
fi
mv -f "$tmp" "$exe"
ln -sf "$exe" "$link"

say "Installed: $exe"
say "Linked:    $link"
say "Config:    $ini"
say "Run:       9rtui"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) say "NOTE: $BIN_DIR not in PATH. Add it or restart shell after updating PATH." ;;
esac
