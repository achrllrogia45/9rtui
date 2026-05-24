#!/usr/bin/env bash
set -euo pipefail

REPO="${NRTUI_REPO:-achrllrogia45/9rtui}"
VERSION="${NRTUI_VERSION:-}"
BRANCH="${NRTUI_BRANCH:-}"
INSTALL_DIR="${NRTUI_INSTALL_DIR:-$HOME/.9rtui}"
BIN_DIR="${NRTUI_BIN_DIR:-$HOME/.local/bin}"
API_BASE="${NRTUI_API:-http://localhost:20128}"
DB_PATH="${NRTUI_DB:-$HOME/.9router/db/data.sqlite}"
CACHE_DIR="${NRTUI_CACHE_DIR:-$INSTALL_DIR/tmp}"
SRC_DIR="$CACHE_DIR/src"

ts() { printf '%s\n' "$*"; }
fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "missing dependency: $1"; }

resolve_version() {
  if [ -n "${VERSION:-}" ]; then return; fi
  api="https://api.github.com/repos/$REPO/releases/latest"
  ts "resolve latest release: $api"
  VERSION="$(curl -fsSL -H 'User-Agent: 9rtui-installer' "$api" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
  [ -n "$VERSION" ] || fail "failed to resolve latest release; set NRTUI_VERSION"
}

download_file() {
  url="$1"; dst="$2"
  tmp="$dst.tmp"
  rm -f "$tmp"
  curl -fL --progress-bar "$url" -o "$tmp"
  mv -f "$tmp" "$dst"
}

if [ "${EUID:-$(id -u)}" = "0" ] && [ -z "${NRTUI_ALLOW_SUDO:-}" ]; then
  fail "do not run installer with sudo; set NRTUI_ALLOW_SUDO=1 only if intentional"
fi

need curl
need tar
need mkdir
need rm
need cp
need chmod

resolve_version
SOURCE_REF="${BRANCH:-$VERSION}"
mkdir -p "$INSTALL_DIR/.accounts" "$INSTALL_DIR/.tui-logs/full-backups" "$CACHE_DIR" "$BIN_DIR"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$os" in linux) os="linux" ;; darwin) os="darwin" ;; *) fail "unsupported OS: $os" ;; esac
case "$arch" in x86_64|amd64) arch="amd64" ;; aarch64|arm64) arch="arm64" ;; *) fail "unsupported arch: $arch" ;; esac
asset="9rtui-${os}-${arch}"
exe="$INSTALL_DIR/9rtui"

install_configs() {
  if [ ! -f "$INSTALL_DIR/.env" ]; then printf 'WEB_PASS=\n' > "$INSTALL_DIR/.env"; chmod 600 "$INSTALL_DIR/.env" || true; fi
  if [ ! -f "$INSTALL_DIR/9rtui.ini" ]; then
    cat > "$INSTALL_DIR/9rtui.ini" <<EOF
project_dir = .
db_path = $DB_PATH
log_dir = ./.tui-logs
api_base = $API_BASE
accounts_path = ./.accounts/
dev_mode = false
EOF
    chmod 600 "$INSTALL_DIR/9rtui.ini" || true
  fi
  ln -sf "$exe" "$BIN_DIR/9rtui"
}

sync_scripts() {
  archive="$CACHE_DIR/9rtui-$SOURCE_REF.tar.gz"
  if [ -n "${BRANCH:-}" ]; then
    url="https://github.com/$REPO/archive/refs/heads/$BRANCH.tar.gz"
  else
    url="https://github.com/$REPO/archive/refs/tags/$SOURCE_REF.tar.gz"
  fi
  if [ ! -f "$archive" ] || [ "${NRTUI_REFRESH:-0}" = "1" ]; then ts "download scripts/source: $url"; download_file "$url" "$archive"; fi
  rm -rf "$SRC_DIR"; mkdir -p "$SRC_DIR"; tar -xzf "$archive" -C "$SRC_DIR" --strip-components=1
  rm -rf "$INSTALL_DIR/scripts"; cp -R "$SRC_DIR/scripts" "$INSTALL_DIR/scripts"; chmod -R u+rwX,go-rwx "$INSTALL_DIR/scripts" || true
}

build_from_source() {
  need tar
  if ! command -v go >/dev/null 2>&1; then fail "go missing; install Go or use prebuilt release installer without NRTUI_BUILD_FROM_SOURCE=1"; fi
  sync_scripts
  cd "$SRC_DIR"
  go build -trimpath -ldflags "-s -w -X main.version=$VERSION -X main.commit=source -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o "$exe" .
}

if [ "${NRTUI_BUILD_FROM_SOURCE:-0}" = "1" ]; then
  ts "build from source"
  build_from_source
else
  url="https://github.com/$REPO/releases/download/$VERSION/$asset"
  cached="$CACHE_DIR/$asset-$VERSION"
  if [ ! -f "$cached" ] || [ "${NRTUI_REFRESH:-0}" = "1" ]; then ts "download binary: $url"; download_file "$url" "$cached"; fi
  cp -f "$cached" "$exe"
  chmod 0755 "$exe"
  sync_scripts
fi

install_configs
ts "version:   $VERSION"
ts "installed: $exe"
ts "scripts:   $INSTALL_DIR/scripts"
ts "config:    $INSTALL_DIR/9rtui.ini"
ts "run:       9rtui"
case ":$PATH:" in *":$BIN_DIR:"*) ;; *) ts "NOTE: $BIN_DIR not in PATH" ;; esac
