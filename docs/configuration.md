# Configuration

Environment variables and runtime settings.

[Back to the documentation index](README.md)

## Runtime configuration

| Variable | Local default | Purpose |
| --- | --- | --- |
| `HOST` | `127.0.0.1` | HTTP bind address; Docker sets `0.0.0.0` inside the container. |
| `PORT` | `8080` | HTTP listening port; Docker also defaults to 8080. |
| `DATA_DIR` | `./data` | Database and storage root; Docker sets `/data`. |
| `SENDORA_DB_KEY_FILE` | required, no default | Path to the external regular file containing exactly 32 raw key bytes. Owner must be root or the current process UID; no group-write or world permissions. Group-read is allowed for projected `fsGroup` secrets. Never put the key value in an environment variable or command-line argument. |
| `SENDORA_DB_INITIALIZE` | `false` | Explicit `true` permits a new encrypted database only. Disable after private first startup; existing plaintext is never automatically migrated. |
| `APP_URL` | unset | Canonical application URL used for generated links, email links, OAuth callbacks, origin checks, and secure-cookie decisions. |
| `JWT_SECRET` | generated and stored in SQLite | Optional override for the session-signing secret. Must be at least 32 characters with at least 8 distinct ones (Sendora refuses to start otherwise); keep a stable, high-entropy value. `JWT_SECRET_FILE` reads it from a file (Docker/Kubernetes secret) instead. Every `*_FILE` secret (this one, the SMTP password, provider client secrets) must be owned by root or the Sendora user and have no group-write and no world access (for example mode `0400` or `0440`), or Sendora refuses to start; a secret in a plain environment variable is logged as a warning (never the value). |
| `SENDORA_SETUP_CODE` | generated | Optional fixed one-time code for first-run setup (at least 16 characters, not a repeated or sequential pattern), for automated deployments. Without it Sendora generates a code and prints it in the log while no account exists. |
| `SENDORA_VERIFIED_DOWNLOAD_MAX_BYTES` | `17179869184` (16 GiB) | Positive integer byte limit for each temporary verified download snapshot; invalid/nonpositive values fall back to the default. |
| `SENDORA_TRUSTED_PROXIES` | unset | Comma-separated IPs/CIDRs of reverse proxies whose `X-Forwarded-For` / `X-Forwarded-Proto` headers are believed (for example `10.0.0.0/8,127.0.0.1`). Empty means the TCP peer is the client and forwarded headers are ignored. With proxies configured, the right-most forwarded address that is not itself a trusted proxy is the client address used for rate limits and download records, and `X-Forwarded-Proto: https` marks cookies `Secure` and enables HSTS. **Set this when running behind an ingress**, otherwise all visitors share one rate-limit budget. |
| `SENDORA_DISABLE_IP_BANS` | unset | Explicit `true` switches off the address blocking (see *Blocking repeated failed sign-ins*) regardless of the setting. Recovery switch if an administrator blocked their own address; remove it afterwards. |
| `SENDORA_FORCE_LOCAL_LOGIN` | unset | Explicit `true` re-enables local username/password sign-in at runtime even when an administrator turned it off, without changing the database. Recovery switch for a failing external provider; a warning is logged at startup. Remove it afterwards. |
| `TZ` | environment-dependent | Container timezone; Compose sets UTC. |

SMTP, registration, expiry/cleanup policy, share size limits, branding, themes, login providers, and email templates are configured through the administrator interface and persisted in SQLite, not through corresponding SMTP environment variables.

`APP_URL` takes precedence over the administrator `application_url` setting. For a remote deployment, set one canonical origin such as **`https://files.example.com`** before sending emails or configuring OAuth. Do not rely on request `Host` or forwarded headers to supply the public origin. Token-bearing email links and OAuth callbacks require a configured trusted URL for non-loopback access; HTTP localhost URLs remain allowed for development.

## Settings from a ConfigMap or environment variables

Every setting of **Admin → Settings** can also be set by the deployment, which suits Kubernetes ConfigMaps and GitOps:

- The variable name is `SENDORA_` plus the setting key in capitals, for example `SENDORA_SMTP_HOST` or `SENDORA_ALLOW_SELF_REGISTRATION`.
- **The configuration wins.** At every start the value is validated with exactly the same rules as the admin screen and written to the database. The field is then **read-only** in the admin screen, which lists the managed settings at the top. Change the value in the ConfigMap and restart the pod.
- An invalid value **stops the server** with a message naming the variable, so a typo never silently falls back to a default.
- Removing a variable makes the setting editable in the screen again; it keeps its last value.
- **Secrets** (SMTP password, client secrets) belong in a Kubernetes Secret, not in a ConfigMap. Mount the Secret as files and point to them with the `_FILE` suffix, for example `SENDORA_SMTP_PASSWORD_FILE=/run/secrets/sendora-config/smtp-password`. A plain variable also works. Values from files are used as-is apart from a trailing newline.
- Booleans are `true` or `false`; numbers are whole numbers within the same ranges as in the screen.
- Not configurable this way: the session-signing secret (generated, or `JWT_SECRET`), uploaded logo and favicon, and email template texts (edit them under Admin → Email templates, or import JSON).

| Variable | Default | Admin setting |
|---|---|---|
| `SENDORA_ACCOUNT_PASSWORD_MIN_LENGTH` | `8` | `account_password_min_length` |
| `SENDORA_ACCOUNT_PASSWORD_REQUIRE_DIGIT` | `false` | `account_password_require_digit` |
| `SENDORA_ACCOUNT_PASSWORD_REQUIRE_LOWER` | `false` | `account_password_require_lower` |
| `SENDORA_ACCOUNT_PASSWORD_REQUIRE_SYMBOL` | `false` | `account_password_require_symbol` |
| `SENDORA_ACCOUNT_PASSWORD_REQUIRE_UPPER` | `false` | `account_password_require_upper` |
| `SENDORA_ALLOW_PREVIEW` | `true` | `allow_preview` |
| `SENDORA_ALLOW_REVERSE_SHARES` | `true` | `allow_reverse_shares` |
| `SENDORA_ALLOW_SELF_REGISTRATION` | `false` | `allow_self_registration` |
| `SENDORA_APPLICATION_NAME` | `Sendora` | `application_name` |
| `SENDORA_APPLICATION_URL` | empty | `application_url` |
| `SENDORA_AUTO_CLEAN_FILES` | `true` | `auto_clean_files` |
| `SENDORA_BACKUP_KEEP` | `7` | `backup_keep` |
| `SENDORA_BACKUPS_ENABLED` | `true` | `backups_enabled` |
| `SENDORA_CLEAN_FILES_DAYS` | `7` | `clean_files_days` |
| `SENDORA_DEFAULT_EXPIRY_TIME` | `7` | `default_expiry_time` |
| `SENDORA_DEFAULT_LANGUAGE` | `en` | `default_language` |
| `SENDORA_DEFAULT_STORAGE_QUOTA_BYTES` | `10000000000` (10 GB) | `default_storage_quota_bytes` |
| `SENDORA_EXPIRY_WARNING_DAYS` | `2` | `expiry_warning_days` |
| `SENDORA_IDLE_LOGOUT_MINUTES` | `60` | `idle_logout_minutes` |
| `SENDORA_IP_BAN_ALLOWLIST` | empty | `ip_ban_allowlist` |
| `SENDORA_IP_BAN_ENABLED` | `true` | `ip_ban_enabled` |
| `SENDORA_LOCAL_LOGIN_ENABLED` | `true` | `local_login_enabled` |
| `SENDORA_AUDIT_FORWARD_STDOUT` | `false` | `audit_forward_stdout` (see *Forwarding the audit log*) |
| `SENDORA_AUDIT_FORWARD_SYSLOG` | empty (off) | `audit_forward_syslog` (`host:port`) |
| `SENDORA_AUDIT_FORWARD_SYSLOG_TLS` | `true` | `audit_forward_syslog_tls` |
| `SENDORA_AUDIT_FORWARD_WEBHOOK_URL` | empty (off) | `audit_forward_webhook_url` |
| `SENDORA_AUDIT_LOG_IP` | `false` | `audit_log_ip` (also decides whether the recipient address of a download is stored) |
| `SENDORA_AUDIT_RETENTION_DAYS` | `90` | `audit_retention_days` (90 to 365; a stored value below 90 is raised to 90) |
| `SENDORA_MAX_EXPIRY_TIME` | `30` | `max_expiry_time` |
| `SENDORA_MAX_SHARE_SIZE` | `2048` | `max_share_size` |
| `SENDORA_MFA_POLICY` | `optional` (`off` disables two-step verification entirely) | `mfa_policy` |
| `SENDORA_MFA_REMINDER_DAYS` | `1` | `mfa_reminder_days` |
| `SENDORA_MFA_TOTP_ENABLED` | `true` | `mfa_totp_enabled` |
| `SENDORA_MFA_WEBAUTHN_ENABLED` | `true` | `mfa_webauthn_enabled` |
| `SENDORA_REGISTRATION_EMAIL_DOMAINS` | empty | `registration_email_domains` |
| `SENDORA_REQUIRE_EMAIL_VERIFICATION` | `true` | `require_email_verification` |
| `SENDORA_REQUIRE_SHARE_PASSWORD` | `false` | `require_share_password` |
| `SENDORA_SHARE_PASSWORD_MIN_LENGTH` | `8` | `share_password_min_length` |
| `SENDORA_SHARE_PASSWORD_REQUIRE_DIGIT` | `false` | `share_password_require_digit` |
| `SENDORA_SHARE_PASSWORD_REQUIRE_LOWER` | `false` | `share_password_require_lower` |
| `SENDORA_SHARE_PASSWORD_REQUIRE_SYMBOL` | `false` | `share_password_require_symbol` |
| `SENDORA_SHARE_PASSWORD_REQUIRE_UPPER` | `false` | `share_password_require_upper` |
| `SENDORA_SHOW_LANGUAGE_SELECTOR` | `true` | `show_language_selector` |
| `SENDORA_SMTP_FROM` | empty | `smtp_from` |
| `SENDORA_SMTP_HOST` | empty | `smtp_host` |
| `SENDORA_SMTP_PASS` (also `SENDORA_SMTP_PASSWORD`; use `_FILE`) | empty | `smtp_pass` |
| `SENDORA_SMTP_PORT` | `587` | `smtp_port` |
| `SENDORA_SMTP_REPLY_TO` | empty | `smtp_reply_to` |
| `SENDORA_SMTP_TLS` | `starttls` | `smtp_tls` |
| `SENDORA_SMTP_USER` | empty | `smtp_user` |
| `SENDORA_UI_BACKGROUND` | empty | `ui_background` |
| `SENDORA_UI_BUILTIN_BACKGROUND` | `koenigssee` | `ui_builtin_background` |
| `SENDORA_UI_DEFAULT_BACKGROUND` | `true` | `ui_default_background` |
| `SENDORA_UI_SLIDESHOW_SECONDS` | `30` | `ui_slideshow_seconds` |
| `SENDORA_UI_USE_UNSPLASH` | `false` | `ui_use_unsplash` |

### Forwarding the audit log

A copy of the audit log outside the host survives an attacker who controls the server (see the threat model). Admin → Security → Logging, or the variables above, configure up to three destinations at once:

- **Standard output** (`audit_forward_stdout=true`): one JSON line per record, `{"sendora_audit":{...}}`, for the container log collector.
- **Syslog** (`audit_forward_syslog=host:port`): RFC 5424 messages over TCP with octet-counting framing (RFC 6587; RFC 5425 when `audit_forward_syslog_tls=true`, the default, with certificate verification against the system roots). Facility 13 (log audit), severity warning for denied or failed outcomes, informational otherwise; APP-NAME `sendora`, MSGID `audit` or `chain_head`; the message is the same JSON as on standard output.
- **Webhook** (`audit_forward_webhook_url`): HTTPS `POST` of a JSON array of such objects (batches of up to 100), TLS verified, redirects not followed; any 2xx answer counts as delivered. Plain `http://` is accepted for `localhost` only. A bearer token is read from the file named by `SENDORA_AUDIT_FORWARD_WEBHOOK_TOKEN_FILE` (same permission rule as every other `*_FILE` secret); never put credentials in the URL, which is visible in the settings and in the audit log.

Every record is forwarded with all its fields including `seq`, `prev_hash` and `hash`, so the receiver can verify the chain and detect gaps. Every five minutes, at start and at an orderly stop, a heartbeat `{"sendora_audit_chain_head":{"last_seq":…,"hash":"…","ts":"…","reason":"start|periodic|stop"}}` carries the current end of the chain; it is not part of the chain. A head that moves backwards, or records missing below it, means the local log was changed or truncated.

Forwarding never blocks or fails a request: each destination has an in-memory queue of 10,000 records, failed deliveries are retried with exponential backoff (1 second up to 5 minutes), and a full queue drops records and counts them. Delivery is at least once (a retried batch may arrive twice; deduplicate on `seq`). `GET /api/audit/status` and `GET /api/logs/status` report per destination `enabled`, `last_success`, `last_error` (an error class such as `connect`, `tls`, `timeout`, `http_status_503` or `token_file`, never an address or secret), `last_error_at`, `queued`, `dropped`, `failed` and `delivered`. Destinations on link-local addresses (including cloud metadata services) are refused, both when saved and after DNS resolution; private (RFC 1918) and loopback addresses are allowed. When a destination is removed or changed, the record of that settings change is still delivered to the old destination.

### Readiness and metrics

- `GET /api/health` is the liveness probe: the process answers HTTP.
- `GET /api/health/ready` (no authentication) answers `200` or `503` with `{"status":"ok|fail","checks":{...}}`. Checks: `database` (a query), `audit_log` (the last audit write succeeded and the database is writable; no record is written), `disk` (at least 256 MiB free in `DATA_DIR`), `backup` (only while `backups_enabled`: the newest backup is at most 48 hours old, i.e. two automatic backup intervals; tolerated during the first hour after a start) and `audit_chain` (the incremental chain verification; while the start-up verification of a large log is still running the last known result is used). Use it as the Kubernetes readiness probe.
- `GET /api/metrics` serves Prometheus text format and is **off (404)** unless `SENDORA_METRICS_TOKEN_FILE` names a file holding a token (same permission rule as the other `*_FILE` secrets). Scrapers send it as `Authorization: Bearer <token>`. Counters: `sendora_logins_total{result="ok|failed"}`, `sendora_mfa_failures_total`, `sendora_audit_write_failures_total`, `sendora_audit_forward_dropped_total{sink}`, `sendora_audit_forward_failed_total{sink}`, `sendora_decrypt_failures_total`, `sendora_ip_bans_total`, `sendora_downloads_started_total`, `sendora_uploads_completed_total`. Gauges: `sendora_upload_sessions_active`, `sendora_verified_download_slots_in_use`, `sendora_audit_forward_queue_length{sink}`. Counters restart at zero with the process.

| Variable | Purpose |
|---|---|
| `SENDORA_AUDIT_FORWARD_WEBHOOK_TOKEN_FILE` | File with the bearer token for the audit webhook (optional). |
| `SENDORA_METRICS_TOKEN_FILE` | File with the bearer token for `/api/metrics`; without it the endpoint is off. |

### Login providers

Login providers can be configured the same way, with `SENDORA_PROVIDER_<ID>_<FIELD>`. `<ID>` consists of capital letters and digits and keeps the provider's callback URL stable: `<APP_URL>/api/auth/oidc/config-<id>/callback` (in lower case), which you register at the identity provider.

| Field | Meaning |
|---|---|
| `TYPE` | `entra`, `oidc`, `google` or `github` (required) |
| `NAME` | Name on the sign-in button (default: the ID) |
| `TENANT_ID` | Entra directory (tenant) ID |
| `ISSUER_URL` | Issuer URL for `oidc` |
| `CLIENT_ID` | Application (client) ID (required) |
| `CLIENT_SECRET` / `CLIENT_SECRET_FILE` | Client secret value; prefer the `_FILE` form from a Secret |
| `ENABLED` | `true` (default) or `false` |
| `ALLOW_REGISTRATION` | Create accounts at first sign-in: `false` (default) or `true` |
| `TRUST_EMAIL` | Trust the provider's verified email (`false` by default; never used for Entra) |
| `ICON` | Optional icon name |

Example for Microsoft Entra ID:

```sh
SENDORA_PROVIDER_ENTRA_TYPE=entra
SENDORA_PROVIDER_ENTRA_NAME="Company login"
SENDORA_PROVIDER_ENTRA_TENANT_ID=00000000-0000-0000-0000-000000000000
SENDORA_PROVIDER_ENTRA_CLIENT_ID=00000000-0000-0000-0000-000000000000
SENDORA_PROVIDER_ENTRA_CLIENT_SECRET_FILE=/run/secrets/sendora-config/entra-client-secret
SENDORA_PROVIDER_ENTRA_ALLOW_REGISTRATION=true
# callback URL to register in Entra: <APP_URL>/api/auth/oidc/config-entra/callback
```

Configured providers are marked *Managed by configuration* under Admin → Login providers and cannot be edited or deleted there. The type, client ID or issuer of a provider that already has linked accounts cannot change (the server refuses to start); use a new `<ID>` instead. Unknown `SENDORA_PROVIDER_*` variables stop the server, so typos are caught. Removing all variables of a provider makes it editable again.
