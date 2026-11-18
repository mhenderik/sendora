# Changelog

## 0.1.5

- **Security:** the administrator statistics no longer list the names and link IDs of
  the most downloaded shares. With a share's link ID an administrator could open it,
  which contradicted "administrators cannot see other users' shares". The statistics now
  contain counts only (a test checks that no share name or link ID appears).
- Documentation corrections: the key also protects the encrypted share files, key
  rotation is not possible once files were uploaded since 0.1.2, only the owner (not
  an administrator) can edit a share, and the footer version is visible to signed-in users.

### Security: authentication, sessions and tokens

- **All users must sign in again after upgrading:** access, second-step and share-unlock
  tokens are now signed with separate keys derived (HKDF-SHA256) from the stored secret
  instead of the secret itself, so existing tokens stop working. Share unlock tokens
  of password-protected shares are issued again on the next unlock.
- Verifying an email address with the emailed code signs in only a new self-registration.
  Any other account (for example after an address change) gets a plain confirmation and
  signs in with its password. The code is compared in constant time and now has 8 digits.
- A wrong current password on a profile change or on the two-step setup actions counts as a
  failed sign-in (same counter and lockout as the login, address ban included) and is
  refused while the account is locked.
- First-run setup: failed attempts count towards the address ban and are limited across all
  addresses (10 per minute). `SENDORA_SETUP_CODE` needs at least 16 characters and is
  ignored (a generated code is used, with a log line) when it is one repeated character or
  a sequential run.
- New password hashes use bcrypt cost 12; older hashes are upgraded after a successful
  login or share unlock.
- `JWT_SECRET` with fewer than 8 distinct characters is refused at startup (32 characters
  minimum as before).
- Share unlock: at most 10 wrong passwords per share per 10 minutes, then a 10-minute
  block for that share, in addition to the per-address limits.
- Sign-in with an external provider has its own pool of concurrent requests, provider
  discovery and token requests time out after 5 seconds, follow at most 3 redirects and
  refuse loopback, link-local and cloud metadata addresses (private ranges stay allowed).
  They no longer use `HTTP_PROXY`/`HTTPS_PROXY`.
- The sign-in page ignores `next` values such as `//host` or `/\host`.
- Failed backups, provider creation and email template previews return a fixed message;
  the detail is only in the server log. Request headers are limited to 64 KiB.

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
