#!/usr/bin/env python3
"""Universal importer: delegates to provider-specific scripts.

Usage:
    python importer.py kiro [args...]
    python importer.py codex [args...]
"""
import sys
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parent

PROVIDERS = {
    "kiro": "import-kiro.py",
    # codex/antigravity use direct DB import (handled by Go binary)
}

def main():
    if len(sys.argv) < 2 or sys.argv[1] in ("-h", "--help"):
        print(__doc__)
        print("Available providers:", ", ".join(PROVIDERS))
        return 0

    provider = sys.argv[1].lower()
    if provider not in PROVIDERS:
        print(f"unknown provider: {provider}", file=sys.stderr)
        print(f"available: {', '.join(PROVIDERS)}", file=sys.stderr)
        return 2

    script = ROOT / PROVIDERS[provider]
    if not script.exists():
        print(f"script not found: {script}", file=sys.stderr)
        return 1

    return subprocess.call([sys.executable, str(script)] + sys.argv[2:])

if __name__ == "__main__":
    raise SystemExit(main())
