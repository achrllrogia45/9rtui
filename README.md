# 9rtui

Terminal UI and helper tools for managing local 9Router account rows.

9rtui reads the local 9Router SQLite database, shows accounts in a TUI, supports import/export JSON, delete/restore undo logs, and maintenance actions.

## Install

Linux/macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/achrllrogia45/9rtui/main/install.sh | bash
```

Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/achrllrogia45/9rtui/main/install.ps1 | iex
```

Installers build from source on your machine and install into:

- Linux/macOS: `~/.9rtui`
- Windows: `%USERPROFILE%\.9rtui`

## Runtime files

```text
.9rtui/
  9rtui / 9rtui.exe
  9rtui.ini
  .env
  .accounts/
  .tui-logs/
  scripts/
```

`9rtui.ini` default:

```ini
project_dir = .
db_path = ~/.9router/db/data.sqlite
log_dir = ./.tui-logs
api_base = http://localhost:20128
accounts_path = ./.accounts/
dev_mode = false
```

Windows default DB:

```text
%APPDATA%\9router\db\data.sqlite
```

## Commands

```bash
9rtui                         # open TUI
9rtui tui                     # open TUI
9rtui version                 # print version
9rtui config                  # print resolved config
9rtui check-db                # verify DB quick_check
9rtui import-file -provider kiro -file accounts.json
9rtui export [-provider kiro]
9rtui web start
9rtui web expose
9rtui stop
```

## Import/export

Account JSON files live in:

```text
.accounts/
```

Logs, undo files, and full DB backups live in:

```text
.tui-logs/
```

Export names use minute precision:

```text
kiro-8-accounts-YYYYMMDD-HHMM.json
```

## Development

```bash
go test ./...
go build -o 9rtui .
GOOS=windows GOARCH=amd64 go build -o 9rtui.exe .
```

Local helper:

```bash
scripts/build-all.sh
```

## Security

Exports contain credentials/tokens. Do not commit `.accounts/`, `.tui-logs/`, `.env`, or `9rtui.ini`.
