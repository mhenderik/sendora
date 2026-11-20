# Changelog

## Unreleased

## 0.2.0 (in development)

0.2.0 adds pluggable object storage and optional multi-tenancy. **The defaults stay
the local data directory and single-tenant mode; existing installations keep working
unchanged** (same layout, no migration needed).

### Object storage

- Share files can be stored on an **S3-compatible service** (AWS S3, MinIO, Cloudflare
  R2, Wasabi, Backblaze B2; static keys or the AWS default credential chain including
  IRSA/web identity and EKS Pod Identity), **Azure Blob Storage** (shared key, SAS, or
  `DefaultAzureCredential` with workload/managed identity) or **Google Cloud Storage**
  (service-account JSON or Application Default Credentials/GKE workload identity, through
  the JSON API without the gRPC client library). New setting `storage_backend`
  (`local`, `s3`, `azure`, `gcs`) with per-backend settings `storage_s3_*`,
  `storage_azure_*`, `storage_gcs_*`; see [docs/STORAGE.md](docs/STORAGE.md).
- **Encryption stays in Sendora:** files are encrypted (format v2) before they leave the
  server; object stores only see ciphertext under random names (`files/<32 hex>`) that
  reveal neither share link IDs nor file names. Uploads are assembled locally as before,
  encrypted and streamed to the store; a failed upload leaves no partial object.
  Downloads, HTTP range requests, ZIP downloads, verified downloads and previews read
  from the store without loading whole files into memory.
- **Per-file location:** every file record names its storage location (new columns
  `files.storage_id`, `files.object_key`, table `storage_locations` with the
  configuration and credentials each location was used with). Switching the backend or
  bucket only affects new files; earlier files stay readable where they are.
- **Migration** of existing files to the current backend: a background job from
  Admin → Storage (progress, cancel, audit trail) and
  `sendora-db storage-migrate [-dry-run]`. Each file is copied, verified completely
  (every GCM chunk tag, file ID, SHA-256), switched and then its source deleted (web job:
  after 15 minutes). Plain legacy files are encrypted on the way; failed files stay in
  their old location.
- Deleting files, shares, users and GDPR erasure remove remote objects through a
  deletion queue filled in the same database transaction (retried with backoff). Expiry
  clean-up and `sendora-db verify-files` (including orphan objects) work for remote
  backends.
- **Remote backup copies:** `backup_remote_enabled` also uploads the automatic
  (SQLCipher-encrypted) daily backups to `<prefix>backups/`; `backup_keep` applies to
  the remote copies too.
- New **Storage** tab in the admin settings: backend selector and fields, write-only
  secrets, *Test connection* (writes, reads and deletes a probe object), files and bytes
  per location, pending deletions and the migration control. All settings are also
  `SENDORA_STORAGE_*` variables (read-only in the screen, `_FILE` for secrets).
- Changing storage settings, testing a connection and starting or cancelling a migration
  need a step-up and are audited (`storage_backend_changed`, `storage_connection_tested`,
  `storage_connection_test_failed`, `storage_migration_*`, `backup_remote_*`). Saving a
  changed remote configuration runs a connection test first; changing the endpoint,
  account or access key requires entering the stored secret again; `http://` endpoints
  are accepted only for loopback, private and cluster-internal hosts; bucket, container,
  account, region and prefix names are validated; an invalid configuration stops the
  server at start.
- Documentation: new [docs/STORAGE.md](docs/STORAGE.md); configuration, threat model
  (object storage provider), data flows, shared responsibility and audit events updated;
  commented storage examples in `deploy/kubernetes.yaml`.

### Build

- New dependencies: `aws-sdk-go-v2` (`service/s3`, `config`, `credentials`), Azure
  `azblob` and `azidentity`, `golang.org/x/oauth2/google`. `azblob` is pinned at
  v1.6.4: v1.7 and later pull in Apache Arrow (about 4.7 MB more binary).
- Binary size (linux, stripped): `sendora` 14.4 MB -> 25.6 MB, `sendora-db` 3.3 MB -> 13.4 MB
  (it reads and migrates files in object stores). Image size (local build): 126 MB -> 147 MB
  uncompressed, about 39.5 MiB -> 45.8 MiB compressed. The Google Cloud Storage backend uses the
  JSON API with `golang.org/x/oauth2/google` instead of the gRPC-based client library.
- **One binary**: `sendora-db` is now a subcommand of the main program (`sendora db
  <command>`, or `sendora-db <command>` through the name it is started with). The image
  installs `/usr/local/bin/sendora-db` (and `/sendora-db`) as links to `/sendora`, so
  `docker exec <container> sendora-db ...` and existing scripts keep working; the second
  binary is gone (the code moved from `cmd/sendora-db` to `internal/dbcli`). Image size
  (local build): 147 MB -> 134 MB uncompressed, 46.0 MiB -> 41.3 MiB (48,196,898 -> 43,352,234 bytes)
  as `docker save | gzip`. `sendora healthcheck` is unchanged.
- Dockerfile `ARG VERSION` is 0.2.0.

### Code scanning

- Path handling hardened so the guards are visible to CodeQL's taint analysis (no
  behaviour change for valid input): the upload session directory is built behind an
  early-return check of the session ID; chunk, complete, append and clean-up check the ID
  again before touching files; branding and background images go through a `localJoin`
  helper (`filepath.IsLocal`, no `..`) right before they are written, served or deleted.
- Outgoing mail: every address is parsed with CR/LF/NUL rejected and rendered with
  encoded names for From, To and Reply-To; the Message-ID domain is sanitised. The
  remaining finding on the SMTP write is inherent (recipient and text are user input by
  design; headers are CRLF-safe and the body is quoted-printable).
- Integer settings of the platform are clamped before `int` and `time.Duration`
  conversions. The SHA-1 of the breached-password check stays (required by the HIBP
  k-anonymity range API; only 5 hex characters are sent, nothing is stored); documented
  at the call.


### Multi-tenancy

- Optional multi-tenant mode (`SENDORA_MULTI_TENANT=true`, `SENDORA_BASE_DOMAIN`): one
  process serves many organisations on `<slug>.<base domain>` or verified custom domains.
  **Single-tenant mode stays the default and is unchanged**; existing installations are
  not affected. See [docs/MULTI-TENANT.md](docs/MULTI-TENANT.md).
- Each tenant has its own SQLCipher database, directory (shares, uploads, backups,
  branding), users, settings, SMTP, sign-in providers and audit log, served by its own
  server instance. The tenant's database key is derived from the master key with
  HKDF-SHA256 and the tenant's random ID (`sendora/v1/tenant-db/<id>`); file keys, the audit
  key, the token signing secret and all HMAC keys follow from it, so a token, cookie,
  link or file of one tenant never verifies or decrypts in another.
- Routing by host (`X-Forwarded-Host` only from trusted proxies); unknown hosts get a
  plain 404, suspended tenants 503. Tenants open on the first request and close when idle
  (LRU, `SENDORA_MAX_OPEN_TENANTS`); background jobs also run for idle tenants on a schedule.
- Platform console on `admin.<base domain>` (separate page of the web build): platform
  administrators with TOTP (required by default, see below), first one via a one-time
  setup code in the log; create, search, suspend, resume and delete tenants (delete needs
  a step-up), quotas (maximum users, maximum storage), custom domains with DNS TXT
  verification, primary domain, per-tenant storage, platform settings (also via
  `SENDORA_*`), platform SMTP as default for tenants. The console sees
  counts only, never tenant content. A tamper-evident platform audit log records every
  action; actions affecting a tenant are also recorded in the tenant's own audit log.
- `sendora-db tenant-import` converts a single-tenant `DATA_DIR` into a tenant: the
  database is re-encrypted with the derived key, share and branding directories are
  moved, backups re-encrypted, everything verified, the originals archived. Resumable
  and abortable (`-abort`). Imported tenants keep the old key as the root for their file
  and audit keys, so files stay decryptable and the audit chain verifiable without
  rewriting. Offline commands accept `-tenant <id>` (and `-legacy-root`).
- Safety: a single-tenant start refuses a multi-tenant `DATA_DIR` and vice versa; the
  in-app database import is disabled for tenants.
- New example manifest `deploy/kubernetes-multi-tenant.yaml` (wildcard Ingress and TLS
  with cert-manager, ConfigMap).
- **Per-tenant object storage.** Each tenant's server uses the storage the platform
  assigns: the **platform default** (console *Platform settings → Default storage* or
  `SENDORA_PLATFORM_STORAGE_*`, with *Test connection*) under the tenant's fixed prefix
  `<prefix>tenants/<tenant id>/`, or the **tenant's own bucket** configured in the
  console (same validation and connection test as Admin → Storage; overlaps with the
  platform's `tenants/` area or another tenant's bucket are refused). While the default
  is the local data directory, tenants keep using their directory. A per-tenant switch
  *Tenant may configure its own storage* lets the tenant's administrators use Admin →
  Storage themselves (explicit credentials only, never the server's IAM role or
  workload identity); otherwise they see their storage read-only with an explanation.
  Tenants share a bucket but never a key space, every object is encrypted with the
  tenant's own key, and the migration job, remote backup copies and
  `sendora-db storage-migrate -tenant` work per tenant. Deleting a tenant removes its
  prefix (and Sendora's objects in its own bucket); it is refused when objects cannot
  be removed unless forced (the leftovers are listed in the audit record).
  `SENDORA_STORAGE_*` is ignored in multi-tenant mode.
- **SSRF guard for tenants** (new package `internal/netguard`): in multi-tenant mode the
  SMTP server, OIDC issuers, audit log webhook and syslog targets and storage endpoints a
  tenant's administrator configures must not resolve to loopback, private (RFC 1918,
  ULA), link-local, carrier-grade NAT, cloud metadata or other special-purpose addresses.
  Checked when saved and again on every connection by a dialer on the resolved address
  (DNS rebinding, redirects, GCS token URIs). Platform-provided targets (platform SMTP,
  platform storage, `SENDORA_*` settings and providers) are not affected. A platform
  administrator can allow private targets per tenant (*Network targets*; step-up,
  audited as `tenant_network_policy_changed` in both logs). Single-tenant mode is unchanged.
- **Platform MFA switch** (owner decision): platform setting `platform_mfa_required`
  (default `true`, also `SENDORA_PLATFORM_MFA_REQUIRED`). Changing it in the console shows
  a warning, needs a fresh confirmation and is recorded as
  `platform_mfa_requirement_changed`. When off, platform administrators sign in with a
  password only; enrolling an authenticator stays possible (*My account*) and step-ups
  use the password for administrators without one. Switching it on ends the sessions of
  administrators without an authenticator.
- **Offline platform-administrator recovery**: `sendora-db platform-admin-reset -data-dir
  <DATA_DIR> -key-file <master key> -user <name> [-mfa] [-password]` (server stopped)
  removes the administrator's authenticator and/or sets a temporary password that must
  be changed at the next sign-in; unlocks the account, ends its sessions and records
  `platform_admin_offline_reset` in the platform audit log. Administrators can also
  change their own password in the console.

## 0.1.5 (2026-11-18)

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
- **Defaults:** password minimum 8 for accounts and shares, self-registration off, previews
  on (only the fixed list of image, video and audio types is ever shown inline). New
  installations get `mfa_policy` **`optional`**: nobody is forced to use two-step
  verification, but everyone can set it up (`off` switches it off entirely: no enrolment and
  no reminder; existing installations keep their stored value). All remain configurable.
- **Two-step verification reminder:** a signed-in account that signs in with a local
  password and has no second factor yet gets a dialog ("Two-step verification is not set
  up yet") with *Set up now* and *Remind me later*. A dismissed reminder returns after
  `mfa_reminder_days` days (Admin -> Settings, default 1, 0 = at the next sign-in; kept in
  the browser). It is not shown when the policy is `off`, to accounts that use an identity
  provider, or to guests. A required policy (`admins`, `privileged` or `all`) applies only to local accounts.
- New two-step verification policy `privileged`: required for administrators **and**
  auditors (Admin -> Security).
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

### Compliance: accounts, sessions and evidence

- **Access review** (ISO 27001 A.5.18 / SOC 2 CC6.2-6.3): Admin -> Access review (also
  for auditors) lists every account with role, sign-in sources, two-step status, email
  verification, created at/by, last role and email change, last sign-in, lock and disabled
  state, and share count and storage (counts only). CSV/JSONL export, recorded before it is
  served (`access_review_exported`).
- **Disable instead of delete:** a disabled account cannot sign in (password, second factor
  or provider), its sessions end and access tokens are refused at once, its share links
  answer like expired shares, pending uploads and invitations are cancelled; nothing is
  deleted (`user_disabled` / `user_enabled`).
- **Sessions:** Profile -> "Your sessions" with sign-out per session and "Sign out all other
  sessions"; administrators can sign a user out everywhere (`user_signed_out`). Signing a
  session out ends its access tokens immediately. Sessions started through a sign-in
  provider must sign in at the provider again after `provider_session_max_hours` (default
  24), so deprovisioning at the identity provider takes effect.
- **Security notifications** by email: password changed, second factor added or removed,
  recovery codes regenerated, and sign-in from a new browser and network (`notify_new_signin`,
  on by default; only keyed hashes are stored).
- **Password screening:** account passwords are checked against an embedded list of common
  passwords and may not contain the username, email or application name, or be simple
  repetitions or sequences. Optional Have I Been Pwned range check
  (`password_breach_check_online`, off; only 5 hash characters leave the server). The
  account password minimum is now 8 (8-72; lower stored values are raised).
- **Personal data (GDPR):** "Download my data" (JSON export, `personal_data_exported`),
  self-service account deletion, and administrator erasure (`user_erased`): shares, files,
  invites, sessions and factors are deleted and the account is pseudonymised; audit records
  keep the former name until retention removes them (documented exception).
- **Step-up re-authentication** for critical administrator actions (deleting or erasing
  users, role changes, sign-in providers, security settings, backup download/delete/restore,
  MFA reset requests) within `admin_stepup_minutes` (default 5): second factor if enrolled,
  otherwise the password. Administrator sessions end after `admin_idle_logout_minutes`
  (default 15) of inactivity.
- **Administrator network restriction:** `admin_ip_allowlist` limits the administration and
  audit pages to listed addresses (refusals audited; a list without your own address cannot
  be saved; recovery switch `SENDORA_DISABLE_ADMIN_IP_ALLOWLIST=true`).
- **Privacy and terms:** `privacy_url` / `terms_url` links in the footer, sign-in and share
  pages; optional terms acceptance per version (`terms_accepted`).
- **Audit coverage:** backup downloads are recorded before they are served
  (`backup_downloaded`); sign-outs are recorded with the user; idle and absolute session ends
  (`session_expired_idle`, `session_expired`); automatic backups, pruning, clean-up of
  expired share files and automatic account removal appear as system actions.
- **Password changes and resets are audited** as `password_changed` (own profile or an
  administrator's own account) and `password_reset` (via a reset or welcome link), with the
  account as actor; expired or used MFA reset requests are purged automatically.
- **Password maximum age:** `account_password_max_age_days` (0-365, 0 = never; **365 on new
  installations, 0 on installations upgraded with existing accounts**, so nobody is forced to
  change at the upgrade). An expired local password must be changed before anything else (at
  sign-in, or within one access-token lifetime for a running session, which then signs in
  again); guests, provider-only accounts and accounts an administrator exempts under Users
  (`password_rotation_exempt_changed`) are not affected. A banner warns from 14 days before
  expiry and one email (`password_expiry_warning`, en/nl/de/fr/es) is sent 7 days before.
  Audited as `password_expired` and `password_expiry_warning_sent`.
- **Password history:** `account_password_history` (0-24, default 5) refuses the current
  password and the last N; earlier passwords are kept as bcrypt hashes only (newest N per
  account, deleted on erasure).
- **Access review** shows `password_age_days`, `password_rotation_exempt` and
  `password_expires_at`; the profile shows when the password was last changed and when it
  expires.
- **Data minimisation:** download records store the recipient's IP only while audit IP
  logging is on; stored addresses are cleared otherwise.
- **Off-host audit forwarding:** standard output (JSON lines), syslog (RFC 5424 over TCP or
  TLS) and an HTTPS webhook (bearer token from `SENDORA_AUDIT_FORWARD_WEBHOOK_TOKEN_FILE`),
  with a chain-head heartbeat every five minutes; delivery never blocks Sendora.
- **Operations:** readiness probe `GET /api/health/ready` (database, audit log, disk, backup
  age, audit chain) and optional Prometheus metrics at `GET /api/metrics`
  (`SENDORA_METRICS_TOKEN_FILE`).
- Admin: two-step verification, the account password policy, sessions and audit-log
  settings moved to the Security tab. New installations default to `mfa_policy` `optional`.

### Supply chain, image and deployment

- The image is built on Debian 13 "trixie" (Debian 12 left regular security support in July
  2026), with base images pinned by digest, no setuid/setgid programs and OCI labels. A new
  `sendora healthcheck` subcommand replaces wget, and the sqlcipher shell is no longer in the
  runtime image.
- Releases are built natively for amd64 and arm64 (no emulation), only after the full test
  suite passed, and carry GitHub build-provenance and SBOM attestations and a keyless cosign
  signature (see SECURITY.md). A manual release never overwrites a published version.
- CI runs govulncheck, npm audit and an image smoke test; CodeQL and a weekly Trivy image
  scan were added; Dependabot keeps dependencies, base images and actions current; all
  actions are pinned to commit SHAs.
- `deploy/kubernetes.yaml` enforces the Pod Security Standard "restricted", uses its own
  ServiceAccount without a token and ships default-deny NetworkPolicies (replace the
  ingress namespace placeholder); the minimal manifest and the Compose files got resource
  limits and a read-only root filesystem; the HTTPS override uses Caddy pinned by digest
  with only NET_BIND_SERVICE.
- SECURITY.md: image verification steps, response goals, and the corrected note on outgoing
  OpenID Connect requests.

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
