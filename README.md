# Sendora

<p align="center"><img src="web/public/logo.svg" alt="Sendora logo" width="96" height="96"></p>

Sendora is an MIT-licensed, open source, self-hosted file-sharing application with a Go backend (chi and SQLCipher-encrypted SQLite) and a Vue 3 frontend embedded in one executable.

This is an implementation under development, not a claim of a full penetration test or production readiness. Validate external integrations and your deployment before exposing a public service.

## About this project (please read)

Sendora is **vibe coded**: almost all of the code, tests and documentation were written by an AI coding assistant ([Claude Code](https://claude.com/claude-code)) in conversation with the maintainer, who is **not a software developer**. It started because the maintainer missed features in the existing self-hosted file-sharing tools and wanted to see whether they could be built this way.

What that means for you:

- The code has automated tests (Go and Node) and the maintainer checked the behaviour by hand, but **no professional developer or security specialist has reviewed it**, and there has been **no external penetration test**. Treat it accordingly, especially before exposing it to the internet or putting sensitive data in it.
- Some parts, such as passkeys (WebAuthn), single sign-on and the database encryption handling, are security-sensitive. They are tested against software test setups and have not been verified in every real-world environment (see [limitations](docs/security.md)).
- Design decisions follow what the assistant and the maintainer found sensible; you may disagree. Reviews, bug reports and pull requests from experienced developers are very welcome (see [CONTRIBUTING.md](CONTRIBUTING.md)); report vulnerabilities privately as described in [SECURITY.md](SECURITY.md).
- It is provided under the MIT License **without warranty**.

## Features

- File and folder uploads through a custom resumable, chunked API.
- Readable share links (words and number blocks), with expiry that can be changed later, optional or required passwords with an administrator-defined policy, download limits, individual-file downloads, and ZIP downloads for multi-file shares.
- Editing existing shares (name, description, password, download limit), QR codes for share and invitation links, in-browser preview of images, video and audio, and a list or grid view on the share page.
- Reverse shares: authenticated users invite guests to upload by email or through an open link with a maximum number of uploads, without creating ordinary accounts.
- Local accounts, administrator-managed users, public registration with email verification, password reset, and profile preferences.
- OpenID Connect, Google, GitHub, and single-tenant Microsoft Entra ID login.
- Editable email subjects and bodies, download-start notifications, owner/admin download history, statistics, and encrypted database backups.
- SQLCipher 4.20.0 database encryption with an external key, a configurable default storage quota (10 GB for new installations) with per-user overrides in decimal GB, and local-password account lockout.
- Optional administrator activity log with a retention of at most 24 hours (no IP addresses) and CSV export, and an optional external-login-only mode with lockout protection.
- Branding uploads (logo, favicon, backgrounds) that can each be deleted again, plus a choice of five bundled CC0 background photos (and an illustration), or no background at all, bundled/custom themes, and English, Dutch, German, French, and Spanish menus with per-account preferences.
- SHA-256 upload checks and server-prepared download snapshots with browser-side verification.
- Optional multi-factor authentication for local accounts: authenticator-app codes (TOTP) with one-time recovery codes, set globally and overridable per user (passkeys are planned).

## Documentation

- **[Quick start](docs/quickstart.md)** — Run Sendora in Docker in a few minutes. For a build from source see [Build and development](docs/build.md).
- **[Docker Compose files](deploy/compose/README.md)** — Ready-to-use Compose setup with the published image, optional automatic HTTPS.
- **[Build, develop and test](docs/build.md)** — How to build Sendora from source, run it locally and run the test suites.
- **[Configuration](docs/configuration.md)** — Environment variables and runtime settings.
- **[Deployment](docs/deployment.md)** — Docker behind HTTPS (see the Kubernetes guide for clusters).
- **[Kubernetes](docs/kubernetes.md)** — Step-by-step Kubernetes deployment: ConfigMap and Secrets, first-run setup, Ingress, upgrades, backups and troubleshooting.
- **[Backups, restore and migration](docs/backup-and-migration.md)** — Where the data lives, how to back it up and how to move or import a database.
- **[Accounts and authentication](docs/authentication.md)** — Two-step verification, password policy, lockout, automatic sign-out and Microsoft Entra ID.
- **[Email, registration and templates](docs/email.md)** — Public registration, SMTP, welcome mail, editable templates and notification semantics.
- **[Translations](docs/translations.md)** — Translate the interface and the emails through JSON files.
- **[Sharing, uploads and downloads](docs/sharing.md)** — Share options, editing, the activity log and the integrity guarantees of uploads and downloads.
- **[Security and limitations](docs/security.md)** — Operational security notes and the current limitations.

Start with the [quick start](docs/quickstart.md). The full index is in [docs/README.md](docs/README.md).

## Bundled background photos

The five background photographs in `web/src/assets/backgrounds/` come from Wikimedia Commons, where their uploaders declare them CC0 (public-domain dedication); sources, authors, licence labels and the provenance checks (including the Unsplash 2017-06-05 licence cut-off) are listed in `web/src/assets/backgrounds/CREDITS.md`. They were resized and recompressed and metadata was removed. Verify the licence information yourself before redistributing. The administrator chooses one of them, the original illustration, or no background under Admin → Branding; uploaded images always take precedence. The default is "Misty lake".

## License

Sendora is open source under the [MIT License](LICENSE). You may use, copy, modify, distribute, and sublicense it under that license's terms, including retaining its copyright and permission notices. The software is provided without warranty. Third-party dependencies retain their respective licenses.

## Project

- [Security policy](SECURITY.md) and how to report a vulnerability
- [Contributing](CONTRIBUTING.md)
- [Changelog](CHANGELOG.md) and known limitations
- [Third-party notices](THIRD_PARTY_NOTICES.md)
- [Example translations](custom-translations/README.md)
