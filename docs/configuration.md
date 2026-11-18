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
| `SENDORA_DISABLE_ADMIN_IP_ALLOWLIST` | unset | Explicit `true` switches off the administrator network restriction (`admin_ip_allowlist`, see *Administrator network restriction*) and allows saving a list that does not contain your own address. Recovery switch after locking yourself out; remove it afterwards. |
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
| `SENDORA_ACCOUNT_PASSWORD_MIN_LENGTH` | `8` | `account_password_min_length` (8 to 72; a stored value below 8 is raised to 8) |
| `SENDORA_ADMIN_IDLE_LOGOUT_MINUTES` | `15` | `admin_idle_logout_minutes` (0 = same as `idle_logout_minutes`; otherwise administrators get the stricter of the two) |
| `SENDORA_ADMIN_IP_ALLOWLIST` | empty | `admin_ip_allowlist` (IP addresses and CIDR ranges; empty = no restriction) |
| `SENDORA_ADMIN_STEPUP_MINUTES` | `5` | `admin_stepup_minutes` (1 to 60) |
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
| `SENDORA_AUDIT_LOG_IP` | `false` | `audit_log_ip` |
| `SENDORA_AUDIT_RETENTION_DAYS` | `90` | `audit_retention_days` (90 to 365; a stored value below 90 is raised to 90) |
| `SENDORA_MAX_EXPIRY_TIME` | `30` | `max_expiry_time` |
| `SENDORA_MAX_SHARE_SIZE` | `2048` | `max_share_size` |
| `SENDORA_MFA_POLICY` | `optional` (`off` disables two-step verification entirely) | `mfa_policy` |
| `SENDORA_MFA_REMINDER_DAYS` | `1` | `mfa_reminder_days` |
| `SENDORA_MFA_TOTP_ENABLED` | `true` | `mfa_totp_enabled` |
| `SENDORA_MFA_WEBAUTHN_ENABLED` | `true` | `mfa_webauthn_enabled` |
| `SENDORA_NOTIFY_NEW_SIGNIN` | `true` | `notify_new_signin` |
| `SENDORA_PASSWORD_BREACH_CHECK_ONLINE` | `false` | `password_breach_check_online` |
| `SENDORA_PRIVACY_URL` | empty | `privacy_url` (https) |
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
| `SENDORA_TERMS_ACCEPTANCE_REQUIRED` | `false` | `terms_acceptance_required` (needs `terms_url` and `terms_version`) |
| `SENDORA_TERMS_URL` | empty | `terms_url` (https) |
| `SENDORA_TERMS_VERSION` | empty | `terms_version` |
| `SENDORA_UI_BACKGROUND` | empty | `ui_background` |
| `SENDORA_UI_BUILTIN_BACKGROUND` | `koenigssee` | `ui_builtin_background` |
| `SENDORA_UI_DEFAULT_BACKGROUND` | `true` | `ui_default_background` |
| `SENDORA_UI_SLIDESHOW_SECONDS` | `30` | `ui_slideshow_seconds` |
| `SENDORA_UI_USE_UNSPLASH` | `false` | `ui_use_unsplash` |

### Account security

- **Password screening.** Account passwords (setup, registration, profile, reset, administrator-created users) are refused when they are on the embedded list of common passwords (`internal/app/data`, reproducible with `gen_common_passwords.go`), contain the username, the local part of the email address or the application name, or are a simple repetition or sequence. Share passwords keep their own rules. With `password_breach_check_online=true` new passwords are also checked against the Have I Been Pwned range API: only the first 5 hex characters of the SHA-1 hash are sent (k-anonymity), with a 3-second timeout; when the service cannot be reached the password is accepted and a warning is logged.
- **Security notifications.** Users with a verified email address get an email when their password is changed (also through a reset link), a second factor is added or removed, or recovery codes are regenerated, and (setting `notify_new_signin`) when a sign-in comes from a browser family and network (/16 for IPv4, /48 for IPv6) not seen for the account in the last 30 days. Only keyed hashes of that combination are stored, never addresses. A failing mail server never blocks the action.
- **Step-up re-authentication.** Deleting or erasing users, changing administrator/auditor roles, creating, changing or deleting sign-in providers, changing security settings (`mfa_*`, `local_login_enabled`, `application_url`, `audit_*`, `ip_ban_*`, the account password policy, `admin_*`, `provider_session_max_hours`, `password_breach_check_online`, `notify_new_signin`), downloading, deleting and restoring backups, and MFA reset requests need a sign-in or confirmation not older than `admin_stepup_minutes`. The browser asks for the second factor (authenticator code or passkey) or, without one, the password. Administrators who sign in only through a provider and have no second factor sign in at the provider again. Administrator sessions also end after `admin_idle_logout_minutes` of inactivity.

### Administrator network restriction

`admin_ip_allowlist` limits the administration routes and the audit log to client addresses in the list (one IP address or CIDR range per line; empty means no restriction). The client address is determined as everywhere else: forwarded headers count only from `SENDORA_TRUSTED_PROXIES`. Refused requests get `403` and an `access_denied` audit record. Saving a list that does not contain your own current address is refused. If you lock yourself out (for example after the office address changed), restart with `SENDORA_DISABLE_ADMIN_IP_ALLOWLIST=true`, fix the list and remove the variable again.

### Privacy and terms

`privacy_url` and `terms_url` (https) are shown in the footer, on the sign-in page and on public share pages. With `terms_acceptance_required=true` (and a `terms_version`) every signed-in account accepts the current version once (`terms_accepted` in the audit log); until then only its profile, the acceptance, the personal data export and deleting the account work. Changing `terms_version` asks everyone again. Users can download their data (Profile → Your data) and delete their account; administrators can erase an account under Users (see `docs/THREAT-MODEL.md` for what remains in the audit log).

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
