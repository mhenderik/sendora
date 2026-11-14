# Changelog

## 1.0.0 (unreleased)

First public release.

### Features
- Self-hosted file sharing: resumable chunked uploads, readable share links, QR
  codes, previews, password-protected and expiring shares, download limits, and
  editing of existing shares.
- Reverse shares (guests upload to you) through open or single-use invite links.
- Accounts with storage quotas (adjustable default), registration with email
  verification and optional domain allowlist, welcome emails, idle auto-logout.
- Two-step verification: authenticator app (TOTP), recovery codes and passkeys or
  security keys (WebAuthn), as a global or per-user policy.
- External sign-in with OpenID Connect (including Microsoft Entra); local login can
  be switched off with a documented recovery switch.
- Address blocking after repeated failed sign-ins (fail2ban style) with an admin
  screen, allowlist and recovery switch.
- SQLCipher-encrypted database with an external key; verified encrypted backups;
  offline tools (`sendora-db`); database import with sanitising.
- Email: editable templates for eight events, language import and export, optional
  Reply-To, SMTP and Gmail setup.
- Interface and mail translations through JSON files (Dutch complete; German,
  French and Spanish partial; add any language under Admin → Languages). Examples
  in `custom-translations/`.
- Branding: logo, favicon, backgrounds, themes. Built-in Sendora logo.
- Activity log (at most 24 hours) with CSV export.

### Known limitations
- **Passkeys and security keys are not verified against real hardware** in this
  project (only a software authenticator and test vectors). Test yours, and keep a
  recovery code, before you make MFA mandatory.
- Requires official SQLCipher 4.20.0 and CGO; the Docker image builds it.
- Usernames are case-insensitive for ASCII letters only.
- Gmail replaces a From address that is not a verified *Send mail as* alias.
- The database file is still named `fileshare.db` for compatibility with existing
  installations.
- Single instance only (SQLite); no clustering.
- Account lockout can be triggered by anyone who knows a username (trade-off,
  documented in `SECURITY.md`).
