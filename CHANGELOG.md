# Changelog

## 0.1.5

### Hardening from an independent review

- **Verified downloads:** the data directory must have room for the snapshot plus a
  64 MiB margin or the download is refused with 507 (before any temporary file is
  written); one slot is held for at most 2 hours; at most 2 snapshots of one share
  at a time; a request with an invalid or unsatisfiable `Range` gets 416 without
  taking a slot.
- **Uploads:** a slow chunk no longer blocks other users. The body is received into a
  temporary file without holding the shared lock; a second chunk for the same upload
  while one is in flight gets `409` with `busy: true` and `Retry-After` (the web
  client waits and retries).
- **File names:** new uploads refuse the look-alike separators U+2215, U+FF0F and
  U+FF3C (invisible Unicode format characters and length limits were already refused).
  Appending files to a share now refuses a path that would be both a file and a
  folder, across existing and new files.
- **Share metadata:** `GET /api/shares/{id}` no longer returns the share name before a
  password-protected share is unlocked, and the 410 response for an expired share
  has no name. The page shows a neutral title until unlocked.
- **Secure defaults for new installations** (existing installations keep their
  stored values; restore the old value in Admin -> Settings or with the variable):
  - `account_password_min_length` 12 (was 8): `SENDORA_ACCOUNT_PASSWORD_MIN_LENGTH=8`
  - `share_password_min_length` 10 (was 8): `SENDORA_SHARE_PASSWORD_MIN_LENGTH=8`
  - `mfa_policy` `admins` (was `off`): administrators are taken to their profile at
    first sign-in to set up an authenticator app or passkey; `SENDORA_MFA_POLICY=off`
    restores the old behaviour.
  - Unchanged: self-registration off, previews on (only the fixed list of image,
    video and audio types is ever shown inline).
- **Startup:** Sendora refuses to start when `JWT_SECRET_FILE`, the SMTP password
  file or a provider client-secret file is writable by the group or accessible to
  others, or owned by another user (the rule of the database key file). It logs a
  warning, never the value, for a secret given as a plain environment variable, and
  warnings for a non-loopback `HOST` without an https application URL, risky
  self-registration, MFA off while an administrator exists, a password minimum
  below 10, and (once) `X-Forwarded-For` without `SENDORA_TRUSTED_PROXIES`.
  On Linux core dumps are disabled and the process is made non-dumpable.
- **Key material:** the SQLCipher key literal is built in buffers that are wiped
  right after use, and the key read from the key file is wiped once the store has
  its own copy.
- The development `docker-compose.yml` now has a read-only root file system, a
  `/tmp` tmpfs, all capabilities dropped and `no-new-privileges`, like the
  production compose file.

## Unreleased

- **Security:** the administrator statistics no longer list the names and link IDs of
  the most downloaded shares. With a share's link ID an administrator could open it,
  which contradicted "administrators cannot see other users' shares". The statistics now
  contain counts only (a test checks that no share name or link ID appears).
- Documentation corrections: the key also protects the encrypted share files, key
  rotation is not possible once files were uploaded since 0.1.2, only the owner (not
  an administrator) can edit a share, and the footer version is visible to signed-in users.

## 0.1.4 (2026-11-18)

- A wrong or missing database key is now reported as `database cannot be read with
  supplied key`. It used to be reported as `required SQLCipher 4.20.0 engine
  unavailable`, which sent operators looking for a missing engine.
- Documentation for Windows (tested on Windows 11 with Docker Desktop, in Windows
  PowerShell 5.1 and PowerShell 7.7): the bash key command does not work there and a
  key in a Windows folder is refused, so the Docker steps use Docker volumes, with
  back-up and restore of the key; the Kubernetes steps create the key with PowerShell.

## 0.1.3 (2026-11-17)

- The running version is shown in the footer for signed-in users (`/api/users/me`
  returns `version`). Public pages and endpoints still do not expose it.

## 0.1.2 (2026-11-17)

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
- Encrypted files cannot be read by older versions: there is no rollback after the
  first upload without restoring the pre-upgrade backup.

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
