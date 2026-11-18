# Audit events

This catalogue lists every audit action the Sendora server writes, so that an auditor can judge coverage and a SIEM engineer can build rules. It was compiled from the code (every `logEvent`, `logShare`, `critical`/`auditCritical`, `auditAccessDenied` and `auditDecryptFailed` call in `internal/app`, plus the generic rules in `auditMiddleware`/`auditOutcome` and `classifyAction` in `internal/app/audit.go`). The chain itself is implemented in `internal/store/audit.go`.

Applies to version 0.1.5.

[Back to the compliance index](README.md) · wiki: [Audit log](https://github.com/mhenderik/sendora/wiki/Audit-Log)

## 1. Record structure

Every record has these fields (table `audit_log`; same names in the JSON Lines export and in forwarded records):

| Field | Content |
|---|---|
| `seq` | Monotonic sequence number |
| `ts` | UTC timestamp, microseconds (`2006-01-02T15:04:05.000000Z`) |
| `category` | `auth`, `account`, `admin`, `security`, `share` or `system` |
| `actor_id` | User ID; `0` for system, anonymous and public actors |
| `actor_name` | Username at the time of the event (max. 100 characters), or `system`, `public`, `unknown` or empty |
| `actor_role` | `admin`, `auditor`, `user`, `guest`, `system` or `anonymous` |
| `action` | Action name (tables below), or `METHOD /route/pattern` for generic records |
| `target` | Object label (max. 160 characters), see 1.1 |
| `outcome` | `ok`, `denied`, `failed` or `attempted` |
| `detail` | Short free text (max. 2,000 bytes). Never passwords, tokens, secrets, secret setting values, share names, link IDs, file names or file contents |
| `ip` | Client address. Stored **only** while the setting `audit_log_ip` is on (default off). Empty otherwise |
| `prev_hash`, `hash` | Chain: `hash = HMAC-SHA256(audit key, canonical record incl. seq and prev_hash)`. The audit key is derived from the database key with HKDF |

### 1.1 Target formats

| Format | Used for |
|---|---|
| `share#<id>` | Shares (internal row ID; never the share name or link ID) |
| `file#<id>` | Stored files (row ID only) |
| `user#<id> (<username>)` | Accounts (written by handlers through `userTarget`) |
| `user#<id>` / `user` | Accounts in the `attempted` record of a critical action (URL ID only, or `user` when the route has no ID) |
| `invite#<id>` | Reverse-share invitations |
| `provider#<id>`, `provider` | Sign-in providers |
| `backup`, `<backup file name>` | Backups |
| `settings`, `audit_log`, `access_review` | Fixed labels |
| `lang:<code>` | Interface translations |
| `<route pattern>` or `METHOD <path>` | `access_denied` records |
| empty | Events without an object (sign-in, start/stop, ...) |

### 1.2 Category and outcome rules (`classifyAction`)

- Outcome: an action ending in `_failed` (and `backup_failed_auto`) is `failed`; `login_lockout`, `access_denied` and any action ending in `_denied` are `denied`; everything else is `ok`. The critical-action rule (section 3) adds `attempted`.
- Category: an explicit list maps actions to `auth`, `security`, `system` and `account` (see the tables). Of the rest, actions starting with `share`, `download_` or `invite_` are `share`, actions starting with `user_` are `account`, and everything else is `admin`.

## 2. Generic rules (written by the middleware)

The middleware `auditMiddleware` wraps every route. After the handler ran, it writes at most one record that the handler did not write itself:

| Action | Category | Written when | Actor | Target | Detail | Outcome |
|---|---|---|---|---|---|---|
| *(critical action name)* | as the action | A critical action (section 3) finished and the handler wrote no record of its own, also when the handler panicked (status 500) | caller | as in the `attempted` record | `status=<HTTP code>` | `ok` (<400), `denied` (401/403), `failed` (other) |
| `access_denied` | `security` | A request from a **signed-in, verified** user ended with 401 or 403 and the handler wrote no record. Requests without a valid token (missing, expired, revoked, forged) are never recorded, so the log cannot be flooded from the internet | caller | route pattern | `<METHOD> status=<code>` | `denied` |
| `METHOD /route/pattern` (written as `api_mutation` rule) | by route: `/api/shares*`, `/api/uploads*`, `/api/reverse-shares*` → `share`; `/api/auth*` → `auth`; `/api/users*` → `account`; `/api/setup` → `system`; else `admin` | Any POST, PUT, PATCH or DELETE request for which no other record was written in this request, except the exempt routes below | caller, or `anonymous` for public routes | empty | `status=<code>` | `ok`, `denied` or `failed` by status |

Note on the generic mutation rule: the `action` field holds the method and route pattern (for example `PUT /api/users/me`); the name `api_mutation` is the rule, not a stored value. Public mutating routes without their own event (share unlock, forgot password, register, verify email, resend verification, invitation accept, first-run setup) therefore produce a generic record with actor `anonymous` and their HTTP status, as long as they reach the router (rate limits and IP bans apply first, see `internal/app/security.go`).

Routes deliberately **not** recorded by the generic rule (`auditExempt`; `TestAuditCoversEveryMutatingRoute` keeps this list exact):

| Route | Reason in the code |
|---|---|
| `POST /api/auth/refresh` | Silent token rotation; session reuse and sign-in events are recorded explicitly |
| `PATCH /api/uploads/{id}/files/{idx}` | One request per uploaded chunk; upload start and finished share are recorded |
| `POST /api/auth/mfa/webauthn/begin` | Challenge without effect; finish and failures are recorded |
| `POST /api/users/me/mfa/webauthn/begin` | Challenge without effect; finish is recorded |
| `POST /api/users/me/mfa/totp/begin` | Secret preparation without effect; confirm is recorded |
| `POST /api/email-templates/preview` | Renders a preview, stores nothing |

`access_denied` is also written explicitly (actor = caller, outcome `denied`) in two places:

| Where | Category | Target | Detail |
|---|---|---|---|
| A signed-in user addresses a share owned by another account (`ownedShare`; the response is 404) | `share` | `share#<id>` | `share belongs to another account` |
| A request to an administration or audit route from an address outside `admin_ip_allowlist` (`adminNetwork`; response 403) | `security` | `<METHOD> <path>` | `admin_ip_allowlist` |

## 3. Critical actions: `attempted`, then the outcome

For the routes in the table below the route middleware `critical(...)` writes a record with outcome **`attempted` before the handler runs**. If that record cannot be stored (after one retry), the request is refused with **503** and the action is not performed. When the URL names an object that does not exist (only for routes with a target resolver, i.e. backup download), the request is answered 404 before anything happens.

The outcome is then either the handler's own record (normally with the same action name and a richer target such as `user#5 (alice)`), or, when the handler wrote nothing, the generic critical outcome from section 2 (`status=<code>`). For routes that also require step-up (marked S), a missing step-up is refused **before** the `attempted` record and appears as `access_denied` with status 401.

| Action | Route | Step-up | Category | `attempted` target / detail | Outcome record written by |
|---|---|---|---|---|---|
| `personal_data_exported` | `GET /api/users/me/export` | | admin | `user` | middleware (`status=`) |
| `audit_exported` | `GET /api/audit/export`, `GET /api/logs/export` | | security | `audit_log` | handler (see 4.6) |
| `access_review_exported` | `GET /api/access-review/export` | | security | `access_review` | handler (see 4.6) |
| `user_created` | `POST /api/users` | | account | `user` | handler |
| `user_updated` | `PUT /api/users/{id}` | S only for role changes (checked in the handler, after `attempted`) | account | `user#<id>` | handler; a missing step-up gives outcome `denied`, `status=401` |
| `user_deleted` | `DELETE /api/users/{id}` | S | account | `user#<id>` | handler |
| `user_erased` | `POST /api/users/{id}/erase` | S | account | `user#<id>` | handler |
| `user_force_reset` | `POST /api/users/{id}/force-reset-password` | | account | `user#<id>` | handler |
| `mfa_reset_requested` | `POST /api/users/{id}/mfa/reset` | S | account | `user#<id>` | handler |
| `user_disabled` | `POST /api/users/{id}/disable` | | account | `user#<id>` | handler |
| `user_enabled` | `POST /api/users/{id}/enable` | | account | `user#<id>` | handler |
| `user_signed_out` | `POST /api/users/{id}/sessions/revoke` | | account | `user#<id>` | handler |
| `settings_changed` | `PUT /api/settings` | S for security settings (checked in the handler) | admin | `settings` | handler |
| `provider_created` | `POST /api/auth-providers` | S | admin | `provider` | middleware |
| `provider_updated` | `PUT /api/auth-providers/{id}` | S | admin | `provider#<id>` | middleware |
| `provider_deleted` | `DELETE /api/auth-providers/{id}` | S | admin | `provider#<id>` | middleware |
| `backup_created` | `POST /api/backups` | | admin | `backup` | handler |
| `backup_downloaded` | `GET /api/backups/{name}` | S | security | `<file name>` / `size=<n> bytes` | middleware |
| `backup_deleted` | `DELETE /api/backups/{name}` | S | admin | `backup#<name>` | handler |
| `db_import_prepared` | `POST /api/backups/import` | | admin | `backup` | handler |
| `db_import_applied` | `POST /api/backups/import/apply` | S (and the administrator's local password) | admin | `backup` | handler |

## 4. Catalogue of explicit actions

Actor notation: *caller* = the signed-in user making the request (`actor_role` from the account); *subject* = the account the event is about, acting on its own behalf; `system` = background job (`actor_id` 0, role `system`); `public` = anonymous share recipient (role `anonymous`).

### 4.1 Authentication and sessions (category `auth` unless noted)

| Action | When it is written | Actor | Target | Detail | Outcome |
|---|---|---|---|---|---|
| `login_ok` | Successful local sign-in without MFA step; after a successful MFA step (together with `mfa_login_ok`); successful provider sign-in | subject | empty | empty, or `provider <name>` | ok |
| `login_failed` | Wrong password for an existing account; unknown username (actor `unknown`, id 0); sign-in while locked; sign-in to a disabled account (password, MFA or provider path); provider callback errors (actor `unknown`) | subject or `unknown` | empty | empty, `account locked`, `account disabled[; <method>]`, `provider <name>`, `account disabled; provider <name>` | failed |
| `login_lockout` | 5th consecutive failure (lock 15 minutes); password, second-step and step-up failures count on the same per-account counter | subject | empty | `locked after repeated failures` | denied |
| `mfa_login_ok` | Second step succeeded | subject | empty | `totp`, `recovery` or `webauthn` | ok |
| `mfa_failed` | Wrong TOTP/recovery code or passkey assertion at sign-in; MFA attempt while locked; failed passkey registration | subject | empty | method, `account locked`, `webauthn registration` | failed |
| `mfa_enrolled` | Authenticator app confirmed; passkey registered; recovery codes regenerated | subject | empty | `totp`, `webauthn`, `recovery codes regenerated` | ok |
| `mfa_removed` | Authenticator app or passkey removed by the user | subject | empty | `totp`, `webauthn` | ok |
| `logout` | Sign-out with an identifiable session | subject | empty | empty | ok |
| `session_expired` | Refresh attempted after the absolute 30-day session limit | subject | empty | `absolute session limit reached` | ok |
| `session_expired_provider` | Provider-only session older than `provider_session_max_hours` | subject | `user#<id> (<name>)` | `after <n> hours` | ok |
| `session_expired_idle` | Session idle longer than the effective idle limit | subject | empty | `idle_logout_minutes=<n>` | ok |
| `session_reuse_detected` | A rotated (spent) refresh token was presented again; all sessions of the account are ended | subject | empty | `all sessions signed out` | ok |
| `session_revoked` | User ended one of their own sessions, or all other sessions | subject | `user#<id> (<name>)` | `one session`, `current session`, `other sessions: <n>` | ok |
| `stepup_ok` (category admin) | Step-up re-authentication succeeded | subject | empty | `password`, `totp` or `webauthn` | ok |
| `stepup_failed` (category admin) | Step-up re-authentication failed | subject | empty | method | failed |

Classified as `auth` in `classifyAction` but **not written by any code path in 0.1.5**: `password_changed`, `password_reset`. A user's own password change (`PUT /api/users/me`) and a password reset through an emailed link (`POST /api/auth/reset-password`) are recorded only by the generic mutation rule (method, route, status; the reset as `anonymous`). An administrator's account update that changed a password would carry the detail `password changed` on `user_updated`, but administrators cannot set another user's password.

### 4.2 Accounts (category `account` unless noted)

| Action | When it is written | Actor | Target | Detail | Outcome |
|---|---|---|---|---|---|
| `user_created` | Administrator created an account | caller | `user#<id> (<name>)` | empty or `welcome=<sent/skipped/...>` | ok |
| `user_updated` | Administrator updated an account | caller | `user#<id> (<name>)` | empty or `password changed` | ok |
| `user_email_changed` | Administrator changed another user's email (address becomes unverified, user signed out, old address notified) | caller | `user#<id> (<name>)` | `email_verified reset` | ok |
| `user_role_changed` | Administrator or auditor flag changed | caller | `user#<id> (<name>)` | `admin=<bool> auditor=<bool>` | ok |
| `mfa_policy_changed` (category admin) | Per-user MFA policy changed (target user) or global `mfa_policy` changed (target `settings`) | caller | `user#<id> (<name>)` or `settings` | `policy=<value>` | ok |
| `user_deleted` | Account deleted (with its shares and files) | caller | `user#<id> (<name>)` captured before deletion | empty | ok |
| `user_erased` | Account erased (pseudonymised, content deleted) by an administrator or by the user | caller | `user#<id> (<former name>)` | `by administrator` or `self-service` | ok |
| `user_force_reset` | Administrator forced a password change | caller | `user#<id> (<name>)` | empty | ok |
| `user_password_reset_sent` | Reset link mailed after a forced reset | caller | `user#<id> (<name>)` | `reset_email=<outcome>` | ok |
| `user_unlocked` | Administrator lifted a sign-in lockout | caller | `user#<id> (<name>)` | empty | ok |
| `user_welcome` | Administrator (re)sent the welcome email | caller | `user#<id> (<name>)` | `welcome=<outcome>` | ok |
| `user_disabled` | Account disabled (sessions, uploads and invites revoked) | caller | `user#<id> (<name>)` | `uploads revoked: <n>, invites revoked: <n>[, reason: <text>]` — the reason is free text entered by the administrator | ok |
| `user_enabled` | Account re-enabled | caller | `user#<id> (<name>)` | empty | ok |
| `user_signed_out` | Administrator signed a user out everywhere | caller | `user#<id> (<name>)` | `sessions: <n>` | ok |
| `mfa_reset_requested` | Administrator requested an MFA reset (confirmation link mailed to the user) | caller | `user#<id> (<name>)` | empty | ok |
| `mfa_reset_confirmed` | User confirmed the MFA reset link; factors removed | subject | `user#<id> (<name>)` | `requested_by=<admin id>` | ok |
| `terms_accepted` (category admin) | User accepted the current terms version | subject | `user#<id> (<name>)` | `version=<v>` | ok |

### 4.3 Shares, downloads and invitations (category `share` unless noted)

| Action | When it is written | Actor | Target | Detail | Outcome |
|---|---|---|---|---|---|
| `share_created` | Upload completed, share created | caller (user or guest) | `share#<id>` | `<n> files` | ok |
| `share_files_added` | Files appended to an existing share | caller | `share#<id>` | `<n> files` | ok |
| `share_file_removed` | Owner removed one file | caller | `share#<id>` | `file#<id>` | ok |
| `share_edited` | Owner edited name, description, password or limit | caller | `share#<id>` | comma-separated list of changed field names (no values) | ok |
| `share_expired_manual` | Owner expired a share | caller | `share#<id>` | empty | ok |
| `share_extended` | Owner extended the expiry | caller | `share#<id>` | `<n> days` | ok |
| `share_expiry_changed` | Owner set a new expiry date | caller | `share#<id>` | empty | ok |
| `share_limit_changed` | Owner changed the download limit | caller | `share#<id>` | empty | ok |
| `share_deleted` | Owner deleted a share, or pruned own expired shares | caller | `share#<id>` | empty or `prune` | ok |
| `download_started` | A public download started (**sampled**, see section 5) | `public` | `share#<id>` | `all files` or `file#<id>` | ok |
| `share_preview` | A public in-browser preview (**sampled**) | `public` | `share#<id>` | empty | ok |
| `shares_expired` | Maintenance job marked shares expired (time or download limit) | system | empty | `<n> shares` | ok |
| `invite_created` | User created a reverse-share invitation | caller | `invite#<id>` | `email` or `open` | ok |
| `invite_revoked` | User revoked an invitation | caller | `invite#<id>` | empty | ok |
| `invite_accepted` | A guest accepted an invitation (guest session started) | guest | `invite#<id>` | empty | ok |

### 4.4 Administration (category `admin` unless noted)

| Action | When it is written | Actor | Target | Detail | Outcome |
|---|---|---|---|---|---|
| `settings_changed` | Settings saved | caller | `settings` | changed keys with `'old' -> 'new'` (values max. 80 characters, control characters removed); for secret settings (`secretKeys`, names containing `secret`, ending `_pass`, `_password`, `_token`, `_key`) and email templates (`tpl_*`) **only the key name**; `no changes`; long lists end with `(+N more)` | ok |
| `audit_retention_changed` | `audit_retention_days` changed (in addition to `settings_changed`) | caller | `settings` | `'old' -> 'new'` | ok |
| `audit_ip_logging_changed` | `audit_log_ip` changed | caller | `settings` | `'old' -> 'new'` | ok |
| `application_url_changed` | `application_url` changed | caller | `settings` | `'old' -> 'new'` | ok |
| `provider_created`, `provider_updated`, `provider_deleted` | Sign-in provider changed (critical; outcome by the middleware) | caller | `provider[#<id>]` | `status=<code>` | by status |
| `backup_created` | Administrator made a backup | caller | `<file name>` | empty | ok |
| `backup_deleted` | Administrator deleted a backup | caller | `<file name>` | empty | ok |
| `db_import_prepared` | Uploaded database validated and re-keyed | caller | empty | `users=<n> admins=<n> shares=<n> files=<n>` | ok |
| `db_import_applied` | Import applied (the instance's own audit log is kept) | caller | name of the pre-import backup (`sendora-preimport-…`) | `users=<n> admins=<n> shares=<n> files=<n>` | ok |
| `ui_language_imported` | Interface translation imported | caller | `lang:<code>` | `<n> keys` | ok |
| `ui_language_deleted` | Interface translation deleted | caller | `lang:<code>` | empty | ok |
| `ip_unbanned` (category security) | Administrator lifted an IP ban | caller | empty | empty | ok |

Other administrator mutations (branding, backgrounds, themes, email templates and languages, test email, provider unlinking by a user, WebAuthn key rename, upload start/abort, step-up WebAuthn challenge, ...) are covered by the generic mutation rule in section 2.

### 4.5 Security and system events

| Action | Category | When it is written | Actor | Target | Detail | Outcome |
|---|---|---|---|---|---|---|
| `ip_banned` | security | A client address exceeded 10 failed credential checks in 10 minutes and was banned (30 min, doubling per strike, max. 24 h) | empty name, role `anonymous` | empty | `strike=<n> minutes=<n>` (the address only in `ip`, and only with `audit_log_ip`) | ok |
| `file_decrypt_failed` | security | A stored file failed decryption or authentication on download, range, ZIP or preview (**rate-limited**, section 5) | system | `file#<id>` | empty | failed |
| `key_fingerprint_changed` | security | At start, the database key fingerprint differs from the last recorded one | system | empty | `was <fp> now <fp>` (fingerprint = 8 bytes of HMAC, not the key) | ok |
| `audit_viewed` | security | A page of the audit log was read (`/api/audit` or `/api/logs`) (**rate-limited**) | caller | `audit_log` | empty | ok |
| `system_start` | system | Process start | system | empty | `version=<v> key=<fingerprint>` | ok |
| `system_stop` | system | Orderly shutdown (after requests and jobs finished), and before a database import swaps the database | system | empty | empty | ok |
| `audit_purged` | system | Retention removed records older than `audit_retention_days` | system | empty | `<n> records past retention` | ok |
| `audit_purge_failed` | system | Retention purge started to fail (written once per failure period, not on every run) | system | empty | `retention purge failed` or `retention purge refused: the records to be removed do not verify` | failed |
| `backup_created_auto` | system | Automatic backup (every 24 h while `backups_enabled`) | system | `<file name>` | empty | ok |
| `backup_failed_auto` | system | Automatic backup failed | system | empty | `automatic backup failed; details are in the server log` | failed |
| `backup_pruned` | system | Old automatic backup removed beyond `backup_keep` | system | `<file name>` | `backup_keep=<n>` | ok |
| `share_files_purged` | system | Shares expired more than `clean_files_days` ago were removed | system | empty | `<n> shares removed, <n> failed (expired more than <d> days ago)` | ok |
| `account_removed_auto` | system | Unverified self-registration older than 48 h removed; guest account of an expired, unused open-link invitation removed | system | `user#<id> (<name>)` | `reason=email not verified within 48 hours of registration` or `reason=guest invitation expired unused` | ok |

### 4.6 Audit log and access-review exports

| Action | When | Actor | Target | Detail |
|---|---|---|---|---|
| `audit_exported` | After the `attempted` record and the size check, before the export is streamed | caller (administrator or auditor) | `audit_log` | `<csv or jsonl> <n> records` plus the filters used (`q=`, `category=`, `action=`, `actor=`, `outcome=`, `from=`, `to=`; values max. 40 characters) |
| `access_review_exported` | Before the access-review report is served | caller | `access_review` | `<csv or jsonl> <n> accounts` |

## 5. Rate-limited and sampled events

| Event | Rule | Where |
|---|---|---|
| `download_started` | Only the first request of a download (no `Range` header, or a range starting at byte 0), and at most **one record per share per 5 minutes** (in-memory map, max. 4,096 shares, reset when full). The `downloads` table still records every download start. For the `POST` form of the download route, a request that is sampled out is recorded by the generic mutation rule instead | `download_log.go`, `shares.go` |
| `share_preview` | First request of a media session only, at most **one record per share per 5 minutes** | `preview_log.go`, `shares.go` |
| `file_decrypt_failed` | At most **once per file per 10 minutes** | `integrity.go` |
| `audit_viewed` | At most **once per actor per 5 minutes** | `audit.go` (`auditViewedOnce`) |
| `audit_purge_failed` | Once when purging starts to fail; again only after a successful purge | `audit.go` |
| `access_denied` | Only for signed-in, verified users (see section 2) | `audit.go` |

## 6. Forwarded-only record: `chain_head`

The heartbeat is **not** stored in `audit_log` and is not part of the chain. It is sent only to the configured forwarding destinations (standard output, syslog, webhook) at start, every 5 minutes and at an orderly stop:

```json
{"sendora_audit_chain_head":{"last_seq":1234,"hash":"…","ts":"…","reason":"start|periodic|stop"}}
```

On syslog it has MSGID `chain_head` (audit records use `audit`). A head that moves backwards, or a gap below it at the receiver, indicates truncation or modification of the local log. See [configuration: Forwarding the audit log](../configuration.md#forwarding-the-audit-log).

## 7. What is not recorded

- Read access is not recorded in general (listing shares, reading settings, user lists, the access-review screen). Exceptions: reading the audit log (`audit_viewed`), exports, backup downloads, public downloads and previews (sampled).
- Requests without valid credentials that end in 401 (they would allow flooding from the internet).
- Offline actions with the `sendora-db` tool (`mfa-reset`, `backup`, `migrate`, `verify`, `verify-files`, `audit-verify`): it does not write audit records. Control them on the host.
- Changes made directly in the database file by someone holding the key: they are detected by the chain verification (`/api/audit/status`, readiness check `audit_chain`, `sendora-db audit-verify`), not prevented.
- HTTP access logging: Sendora writes no access log; use the reverse proxy's log if required.
