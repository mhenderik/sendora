# Multi-tenancy

One Sendora process can serve many organisations ("tenants"), each on its own
subdomain or custom domain, with its own users, settings, branding, SMTP server,
sign-in providers, shares, audit log and backups. Multi-tenancy is **optional**:
without `SENDORA_MULTI_TENANT=true` Sendora runs exactly as a single-tenant
installation and nothing in this document applies.

[Back to the documentation index](README.md)

## 1. Architecture

```
                  ┌───────────────────────── one sendora process ─────────────────────────┐
 klant1.example → │ TenantHost ──► *app.Server (klant1)  ──► tenants/<id1>/sendora.db + files │
 klant2.example → │  (routes by    *app.Server (klant2)  ──► tenants/<id2>/sendora.db + files │
 files.acme.nl  → │   Host)        … opened on demand, closed when idle (LRU)                 │
 admin.example  → │            ──► platform console ──► platform.db (tenants, admins, audit) │
 anything else  → │            ──► plain 404                                                  │
                  └────────────────────────────────────────────────────────────────────────┘
```

- **Platform database** `DATA_DIR/platform.db` (SQLCipher): tenants (random ID,
  subdomain, name, status, quotas, cached counts), custom domains with their DNS
  verification token, the per-tenant storage configuration (hook, see §8), the
  platform administrators (bcrypt, TOTP required) and the platform audit log
  (the same tamper-evident HMAC chain as a tenant's audit log).
- **Tenant data** `DATA_DIR/tenants/<tenant id>/`: the tenant's own SQLCipher
  database `sendora.db` and `shares/`, `uploads/`, `backups/`, `branding/`,
  `backgrounds/` — exactly the layout of a single-tenant `DATA_DIR`. The
  directory name is the random tenant ID, never the subdomain.
- **One server per tenant**: every tenant is served by its own `*app.Server`
  with its own store. Nothing is shared between two tenant servers except the
  process, the embedded web build and the platform's routing table.
- **Lazy open, idle close**: a tenant is opened on its first request and closed
  after `tenant_idle_minutes` (default 15) without requests. At most
  `max_open_tenants` (default 50) are open; when the limit is reached the least
  recently used idle tenant is closed first (a request gets 503 + `Retry-After`
  only when every open tenant is busy). Concurrent first requests open a tenant
  once.
- **Background jobs** (expiry, clean-up, expiry mails, password expiry warnings,
  audit retention, the daily backup) run for every open tenant as in
  single-tenant mode. Every `tenant_maintenance_hours` (default 6) the platform
  also opens each idle tenant briefly so its jobs run, records its counts and
  closes it again. The daily backup is due by the age of the newest backup, not
  by uptime.

## 2. Key derivation

The master key is the 32-byte file named by `SENDORA_DB_KEY_FILE`. In
multi-tenant mode it is **never used directly** as a database key; every
database gets its own key, derived with HKDF-SHA256 (no salt) and a
domain-separated label as the info string:

| Key | Derivation |
| --- | --- |
| platform database | `HKDF-SHA256(master, info = "sendora/v1/platform-db")` |
| tenant database | `HKDF-SHA256(master, info = "sendora/v1/tenant-db/<tenant id>")` |

The tenant ID is 128 random bits (26 lowercase base32 characters). Inside a
tenant, everything else is derived from the tenant's own database key exactly as
in single-tenant mode: the share file master key (`filecrypt.MasterKey`), the
audit chain key, and — because they are random values stored in the tenant's
own database — the access-token signing secret and every HMAC key (refresh
tokens, reset and welcome links, MFA tickets, WebAuthn user handles, credential
versions). `JWT_SECRET` and `APP_URL` are ignored in multi-tenant mode (a shared
signing secret would let a token of one tenant verify in another).

Consequences:

- a token, cookie, refresh token, reset/welcome link, MFA ticket or share link
  of tenant A never verifies in tenant B: B has neither the row nor the key;
- a file of tenant A cannot be decrypted with tenant B's keys;
- copying a tenant's directory elsewhere does not give access without the
  master key and the tenant ID;
- losing the master key loses **all** tenants: keep a protected recovery copy.

## 3. Routing

The request host decides everything:

| Host | Goes to |
| --- | --- |
| `<slug>.<base domain>` | that tenant |
| a **verified** custom domain | that tenant |
| `admin.<base domain>` | the platform console |
| anything else (including the bare base domain and unknown subdomains) | plain `404 page not found`, identical for every unknown host, so hosts cannot be used to find tenants |

- `X-Forwarded-Host` is used only when the TCP peer is in
  `SENDORA_TRUSTED_PROXIES`; otherwise it is ignored. The ports in `Host` are
  ignored for routing.
- A **suspended** tenant answers `503` (a small page for browsers, JSON for the
  API) and is closed once its running requests finish.
- **One origin per tenant**: a tenant's public URL is its *primary* domain —
  `https://<slug>.<base domain>` by default, or a verified custom domain chosen
  as primary. Requests to the tenant's other hosts are redirected there (`308`
  for GET/HEAD, `421` for other methods), so cookies, passkeys (WebAuthn RP ID)
  and links always use one origin. Emails and links use this URL.
- Cookies are host-only (no `Domain` attribute): a tenant's cookies are never
  sent to another tenant or to the console, and the console's cookie never to a
  tenant.
- `/api/health` answers on every host (container health check, orchestrator
  probes).
- For local testing, `SENDORA_BASE_DOMAIN=localhost:9090` serves
  `http://klant1.localhost:9090` and `http://admin.localhost:9090` (browsers
  resolve `*.localhost` to the loopback address; `http` is only used for
  `localhost` base domains, otherwise URLs are `https`).

## 4. Configuration

| Variable | Purpose |
| --- | --- |
| `SENDORA_MULTI_TENANT` | `true` enables multi-tenant mode. |
| `SENDORA_BASE_DOMAIN` | Required. Host name of the platform, optionally with `:port` used only in generated URLs (e.g. `sendora.example.com`, `localhost:9090`). |
| `SENDORA_DB_INITIALIZE` | `true` once, to create `platform.db`. Tenant databases are created by the platform. |
| `SENDORA_PLATFORM_SETUP_CODE` | Optional fixed one-time code for creating the first platform administrator (otherwise generated and printed in the log). |
| `SENDORA_TENANT_IDLE_MINUTES`, `SENDORA_MAX_OPEN_TENANTS`, `SENDORA_TENANT_MAINTENANCE_HOURS`, `SENDORA_DEFAULT_MAX_USERS`, `SENDORA_DEFAULT_MAX_STORAGE_BYTES`, `SENDORA_PLATFORM_AUDIT_RETENTION_DAYS` | Platform settings (also editable in the console unless set here). |
| `SENDORA_PLATFORM_SMTP_HOST`, `_PORT`, `_USER`, `_PASSWORD` (or `_PASSWORD_FILE`), `_FROM`, `_TLS` | Default SMTP server for tenants that have no SMTP server of their own (welcome mails, resets, notifications). |

Every other `SENDORA_<SETTING>` variable keeps its meaning but applies to
**every** tenant and is read-only in each tenant's admin screen — use this for
platform-wide policy (for example `SENDORA_MFA_POLICY=required`). Leave per-tenant
choices (SMTP, branding, providers) to the tenants. `SENDORA_TRUSTED_PROXIES`,
`SENDORA_FORCE_LOCAL_LOGIN`, `SENDORA_DISABLE_IP_BANS` and
`SENDORA_DISABLE_ADMIN_IP_ALLOWLIST` apply to all tenants.

Safety switches: a single-tenant start refuses a `DATA_DIR` that contains
`platform.db`, and a multi-tenant start refuses a `DATA_DIR` that still contains
a single-tenant `sendora.db` (convert it first, §7).

## 5. The platform console

`https://admin.<base domain>` (served only on that host; the tenant hosts never
serve the console page or its API).

- **First administrator**: on first start the log shows
  `platform console setup code: XXXX-XXXX-XXXX`. Open the console, enter the
  code, choose a username and password (at least 12 characters, screened against
  common passwords), then enrol an authenticator app (TOTP). Until TOTP is set
  up the session can do nothing else.
- **Sign-in**: password, then a TOTP code. Time steps cannot be reused (replay),
  five failures lock the account for 15 minutes, sessions end after 30 minutes
  idle or 12 hours. The session cookie is host-only, `HttpOnly`,
  `SameSite=Strict` and `Secure` on https. Unsafe requests from another origin
  are refused.
- **Tenants**: create (subdomain, name, first administrator's email and
  username, quotas), list and search with status, user count and storage used,
  suspend/resume, edit quotas, rename, delete (type the subdomain **and** enter
  a current TOTP code; removes the database, files and backups).
- **First tenant administrator**: created by the platform without a usable
  password plus a one-time "choose your password" link (7 days). With a working
  SMTP server (the platform default) the link is emailed; otherwise the console
  shows it once to pass on. The address counts as verified once the link is used.
- **Domains**: the subdomain is always present; custom domains are added with a
  DNS TXT record `_sendora-challenge.<domain>` = `sendora-verify=<token>` and
  verified from the console. Only verified domains are routed; one can be made
  primary.
- **Storage**: the per-tenant storage choice (§8).
- **Platform settings**, **platform audit log** (with chain verification) and
  **administrators** (adding, removing and resetting another administrator's
  authenticator need a current TOTP code; the last administrator cannot be
  removed; lost authenticator: another administrator resets it).

**What the console never shows**: tenant users, shares, file names, files, or
the tenant's audit records. Counts (users, stored bytes) are the only tenant
data it reads. Every console action is recorded in the platform audit log; the
actions that affect a tenant (created, suspended, resumed, quota changed,
domains, storage, first administrator) also get a record in the tenant's own
audit log with the role `platform_admin`, so the tenant can see what the
platform did.

## 6. Quotas

- **Maximum users** (non-guest accounts; 0 = unlimited): enforced by a database
  trigger in the tenant's database, so every way of creating an account
  (administrator, self-registration, sign-in provider) respects it; the admin
  screen and registration answer with a clear message.
- **Maximum storage** (bytes; 0 = unlimited): stored plus reserved bytes of all
  shares of the tenant, checked when an upload is admitted, in addition to the
  per-user quotas.

Quota changes apply immediately to an open tenant.

## 7. Converting a single-tenant installation (tenant-import)

```sh
# stop the server first
sendora-db tenant-import -data-dir /data -slug acme -name "Acme" \
    -key-file /run/secrets/master.key [-source-key-file /run/secrets/old.key]
```

- Without `-source-key-file` the existing database key is also the master key
  of the multi-tenant installation (the usual case: keep using the same key
  file).
- The command creates `platform.db` if needed, copies the database with
  `sqlcipher_export` under the tenant's derived key (verified), **moves**
  `shares/`, `uploads/`, `branding/` and `backgrounds/` into
  `tenants/<id>/`, writes re-encrypted copies of the backups, verifies the
  result (database integrity, the audit chain against the original, and every
  share file decrypts and matches its checksum), and only then moves the
  original database and backups into `DATA_DIR/pre-tenant-import-<time>/` and
  activates the tenant. Remove that directory once you have checked the tenant.
- **Safe to re-run and to abort**: progress is kept in
  `DATA_DIR/tenant-import.json`. Running the command again continues an
  interrupted conversion; `sendora-db tenant-import -data-dir /data -abort`
  puts everything back (directories return, copies are removed) until the last
  step. The original database is never modified.
- **Share files: legacy root (decision).** Share files are encrypted with keys
  derived from the database key. Re-encrypting every file would rewrite all
  data and leave a mix after a crash. Instead the imported tenant keeps the old
  key as its *legacy root* (`key_mode = legacy`): its database uses the derived
  tenant key, while the file master key and the audit chain key are still
  derived from the old key. Files written before and after the conversion stay
  readable and the audit chain stays verifiable without rewriting it. When the
  old key is the master key, nothing extra is stored; a different old key is
  stored in the (encrypted) platform database. Isolation holds: the other
  tenants' keys are derived and never equal the legacy root.
- Sessions survive only if `JWT_SECRET` was not used (the signing secret then
  lives in the database); otherwise users sign in again. Set the subdomain (or
  add the old host name as a custom domain and make it primary) so existing
  share links keep working.

Offline commands on a tenant database take `-tenant <id>` (the key is derived
from `-key-file`), plus `-legacy-root` for an imported tenant:
`sendora-db audit-verify -source /data/tenants/<id>/sendora.db -tenant <id>`.

## 8. Storage backends (integration point)

Pluggable object storage (S3, Azure Blob, GCS) is developed separately
(`internal/storage`). Multi-tenancy only stores each tenant's choice in the
platform database, table `tenant_storage`:

| Field | Meaning |
| --- | --- |
| `mode` | `platform`: the platform's default backend with `prefix` (default `tenants/<id>/`) before every object key, so tenants never share a key space. `own`: the tenant's own bucket/container. |
| `prefix` | Object key prefix in `platform` mode. |
| `config` | Opaque JSON in the storage package's format for `own` mode (e.g. `{"type":"s3","bucket":"…","region":"…","access_key_id":"…","secret_access_key":"…"}`). Secret-looking values are masked in the console and kept when saved unchanged. |

Wiring after both branches are merged: `TenantHost.openTenant`
(`internal/app/mt_host.go`) already passes the configuration to every tenant
server as `TenantOptions.Storage`
(`*platform.StorageConfig`). The storage integration builds the tenant's backend
from it in `NewTenantServer` (`internal/app/mt_tenant.go`) — for `platform`
mode the process-wide backend wrapped with `Prefix`, for `own` mode a backend
from `Config` — instead of reading the process-wide storage setting, and keeps
the tenant's directory as the local default. A changed configuration closes the
tenant so the next request reopens it with the new backend. Deleting a tenant
does not remove objects in its own bucket.

## 9. DNS, TLS and Kubernetes

- DNS: a wildcard record `*.sendora.example.com` (and `admin.` is covered by it)
  pointing to the ingress; custom domains need their own record (CNAME to the
  platform) plus the TXT verification record.
- TLS: a wildcard certificate for `*.sendora.example.com` needs a DNS-01
  challenge. Example with cert-manager:

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: { name: sendora-wildcard, namespace: sendora }
spec:
  secretName: sendora-wildcard-tls
  dnsNames: ["sendora.example.com", "*.sendora.example.com"]
  issuerRef: { name: letsencrypt-dns01, kind: ClusterIssuer }
```

  Custom domains get their own certificate (HTTP-01 works for them).
- The ingress must pass the original `Host` (or `X-Forwarded-Host`) and the
  proxy addresses must be in `SENDORA_TRUSTED_PROXIES`.
- Full example: [`deploy/kubernetes-multi-tenant.yaml`](../deploy/kubernetes-multi-tenant.yaml)
  (wildcard Ingress and TLS, ConfigMap, Secret, one replica).

## 10. Backup and restore per tenant

- Each tenant's automatic and manual backups are in `tenants/<id>/backups/`,
  encrypted with the tenant's key. A tenant administrator downloads them as
  before.
- The in-app **database import is disabled** for tenants (it restarts the whole
  process). The operator restores a tenant offline: stop the process, replace
  `tenants/<id>/sendora.db` with the backup (it is already encrypted with the
  tenant key; check it first with
  `sendora-db verify -source <backup> -tenant <id>`), remove the `-wal`/`-shm`
  files, start again.
- A full platform backup is the whole `DATA_DIR` (platform database plus all
  tenant directories, taken while stopped or from a consistent volume snapshot)
  plus the master key.

## 11. Limits

- **One process, SQLite**: all tenants share one process, CPU and memory, and
  each tenant database allows one writer at a time. Run one replica (the data
  directory is a single `ReadWriteOnce` volume); scale vertically. A heavy
  tenant can slow others; quotas limit storage and users, not request rates
  (the per-tenant rate limits still apply).
- Opening a tenant costs a few milliseconds plus the start-up work of a server
  (audit verification runs in the background). Keep `max_open_tenants` above
  the number of simultaneously active tenants.
- Each lazy open and idle close writes `system_start`/`system_stop` records to
  the tenant's audit log.
- The platform operator holds the master key and can, with offline tools,
  decrypt any tenant. Multi-tenancy isolates tenants from each other and from
  the platform *console*, not from the operator of the host (see the threat
  model).
- A tenant's custom domain is reached only after DNS verification; the
  platform does not issue TLS certificates itself.
