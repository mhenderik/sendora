# Changelog

## Unreleased

## 0.1.2 (2026-11-17)

**Version numbering was restarted.** The earlier releases `0.9.0` and `0.9.1` are
now tagged `old/v0.9.0` and `old/v0.9.1`; their images stay in the registry. `0.1.2` is the
release after `0.9.1`, even though the number is lower: change a pinned image tag by hand.

### Security

- **Share files are encrypted at rest.** New uploads are stored as chunked
  AES-256-GCM (64 KiB chunks, a random key per file derived from the database key).
  The encryption happens in the copy that already moves a finished upload to its
  final place, so it adds no extra pass; downloads and previews decrypt on the
  fly and Range requests keep working. Tampered or truncated files are refused.
  Files uploaded before this version stay readable and unencrypted
  (`files.enc` marks encrypted files). Not covered: upload staging, temporary
  download snapshots, branding images. See the wiki page "Encryption".
- **Administrators can no longer see other users' shares.** The admin "All shares"
  page and `GET /api/shares/all` are gone, and admins cannot open, edit, expire,
  delete files from, or read the download history of shares they did not create.
  Public share links work as before.

### Operator notes

- The file encryption key follows `SENDORA_DB_KEY_FILE`. Keep the same key when you
  restore or move an instance: a database import re-encrypts the database with the
  server's key, but cannot re-encrypt files, so files uploaded since 0.1.2 are
  unreadable under a different key.
- Encrypted files cannot be read by 0.9.x: there is no rollback after the first
  upload without restoring the pre-upgrade backup.

### Interface

- New floating "glass" design across the whole interface, with a dark mode.
- Folder uploads show as a collapsible tree with file counts, sizes and per-folder
  progress; the public share page uses the same tree.

### Also in this release


- Setup page: a second hint says where the setup code is on Kubernetes and without
  containers, and that `SENDORA_SETUP_CODE` sets a fixed code. The documentation
  has a section "First-run setup code" (wiki) that the quick starts link to.
- Kubernetes: the startup check no longer warns about the `SENDORA_SERVICE_*` /
  `SENDORA_PORT_*` variables Kubernetes injects for a Service named `sendora`;
  the example manifests set `enableServiceLinks: false`.

## 0.9.1 (2026-11-15)

- `localhost`, `127.0.0.1` and `[::1]` count as the same origin when `APP_URL`
  is a loopback address (same scheme and port), so local and port-forward
  tests work from either address.
- Documentation moved to the [wiki](https://github.com/mhenderik/sendora/wiki):
  configuration reference, Docker and Kubernetes deployment, hardening guide,
  security notes, upgrades and backups, developer docs. The repository keeps the
  quick starts and the configuration table.
- `JWT_SECRET_FILE` is supported (read the signing secret from a mounted file).
- Startup warnings for unknown `SENDORA_*` variables (typos) and for a
  configuration that switches local login off without an enabled provider.
- Compose Caddyfile: backup import allows up to 4GB, other requests 100MB.
- The database file is now `sendora.db`. On the first start after the upgrade,
  Sendora renames an existing `fileshare.db` (with its `-wal`, `-shm` and
  `.pre-import` files) once and logs it; with both names present it refuses to
  start. Update scripts that call `sendora-db -source .../fileshare.db`.
- Removed: the `FILESHARE_VERIFIED_DOWNLOAD_MAX_BYTES` alias (use
  `SENDORA_VERIFIED_DOWNLOAD_MAX_BYTES`), and listing of old `fileshare-*.db`
  plaintext backups in the admin screen.
- Quick starts: one-command Docker, bare-minimum Kubernetes manifest
  (`deploy/kubernetes-minimal.yaml`, 2Gi volume, storage class check and a
  hostPath test volume); screenshots in the documentation.
- Kubernetes example: optional init container for storage that cannot give the
  volume to UID 10001 (kind, minikube, k3s local-path).
- Fixed: "Max expiry 0 = no maximum" was refused by the settings validation.
- Fixed: the language selector on signed-out pages stretched across the screen;
  spacing on the Security screen.

## 0.9.0 (2026-11-14)

First public release. Feature-complete, but not yet declared stable: it is vibe coded and has not been independently reviewed (see Known limitations). 1.0.0 follows after more real-world use.

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
- Microsoft Entra sign-in button with the Microsoft logo; the Entra user name is stored as an unverified email address on new accounts.
- Every admin setting and the login providers can be set from environment variables /
  a Kubernetes ConfigMap (secrets from files); configured values are read-only in the UI.
- Branding: logo, favicon, backgrounds, themes. Built-in Sendora logo.
- Activity log (at most 24 hours) with CSV export.

### Security
- Pentest fixes: absolute 30-day session lifetime, refresh-token reuse detection,
  provider-only sessions end with the provider, no password reset by mail for SSO-only
  accounts, recent sign-in required for their password/email changes, username rules,
  equal login timing, taken emails hidden at registration, SMTP password and client
  secret bound to their server, generic SMTP test errors, instance-wide mail cap,
  minimum transfer speed and one download slot per client, duplicate upload paths
  refused, share folders deleted outside the database transaction, hardened database
  import, settings validation, stricter CSV export, one-time first-run setup code,
  version hidden from public endpoints, `JWT_SECRET` minimum length.
- Fixed: the 48-hour cleanup of unconfirmed registrations also deleted Entra accounts
  and existing accounts that had changed their email address.
- Second review: guests no longer receive the share link, the reverse-share switch also
  stops existing invitations, open-link slots are released and guest accounts cleaned
  up, rate limits group IPv6 by /64, refresh-token reuse has a 60-second grace period.

### Known limitations
- **Vibe coded, not independently reviewed.** The project was written with an AI coding
  assistant by a maintainer who is not a developer; there has been no external code
  review or penetration test. See the README.
- **Passkeys and security keys are not verified against real hardware** in this
  project (only a software authenticator and test vectors). Test yours, and keep a
  recovery code, before you make MFA mandatory.
- Requires official SQLCipher 4.20.0 and CGO; the Docker image builds it.
- Usernames are case-insensitive and limited to ASCII letters, digits, dot, underscore
  and hyphen.
- Gmail replaces a From address that is not a verified *Send mail as* alias.
- The database file is still named `fileshare.db` for compatibility with existing
  installations.
- Single instance only (SQLite); no clustering.
- Account lockout can be triggered by anyone who knows a username (trade-off,
  documented in `SECURITY.md`).
