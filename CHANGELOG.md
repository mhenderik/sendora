# Changelog

## Unreleased

## 0.1.5 (in development)

### Audit log

- The optional, best-effort 24-hour activity log is replaced by an always-on,
  tamper-evident audit log. Every record is chained to the previous one with an
  HMAC-SHA256 keyed from the database key (editing, deleting, inserting or reordering a
  record is detected at a definite sequence number). Records are written synchronously;
  a failed write is retried once, reported on stderr and counted.
- Critical administrator actions (users and roles, settings, login providers, backups,
  database import, MFA reset, password reset) write an `attempted` record before they
  run and are refused with 503 when that record cannot be stored. A middleware records
  every other mutating request (`api_mutation`) and denied access (`access_denied`).
- New settings `audit_retention_days` (90 to 365, default 90) and `audit_log_ip`
  (default off). Changes are themselves audited with the old and new value. The old
  `logging_enabled` and `log_retention_hours` settings and their `SENDORA_*` variables
  are ignored; the existing event log (24 hours at most) is dropped by the migration.
- New read-only `auditor` role (mutually exclusive with administrator). Auditors and
  administrators read the same audit log: all categories through `/api/audit` (the admin
  Logs page is the same page), CSV or JSON Lines export with the chain hashes, and the
  chain status. Share events name a share only by number (`share#N`), never by name,
  link ID or file name, and nobody can read share content through the log.
- **Every download of the audit log is recorded** (person, role, format, number of records
  and the filters used). The record is written before the export is served; if it cannot
  be written, the export is refused. The old `logs_exported` action is now `audit_exported`.
- **Audit log search** works on words: spaces, underscores, dashes and `*` separate them,
  and each word may occur anywhere in the action, user, target, detail or category, so
  `create_user`, `user create` and `create` all find `user_created`.
- `sendora-db audit-verify -source <db> -key-file <key> [-json]` verifies the chain
  offline and exits non-zero on a break. Importing a database keeps this instance's
  audit log instead of replacing it with the backup's.

### Security

- A database import now also clears pending MFA-reset confirmation links from the imported
  file, so a link that was valid in the old database cannot remove a second factor afterwards.
- **Security: share file format v2.** Every new encrypted file carries a random file ID
  (header 53 bytes) that is part of each chunk's authenticated data and is stored in the
  new `files.enc_id` column. A file whose header ID differs from its database row is
  refused, so ciphertext copied or renamed over another file (even of equal size) is no
  longer accepted. Files written by 0.1.2-0.1.4 (format v1) stay readable.
- Encrypted empty files now have their (empty) chunk authenticated when opened; before,
  altering one was not noticed.
- Uploads into an encrypted store are refused when the file key is missing instead of
  being stored in plain text, and a partly written destination file is removed on failure.
- New `sendora-db verify-files` (`-source`, `-key-file`, `-data-dir`, `-json`, `-names`):
  checks every stored file (header, size, file ID, every chunk tag, SHA-256) and lists
  orphan files; prints no keys, plaintext or names unless `-names` is given; exit status is
  non-zero on any problem.
- Key fingerprint (`file encryption active fp=... format=2 sqlcipher=...` at startup),
  encrypted/plain file counts and decrypt-failure counters for later use by the admin UI.
- Specification `docs/FILE-FORMAT.md` with test vectors
  (`internal/filecrypt/testdata/vectors.json`), an independent Python check
  (`docs/tools/verify_vectors.py`), fuzz and property tests for the reader.

- **Administrators can no longer take over another user's account silently** (that would
  give them access to the user's shares):
  - `PUT /api/users/{id}` refuses a password for another user (use "Send password reset
    link"); the `admin`, `name` and `email` fields are optional, so a partial update can
    no longer demote a user or blank an address.
  - Changing another user's email address makes it unverified again, signs the user out
    and notifies the previous address.
  - "Force reset" now mails the user a one-time reset link (verified address only; the
    administrator never sees it). Resending the welcome email needs a verified address.
  - Resetting a user's MFA is a two-step flow: the administrator's request mails a
    24-hour, single-use confirmation link to the user's verified address and the factors
    are removed only when the user confirms (`POST /api/auth/mfa-reset/confirm`); the user
    is notified afterwards. Without a verified address use `sendora-db mfa-reset`.
  - With `APP_URL` set, the application URL setting is read-only; other changes to it are
    logged (`application_url_changed`).
  - Applying a database import requires the administrator's local password; an
    administrator without one must set a password under their profile first.
  - Updating, resetting or deleting guest accounts through the user endpoints returns 404.
  - New email templates (English, Dutch, German, French, Spanish) and event-log actions:
    `user_password_reset_sent`, `user_email_changed`, `mfa_reset_requested`,
    `mfa_reset_confirmed`. The old `mfa_reset` action is no longer written.

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
- **Defaults stay as they were** (the review suggested stricter ones, but 12 characters and
  mandatory MFA did not work well in practice): password minimum 8 for accounts and
  shares, `mfa_policy` `off`, self-registration off, previews on (only the fixed list of
  image, video and audio types is ever shown inline). All remain configurable.
- **Two-step verification reminder:** a signed-in account that signs in with a local
  password and has no second factor yet gets a dialog ("Two-step verification is not set
  up yet") with *Set up now* and *Remind me later*. A dismissed reminder returns after
  `mfa_reminder_days` days (Admin -> Settings, default 1, 0 = at the next sign-in; kept in
  the browser). Accounts that use an identity provider, guests and auditors are not asked.
  A required policy (`admins` or `all`) still applies only to local accounts.
- **Theme editor:** colours show as a swatch plus hex field and every change is previewed
  at once on the running interface (for you only, until saved); leaving the editor restores
  the previous theme. Built-in themes can no longer be overwritten: "Copy and edit" creates
  a new custom theme.
- Checkboxes and radio buttons are drawn by the application and no longer show as dark blocks.
- **Startup:** Sendora refuses to start when `JWT_SECRET_FILE`, the SMTP password
  file or a provider client-secret file is writable by the group or accessible to
  others, or owned by another user (the rule of the database key file). It logs a
  warning, never the value, for a secret given as a plain environment variable, and
  warnings for a non-loopback `HOST` without an https application URL, risky
  self-registration and (once) `X-Forwarded-For` without `SENDORA_TRUSTED_PROXIES`.
  On Linux core dumps are disabled and the process is made non-dumpable.
- **Key material:** the SQLCipher key literal is built in buffers that are wiped
  right after use, and the key read from the key file is wiped once the store has
  its own copy.
- The development `docker-compose.yml` now has a read-only root file system, a
  `/tmp` tmpfs, all capabilities dropped and `no-new-privileges`, like the
  production compose file.

### Fixes and improvements after a second review

- **Audit log:**
  - Auditors can set up and manage their own second factor, so they are no longer locked
    out of the audit log when MFA is required for everyone.
  - `access_denied` is recorded only when a signed-in, verified user is refused. Expired,
    revoked or invalid tokens and unauthenticated requests no longer write records, so the
    log cannot be flooded from the internet.
  - Verification reads one consistent snapshot, so concurrent writes or purges no longer
    raise a false tamper alarm. The chain status is verified incrementally (same result as a
    full check; changes to existing records still restart the check, detected by triggers
    counting into a new `audit_guard` table). A full check runs in the background at start.
  - `audit_retention_days` is now **90 to 365 days** (default 90). Lower values are refused,
    and a stored value below 90 is raised to 90 on upgrade, so the evidence cannot be wiped
    by lowering the retention.
  - Orderly shutdown waits for requests in flight and background jobs, then writes
    `system_stop`; a database import records the stop before carrying the audit log. A
    request whose handler crashes is recorded as failed (status 500).
  - Exports are counted first (413 when too large) and streamed in pages instead of being
    loaded into memory. The administrator CSV has the same columns as the auditor export
    (including `ip`, `prev_hash`, `hash`; the first column is now `seq`), and
    `/api/logs/export` also accepts `format=jsonl`.
  - Search is faster (new indexes); totals are capped at 10,000 (`total_capped`), and
    `before_seq` / `next_before_seq` add keyset paging next to offset.
  - `audit_purge_failed` is recorded once when purging starts to fail, and the status shows
    `purge_refused`.
  - `download_started` is recorded only for the first request of a download, at most once
    per share every 5 minutes.
  - The administrator's MFA reset request is recorded as `mfa_reset_requested`; this and
    `mfa_reset_confirmed` are category account. Long `settings_changed` details end with
    "(+N more)".
- **Downloads:**
  - A modified chunk in an encrypted file answers 409 "integrity check failed" (was 503) for
    single, range and ZIP downloads, and is recorded as `file_decrypt_failed` (at most once per
    file per 10 minutes); previews that hit a modified chunk are recorded too, and a preview
    whose first chunk is damaged answers 409 instead of 415.
  - Parallel range connections from one client share one verified snapshot, so download
    managers no longer get 503 after the second connection. Transfers that keep moving are no
    longer cut off after 2 hours.
- **Share passwords:** parallel guesses can no longer get past the attempt limit. A client
  (address, IPv6 /64) is blocked after 10 failures in 10 minutes; the whole share only after
  50, so one guessing client cannot lock out the other recipients.
- **`sendora-db verify-files`:** prints only problems by default (`-all` lists every row);
  **`-json` now writes JSON Lines** (one object per row, then a summary), which replaces the
  single JSON document; checks files in parallel; shows progress; ignores directories inside
  a share directory.
- **Interface:**
  - Audit log: paging, Refresh and exports use the last submitted search; an older response
    no longer overwrites a newer one; a double click exports once; exports and backup
    downloads sign in again when the session expired and show translated errors; dates follow
    your time zone; Action suggestions; translated categories, outcomes and roles; empty state
    with Clear filters; "1-50 of N" pager; labelled fields and table headers. The admin tab is
    called "Audit log".
  - Theme editor: activating keeps an unsaved draft if it fails; deleting asks first;
    leaving with unsaved changes asks; contrast ratios with a warning below 4.5:1; colours with
    alpha get the right button text colour.
  - The sign-out warning always shows above the two-step reminder; both dialogs keep keyboard
    focus inside; Escape closes the reminder; no reminder while a password change is required.
  - Users: reset-link and MFA-reset actions are disabled when outgoing email is not set up or
    the user has no address; results appear next to the row.
  - Unchecked checkboxes have a stronger border; signed-in auditors and guests can open
    password-reset and MFA-reset links.

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
