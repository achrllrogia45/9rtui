#!/usr/bin/env python3
"""9rtui importer shim.

One import path now: call 9rtui's Go importer, which writes through the
official 9Router backup API instead of direct SQLite account surgery.

Usage:
    python scripts/importer.py kiro accounts.json
    python scripts/importer.py codex accounts.json
    python scripts/importer.py antigravity accounts.json

Environment:
    NRTUI_BIN=/path/to/9rtui   optional binary override
"""
import os
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PROVIDERS = {"kiro", "codex", "antigravity"}


def find_binary() -> str:
    env = os.environ.get("NRTUI_BIN")
    if env:
        return env
    local = ROOT / ("9rtui.exe" if os.name == "nt" else "9rtui")
    if local.exists():
        return str(local)
    found = shutil.which("9rtui")
    if found:
        return found
    raise SystemExit("9rtui binary not found. Build first or set NRTUI_BIN.")


def main() -> int:
    if len(sys.argv) < 3 or sys.argv[1] in ("-h", "--help"):
        print(__doc__.strip())
        print("Available providers:", ", ".join(sorted(PROVIDERS)))
        return 0
    provider = sys.argv[1].lower()
    if provider not in PROVIDERS:
        print(f"unknown provider: {provider}", file=sys.stderr)
        print(f"available: {', '.join(sorted(PROVIDERS))}", file=sys.stderr)
        return 2
    binary = find_binary()
    return subprocess.call([binary, "import-file", "-provider", provider, "-file", sys.argv[2], *sys.argv[3:]])


if __name__ == "__main__":
    raise SystemExit(main())
