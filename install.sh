#!/usr/bin/env bash
set -euo pipefail

REPO="${NRTUI_REPO:-achrllrogia45/9rtui}"
BRANCH="${NRTUI_BRANCH:-main}"
INSTALL_DIR="${NRTUI_INSTALL_DIR:-$HOME/.9rtui}"
BIN_DIR="${NRTUI_BIN_DIR:-$HOME/.local/bin}"
API_BASE="${NRTUI_API:-http://localhost:20128}"
DB_PATH="${NRTUI_DB:-$HOME/.9router/db/data.sqlite}"
CACHE_DIR="${NRTUI_CACHE_DIR:-$INSTALL_DIR/tmp}"
SRC_DIR="$CACHE_DIR/src"

ts() { printf '%s\n' "$*"; }
fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "missing dependency: $1"; }

if [ "${EUID:-$(id -u)}" = "0" ] && [ -z "${NRTUI_ALLOW_SUDO:-}" ]; then
  fail "do not run installer with sudo; set NRTUI_ALLOW_SUDO=1 only if intentional"
fi

need curl
need tar
need mkdir
need rm
need cp
need chmod

mkdir -p "$INSTALL_DIR/.accounts" "$INSTALL_DIR/.tui-logs/full-backups" "$CACHE_DIR" "$BIN_DIR"

ensure_go() {
  if command -v go >/dev/null 2>&1; then
    return
  fi
  local_go="$INSTALL_DIR/cache/go"
  if [ -x "$local_go/bin/go" ]; then
    export PATH="$local_go/bin:$PATH"
    return
  fi
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) fail "unsupported architecture for local Go install: $arch" ;;
  esac
  case "$os" in
    linux|darwin) ;;
    *) fail "unsupported OS for local Go install: $os" ;;
  esac
  ts "go not found; installing local Go into $local_go"
  go_file="$(curl -fsSL 'https://go.dev/dl/?mode=json' | python3 -c 'import json,sys; os=sys.argv[1]; arch=sys.argv[2]; data=json.load(sys.stdin); print(next(f["filename"] for f in data[0]["files"] if f["os"]==os and f["arch"]==arch and f["filename"].endswith(".tar.gz")))' "$os" "$arch")"
  go_archive="$CACHE_DIR/$go_file"
  if [ ! -f "$go_archive" ] || [ "${NRTUI_REFRESH:-0}" = "1" ]; then
    ts "download: https://go.dev/dl/$go_file"
    curl -fL --progress-bar "https://go.dev/dl/$go_file" -o "$go_archive"
  else
    ts "using cached Go archive: $go_archive"
  fi
  rm -rf "$local_go" "$CACHE_DIR/go-extract"
  mkdir -p "$CACHE_DIR/go-extract" "$(dirname "$local_go")"
  tar -xzf "$go_archive" -C "$CACHE_DIR/go-extract"
  mv "$CACHE_DIR/go-extract/go" "$local_go"
  export PATH="$local_go/bin:$PATH"
}

ensure_go

archive="$CACHE_DIR/9rtui-$BRANCH.tar.gz"
url="https://github.com/$REPO/archive/refs/heads/$BRANCH.tar.gz"
if [ ! -f "$archive" ] || [ "${NRTUI_REFRESH:-0}" = "1" ]; then
  ts "download: $url"
  curl -fL --progress-bar "$url" -o "$archive"
else
  ts "using cached source archive: $archive"
fi

rm -rf "$SRC_DIR"
mkdir -p "$SRC_DIR"
tar -xzf "$archive" -C "$SRC_DIR" --strip-components=1

cd "$SRC_DIR"
ts "build: $INSTALL_DIR/9rtui"
go build -trimpath -ldflags "-s -w -X main.version=$BRANCH -X main.commit=$(git rev-parse --short HEAD 2>/dev/null || echo source) -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o "$INSTALL_DIR/9rtui" .
chmod 0755 "$INSTALL_DIR/9rtui"

ts "sync scripts"
rm -rf "$INSTALL_DIR/scripts"
cp -R "$SRC_DIR/scripts" "$INSTALL_DIR/scripts"
chmod -R u+rwX,go-rwx "$INSTALL_DIR/scripts" || true

if [ ! -f "$INSTALL_DIR/.env" ]; then
  printf 'WEB_PASS=\n' > "$INSTALL_DIR/.env"
  chmod 600 "$INSTALL_DIR/.env" || true
fi

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

ln -sf "$INSTALL_DIR/9rtui" "$BIN_DIR/9rtui"

ts "installed: $INSTALL_DIR/9rtui"
ts "scripts:   $INSTALL_DIR/scripts"
ts "config:    $INSTALL_DIR/9rtui.ini"
ts "run:       9rtui"
case ":$PATH:" in *":$BIN_DIR:"*) ;; *) ts "NOTE: $BIN_DIR not in PATH" ;; esac
