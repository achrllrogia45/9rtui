# 9rtui scripts

Standalone importer scripts (alternative to the TUI).

- `importer.py` — universal entry point: `python importer.py <provider> [args]`
- `import-kiro.py` — Kiro accounts via official 9Router API

## Usage

```
# Dry run
python scripts/importer.py kiro --dry-run

# Real import
python scripts/importer.py kiro --import

# Custom accounts file
python scripts/importer.py kiro --source accounts/my-accounts.json --import
```
