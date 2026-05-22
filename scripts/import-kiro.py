#!/usr/bin/env python3
"""Import Kiro accounts from accounts.json into 9Router via official API."""
import argparse, json, time, sys, urllib.request, urllib.error
from pathlib import Path
from datetime import datetime

ROOT = Path(__file__).resolve().parent.parent
ACCOUNTS_FILE = ROOT / "accounts" / "accounts.json"
LOG_DIR = ROOT / "tui-logs"
DEFAULT_API = "http://192.168.0.44:20128"


def load_accounts(path: Path):
    if not path.exists():
        raise SystemExit(f"accounts file not found: {path}")
    data = json.loads(path.read_text())
    # Support multiple shapes:
    #   {data: [...]}
    #   {accounts: [...]}
    #   {rows: [...]}        (TUI undo-log: {op, rows: [...]})
    #   [...]                (plain array)
    if isinstance(data, list):
        arr = data
    elif isinstance(data, dict):
        arr = data.get("data") or data.get("accounts") or data.get("rows") or []
    else:
        arr = []

    out = []
    for a in arr:
        if not isinstance(a, dict):
            continue
        if (a.get("provider") or "").lower() != "kiro":
            continue

        # Credentials may live in:
        #   a["credentials"] (dict)
        #   a["data"] (dict OR JSON-encoded string — TUI undo-log shape)
        creds = a.get("credentials") or {}
        data_field = a.get("data")
        data_obj = {}
        if isinstance(data_field, dict):
            data_obj = data_field
        elif isinstance(data_field, str) and data_field.strip():
            try:
                data_obj = json.loads(data_field)
            except Exception:
                data_obj = {}

        rt = (
            creds.get("refresh_token")
            or creds.get("refreshToken")
            or data_obj.get("refreshToken")
            or data_obj.get("refresh_token")
            or a.get("refreshToken")
        )
        if not rt:
            continue

        psd = data_obj.get("providerSpecificData") or {}
        profile_arn = (
            creds.get("profile_arn")
            or creds.get("profileArn")
            or data_obj.get("profileArn")
            or psd.get("profileArn")
        )
        expires_at = (
            creds.get("expires_at")
            or creds.get("expiresAt")
            or data_obj.get("expiresAt")
        )
        status = a.get("status") or data_obj.get("testStatus")
        if not status and isinstance(a.get("isActive"), (int, float)):
            status = "active" if a["isActive"] == 1 else "inactive"

        out.append({
            "id": a.get("id"),
            "email": a.get("email") or a.get("name"),
            "status": status,
            "refreshToken": rt,
            "profileArn": profile_arn,
            "expiresAt": expires_at,
        })
    return out


def post_import(base_url, refresh_token, timeout=60):
    url = base_url.rstrip("/") + "/api/oauth/kiro/import"
    body = json.dumps({"refreshToken": refresh_token}).encode()
    req = urllib.request.Request(url, data=body, method="POST", headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            text = resp.read().decode("utf-8", "replace")
            return resp.status, json.loads(text) if text else {}
    except urllib.error.HTTPError as e:
        text = e.read().decode("utf-8", "replace")
        try:
            data = json.loads(text)
        except Exception:
            data = {"error": text}
        return e.code, data


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--api", default=DEFAULT_API)
    ap.add_argument("--source", default=str(ACCOUNTS_FILE), help="path to accounts JSON file")
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--import", dest="do_import", action="store_true")
    ap.add_argument("--active-only", action="store_true", default=True)
    ap.add_argument("--include-inactive", action="store_true")
    ap.add_argument("--limit", type=int, default=0)
    ap.add_argument("--sleep", type=float, default=1.0)
    args = ap.parse_args()

    source = Path(args.source).expanduser().resolve()
    accounts = load_accounts(source)
    if args.active_only and not args.include_inactive:
        accounts = [a for a in accounts if a.get("status") == "active"]
    if args.limit:
        accounts = accounts[:args.limit]

    print(f"source: {source}")
    print(f"api: {args.api}")
    print(f"kiro accounts selected: {len(accounts)}")

    if args.dry_run or not args.do_import:
        for i, a in enumerate(accounts[:20], 1):
            print(f"{i:3}. {a.get('email') or '-'} status={a.get('status')} profile={a.get('profileArn') or '-'} token=***{a['refreshToken'][-8:]}")
        if len(accounts) > 20:
            print(f"... {len(accounts)-20} more")
        print("dry run only; use --import to import")
        return 0

    LOG_DIR.mkdir(parents=True, exist_ok=True)
    log_path = LOG_DIR / f"kiro-import-{datetime.now().strftime('%Y%m%d-%H%M%S')}.json"
    results = []
    ok = 0
    fail = 0
    for i, a in enumerate(accounts, 1):
        print(f"[{i}/{len(accounts)}] import {a.get('email') or '-'} ... ", end="", flush=True)
        status, data = post_import(args.api, a["refreshToken"])
        row = {"email": a.get("email"), "sourceId": a.get("id"), "httpStatus": status, "response": data}
        results.append(row)
        if 200 <= status < 300 and data.get("success"):
            ok += 1
            print("OK")
        else:
            fail += 1
            print(f"FAIL {status}: {data.get('error') or data}")
        time.sleep(args.sleep)

    payload = {"createdAt": datetime.now().isoformat(), "api": args.api, "source": str(source), "ok": ok, "fail": fail, "results": results}
    log_path.write_text(json.dumps(payload, indent=2))
    print(f"done ok={ok} fail={fail}")
    print(f"log: {log_path}")
    return 0 if fail == 0 else 2

if __name__ == "__main__":
    raise SystemExit(main())
