# Changelog

## Unreleased

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
