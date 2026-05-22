# Security

9rtui handles local 9Router account credentials.

Sensitive files:

```text
.accounts/
.tui-logs/
.env
9rtui.ini
*.sqlite
```

Account exports can contain refresh tokens, access tokens, profile ARNs, and provider metadata. Treat exported JSON as secrets.

Do not publish screenshots or logs unless secrets are redacted.

Report security issues privately to the repository owner.
