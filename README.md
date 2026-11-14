# Sendora

<p align="center"><img src="web/public/logo.svg" alt="Sendora logo" width="96" height="96"></p>

Sendora is an MIT-licensed, open source, self-hosted file-sharing application with a Go backend (chi and SQLCipher-encrypted SQLite) and a Vue 3 frontend embedded in one executable.

This is an implementation under development, not a claim of a full penetration test or production readiness. Validate external integrations and your deployment before exposing a public service.

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

## Requirements

For Docker deployment:

- Docker Engine/Desktop and the Docker Compose plugin (`docker compose`).
- Network access to download base images and build dependencies.
- Writable persistent storage, including capacity for uploaded files, database backups, and temporary verified downloads.

For a local build:

- Go **1.26 or newer**, as declared in `go.mod` (the Docker builder uses Go 1.26).
- Node.js **22** and npm are the Docker build baseline; use that version locally for consistency.
- A C compiler, `pkg-config`, and **SQLCipher 4.20.0** development headers/library. `CGO_ENABLED=1` is required; stock SQLite and `-tags libsqlite3` are not supported. The checked-in `third_party/go-sqlite3` linkage fork is selected by `go.mod` and must remain available. Set `PKG_CONFIG_PATH` to the directory containing your SQLCipher `sqlcipher.pc` if it is not in the default search path (for example `$(brew --prefix sqlcipher)/lib/pkgconfig` on macOS). Startup verifies the engine version and fails closed on a different engine.
- A supported browser. Large browser-verified downloads additionally require a secure context and File System Access API support.

For public registration, password reset, and email delivery, configure a working SMTP server. Public deployments and remote OAuth callbacks need a canonical HTTPS application URL and an HTTPS reverse proxy. Sendora's executable serves HTTP; it does not provision TLS certificates.

## Quick start: a new Docker instance

Create a key **outside the repository and data volume**. It must contain exactly **32 random raw bytes**, not hex/base64 text, a password, or a newline-terminated string. These commands refuse to overwrite an existing key:

```sh
mkdir -p "$HOME/.config/sendora"
chmod 700 "$HOME/.config/sendora"
(umask 077; set -C; openssl rand 32 > "$HOME/.config/sendora/database.key")
```

Back up this key separately to protected off-host storage before proceeding. **Losing it makes the encrypted database and new database backups unrecoverable.** Do not regenerate it on restart.

On **Linux Docker bind mounts**, provision storage and key ownership for the container UID/GID 10001:

```sh
mkdir -p sendora-data
sudo chown 10001:10001 sendora-data
sudo chmod 700 sendora-data
sudo chown root:10001 "$HOME/.config/sendora/database.key"
sudo chmod 0440 "$HOME/.config/sendora/database.key"
```

The read-only Compose secret uses this host file directly; Compose `uid`, `gid`, and `mode` settings cannot remap local-file secret ownership. A key owned by a different host UID is rejected even if it is readable. UID 10001 ownership with mode 0400 is another supported choice. Docker Desktop's file-sharing behavior differs: validate the effective owner/mode inside the container and arrange root or UID 10001 ownership, never relax permissions to world-readable.

For a **new** instance only, run from the project root while access is private:

```sh
SENDORA_DB_INITIALIZE=true docker compose up -d --build
docker compose logs -f sendora
```

After completing private first-run setup, recreate the service with initialization disabled (the default):

```sh
SENDORA_DB_INITIALIZE=false docker compose up -d --force-recreate
```

For subsequent starts, use `docker compose up -d`. Missing keys/databases fail closed. `SENDORA_DB_INITIALIZE=true` only permits creating a new encrypted database; it never converts, overwrites, or silently imports an existing plaintext database. Existing installations must follow the offline migration procedure below.

Open **http://localhost:8080** and complete the first-run setup to create the administrator account. Do this before allowing anyone else to access the service: setup is available while the database contains no users. Use a strong password (local account passwords must be 8–72 bytes).

The supplied Compose file:

- Builds the root Dockerfile.
- Publishes **127.0.0.1:8080 → container port 8080**, not a public interface.
- Mounts **`./sendora-data` → `/data`** for persistence.
- Uses `restart: unless-stopped` and `TZ: UTC`.
- Mounts the key as `/run/secrets/sendora_db_key` and defaults initialization to false. Override the host key path with `SENDORA_DB_KEY_HOST_FILE` when needed; the default is `${HOME}/.config/sendora/database.key`.

**Port and storage distinction:** the local default and Compose example use **8080**; a different port must be configured explicitly. The default local `./data` directory and Compose's `./sendora-data` are different stores. Starting Compose does not import existing local accounts or shares.

Stop the Compose instance without deleting its bind-mounted data:

```sh
docker compose down
```

If startup reports permission errors under `/data`, check the bind mount's ownership and permissions. The container runs as UID **10001** (`app`) and must be able to write it. Docker/Linux bind mounts may need explicit host-side provisioning for that UID; do not solve this by making the directory world-writable or by running the application as root.

For an existing local instance, read [Migration](#migration-between-local-and-docker-storage) before changing mounts or copying data.

## Local build and development

```sh
git clone https://github.com/mhenderik/sendora.git
cd sendora
```

The frontend must be built **before** compiling or running the root Go package, because `main.go` embeds `web/dist` at compile time. Configure the SQLCipher pkg-config search path as needed, then:

```sh
export CGO_ENABLED=1
npm --prefix web ci
npm --prefix web run build
go build -trimpath -o sendora .
go build -trimpath -o /tmp/sendora-db ./cmd/sendora-db
mkdir -p "$HOME/.config/sendora"
chmod 700 "$HOME/.config/sendora"
# Run keygen only once; it refuses to replace an existing file.
/tmp/sendora-db keygen -key-file "$HOME/.config/sendora/database.key"
export SENDORA_DB_KEY_FILE="$HOME/.config/sendora/database.key"
# New database only; keep first-run setup private.
SENDORA_DB_INITIALIZE=true ./sendora
```

After setup, stop and restart with `SENDORA_DB_INITIALIZE=false ./sendora`. For an existing encrypted installation, reuse its key and keep initialization false. Every `go run` example below requires the exported key path and SQLCipher/CGO configuration; none bypass the initialization or key checks.

Alternatively, after building the frontend:

```sh
go run .
```

By default this serves **http://127.0.0.1:8080** and uses `./data` relative to the process's working directory. Run from a consistent directory; changing it can select a new database and break relative file paths. For a new installation, an explicit absolute `DATA_DIR` avoids dependence on the working directory. Do not change an existing installation's path convention without a migration plan.

An example of a separate local port override is:

```sh
HOST=127.0.0.1 PORT=9090 go run .
```

This still uses the same default `./data`: **never run it at the same time as another process using that directory**. Different ports do not make shared storage safe.

For frontend hot reload, first build `web/dist` once, then run in two terminals:

```sh
# Terminal 1, project root; align origin checks with the Vite browser origin.
APP_URL=http://localhost:5173 go run .

# Terminal 2, project root.
npm --prefix web run dev -- --host 127.0.0.1 --port 5173 --strictPort
```

Browse **http://localhost:5173**. `web/vite.config.js` proxies `/api` to `http://localhost:8080`. If the backend uses 9090, the Vite proxy target must be changed explicitly; setting `PORT` alone does not update it. Match `APP_URL` to the actual browser origin, since unsafe cross-origin requests are rejected. Frontend production changes require another frontend build and recompilation of the embedded executable. Do not expose Vite or the development HTTP server directly to the internet.

## Runtime configuration

| Variable | Local default | Purpose |
| --- | --- | --- |
| `HOST` | `127.0.0.1` | HTTP bind address; Docker sets `0.0.0.0` inside the container. |
| `PORT` | `8080` | HTTP listening port; Docker also defaults to 8080. |
| `DATA_DIR` | `./data` | Database and storage root; Docker sets `/data`. |
| `SENDORA_DB_KEY_FILE` | required, no default | Path to the external regular file containing exactly 32 raw key bytes. Owner must be root or the current process UID; no group-write or world permissions. Group-read is allowed for projected `fsGroup` secrets. Never put the key value in an environment variable or command-line argument. |
| `SENDORA_DB_INITIALIZE` | `false` | Explicit `true` permits a new encrypted database only. Disable after private first startup; existing plaintext is never automatically migrated. |
| `APP_URL` | unset | Canonical application URL used for generated links, email links, OAuth callbacks, origin checks, and secure-cookie decisions. |
| `JWT_SECRET` | generated and stored in SQLite | Optional override for the session-signing secret. Keep a stable, high-entropy value if overriding it. |
| `FILESHARE_VERIFIED_DOWNLOAD_MAX_BYTES` | `17179869184` (16 GiB) | Positive integer byte limit for each temporary verified download snapshot; invalid/nonpositive values fall back to the default. |
| `SENDORA_TRUSTED_PROXIES` | unset | Comma-separated IPs/CIDRs of reverse proxies whose `X-Forwarded-For` / `X-Forwarded-Proto` headers are believed (for example `10.0.0.0/8,127.0.0.1`). Empty means the TCP peer is the client and forwarded headers are ignored. With proxies configured, the right-most forwarded address that is not itself a trusted proxy is the client address used for rate limits and download records, and `X-Forwarded-Proto: https` marks cookies `Secure` and enables HSTS. **Set this when running behind an ingress**, otherwise all visitors share one rate-limit budget. |
| `SENDORA_DISABLE_IP_BANS` | unset | Explicit `true` switches off the address blocking (see *Blocking repeated failed sign-ins*) regardless of the setting. Recovery switch if an administrator blocked their own address; remove it afterwards. |
| `SENDORA_FORCE_LOCAL_LOGIN` | unset | Explicit `true` re-enables local username/password sign-in at runtime even when an administrator turned it off, without changing the database. Recovery switch for a failing external provider; a warning is logged at startup. Remove it afterwards. |
| `TZ` | environment-dependent | Container timezone; Compose sets UTC. |

SMTP, registration, expiry/cleanup policy, share size limits, branding, themes, login providers, and email templates are configured through the administrator interface and persisted in SQLite, not through corresponding SMTP environment variables.

`APP_URL` takes precedence over the administrator `application_url` setting. For a remote deployment, set one canonical origin such as **`https://files.example.com`** before sending emails or configuring OAuth. Do not rely on request `Host` or forwarded headers to supply the public origin. Token-bearing email links and OAuth callbacks require a configured trusted URL for non-loopback access; HTTP localhost URLs remain allowed for development.

## Docker deployment behind HTTPS

The multi-stage Dockerfile builds the frontend with Node 22, compiles Go 1.26 with CGO, and builds **official SQLCipher 4.20.0 source** with OpenSSL on Debian Bookworm. The source URL is `https://github.com/sqlcipher/sqlcipher/archive/refs/tags/v4.20.0.tar.gz`, pinned to SHA-256 `38705f51b61ac0636a5fd064f9a77fafabc7227b2aa2513f1b14c8bb1102affc`. Build and runtime stages check `PRAGMA cipher_version`; the application also checks the engine when opening a database. The runtime is Debian Bookworm slim with SQLCipher/OpenSSL shared libraries, CA certificates, timezone data, and UID/GID 10001. It includes the `sendora-db` offline utility and SQLCipher CLI; the Go executable is **not static**. It declares `/data` as a volume and includes a health check for **`http://127.0.0.1:8080/api/health`** inside the container. This endpoint reports process health/version, not complete storage or SMTP readiness.

1. Provision storage that UID 10001 can write and mount the separately protected database key as described in Quick start.
2. Set `APP_URL: https://files.example.com` in the Compose environment (an example is commented in the file).
3. Build/start the service and finish administrator setup while access is restricted.
4. Configure an HTTPS reverse proxy to forward the public origin to the backend. For a proxy on the same host, the loopback mapping can remain unchanged. A proxy in another container cannot reach this service through its own `127.0.0.1`; arrange an appropriate private Docker network/upstream instead.
5. Configure SMTP and login providers, then test actual delivery, sign-in, file transfer, and restore procedures.

Keep the backend inaccessible except through the trusted proxy. Allow the application's methods, including upload `PATCH`, and accommodate 8 MiB browser chunks (the API permits up to 64 MiB per chunk), download preparation delays, and long transfers. Avoid proxy transformations/compression of download responses that would invalidate their exact size/checksum representation.

If changing the container's `PORT`, also adjust the published port, proxy upstream, and Docker health check: the health check is hard-coded to 8080. Changing only the host port mapping does not require changing the container port.

Run **exactly one Sendora process/container per data directory**. Multiple live instances, shared SQLite mounts, and replica scaling are not supported. Startup recovery and background jobs assume exclusive use, in addition to upload/integrity locks being process-local.

## Kubernetes deployment example

[`deploy/kubernetes.yaml`](deploy/kubernetes.yaml) is an **illustrative starting point**, not a tested production deployment or a claim that a published container image exists. It defines the `sendora` namespace, a 100 GiB `ReadWriteOnce` PVC, a single-replica Deployment with `Recreate` strategy, resource requests/limits, `/api/health` probes, and a private ClusterIP Service. The optional HTTPS Ingress is commented out until you configure it.

### Build and publish your own image

Build from the project root and push to a registry you control. Replace `your-org` and `your-version` with your own registry organization and immutable release tag:

```sh
IMAGE=ghcr.io/your-org/sendora:your-version
docker build -t "$IMAGE" .
docker push "$IMAGE"
```

Authenticate to your registry first, build for the cluster nodes' CPU architecture (or publish a multi-platform image), and configure `imagePullSecrets` if the registry is private. Replace **`ghcr.io/your-org/sendora:VERSION`** in the manifest with the image you actually pushed; that literal placeholder is not an available release. For reproducible deployments, pin the resulting image digest. Building/pushing a tag does not automatically set the application's displayed version; pass `--build-arg VERSION=your-version` to `docker build` to set it.

### Configure storage and private setup

1. Review PVC capacity and resource limits for your file sizes, concurrency, and temporary snapshots. The 100 GiB/CPU/memory values are examples, not capacity guarantees. Select a storage class suitable for SQLite filesystem locking and durability. The manifest omits `storageClassName` to use the cluster default; set it explicitly if needed, and confirm the PVC is Bound before troubleshooting startup.
2. Provision the volume root and any restored application files with **UID 10001 ownership**. The Pod runs without root as UID/GID 10001 with `fsGroup: 10001` and drops all capabilities. `fsGroup` can provide group access when the storage driver supports it, but **is not enough by itself**: Sendora changes the data root/subdirectories to 0700 and database files to 0600, which requires ownership. Root-owned PVC directories can therefore fail startup even if group-writable. Arrange ownership through the storage provisioner/administrator before starting; the example intentionally does not run a privileged/root permission-fixing init container. Do not use world-writable permissions.
3. Generate and separately back up a raw 32-byte key as above. Create the namespace and Secret separately; **do not put raw/base64 key material into the manifest or repository**:

   ```sh
   kubectl create namespace sendora
   kubectl -n sendora create secret generic sendora-db-key \
     --from-file=database.key="$HOME/.config/sendora/database.key"
   ```

   The key file must be readable by the operator running this command. The Secret projection is root-owned with mode **0440**; Pod `fsGroup: 10001` gives the non-root process read access without group-write or world access. The example mounts `/run/secrets/sendora/database.key` read-only and sets `SENDORA_DB_KEY_FILE` accordingly. Protect Kubernetes Secret access, enable encryption at rest for cluster secrets, and keep a separate off-cluster key backup; a Secret is not itself an off-host backup policy.

   For a **new PVC/database only**, temporarily set `SENDORA_DB_INITIALIZE` to `"true"` in the Deployment while access remains private, then apply the uncommented resources:

   ```sh
   kubectl apply -f deploy/kubernetes.yaml
   kubectl -n sendora get pvc,pods,services
   kubectl -n sendora logs deployment/sendora
   ```

   After private first-run setup, restore `SENDORA_DB_INITIALIZE` to `"false"` and reapply the manifest. Keep it false for upgrades, restored databases, and all later starts. The checked-in manifest defaults to false and will not create a missing database until you explicitly opt in. Existing plaintext databases require offline migration, not this initialization switch.

4. The example sets `HOST=0.0.0.0`, `PORT=8080`, and absolute `DATA_DIR=/data`. It sets the canonical `APP_URL` to **`https://sendora.example.com`**; replace this with your real HTTPS origin. This environment variable is supported and overrides the administrator `application_url` setting for links, callbacks, origin checks, and secure-cookie decisions.
5. Finish first-run setup using restricted HTTPS access at that canonical origin, before exposing the Ingress publicly. A plain HTTP port-forward alone does not match the example's HTTPS origin and secure cookies. If you temporarily use port-forwarding for bootstrap, temporarily configure `APP_URL=http://localhost:8080` and access it through `kubectl -n sendora port-forward service/sendora 8080:80`, then restore the real HTTPS `APP_URL` before external access, email, or OAuth configuration. Do not leave both configurations active or expose bootstrap access publicly.

### Optional HTTPS Ingress

Install/configure an Ingress controller, choose its `ingressClassName`, and point your hostname's DNS to its endpoint. Provision a TLS Secret named `sendora-tls` in the `sendora` namespace with a certificate valid for your real hostname; the example does not create certificates or assume a certificate-management integration. Uncomment the final Ingress block only after replacing its `nginx` class and **`sendora.example.com`** hostname as appropriate. Keep its hostname and TLS hosts aligned with `APP_URL`, and restrict access while completing setup.

The backend remains HTTP on port 8080 inside the cluster; terminate HTTPS at the trusted Ingress. Enforce HTTPS redirects through your controller's configuration and tune its controller-specific upload body limits/timeouts for the chunked API and long downloads as described in [Docker deployment behind HTTPS](#docker-deployment-behind-https). No controller-specific annotations are provided because these settings are not portable. Probes check HTTP process health only, not storage consistency, available capacity, SMTP delivery, or the end-to-end HTTPS route.

### Single instance, backups, and migration

**Do not increase replicas or run another Deployment/Job against this data volume while Sendora is running.** `Recreate` prevents overlap during ordinary Deployment updates; it is not high availability or protection against another workload using the same PVC. `ReadWriteOnce` restricts node mounting, not necessarily multiple Pods on one node. Do not manually force-delete a Pod or start its replacement while the original process might still be running. Updates and failures can cause downtime.

Keep full, protected backups outside the PVC, along with deployment configuration and external secrets. Built-in backups include SQLite only, not shared bytes or appearance assets. For a full offline backup/restore, scale the Deployment to zero and confirm the Pod/process has stopped before mounting/copying storage; restore with UID 10001 ownership and start exactly one instance afterward. Do not casually delete the PVC: data retention on claim deletion depends on the storage class/PV reclaim policy. Test a full restore before relying on the deployment.

Stored file locations are resolved against the server's own `DATA_DIR/shares/<share id>/` using only each recorded file's base name, so a database or share folders moved to the PVC at `/data` keep working as long as each share folder is copied over intact. Retain an untouched full backup and verify old downloads after the move; neither the manifest nor changing `DATA_DIR` copies any data.

## Persistence, backups, and restore

Accounts survive restarts when the same data store is retained. `DATA_DIR` contains:

| Path | Contents |
| --- | --- |
| `fileshare.db` | Accounts, bcrypt password hashes, settings, authentication/provider links and secrets, share/file metadata, tokens, download history, and mail queue state. |
| `fileshare.db-wal`, `fileshare.db-shm` | SQLite write-ahead-log/coordination files when present. |
| `shares/` | Published file bytes. |
| `uploads/` | Incomplete upload parts. |
| `branding/`, `backgrounds/` | Uploaded appearance assets. |
| `backups/` | Administrator/automatic **database-only** backups. |
| `.verified-download-*` | Private temporary download snapshots, removed after serving or during startup crash recovery. |

The store creates/restricts its main directories to mode 0700 and the database/managed files to 0600 where implemented. **SQLCipher encrypts the database, WAL page contents, and newly created database backups**, including database-held service secrets. It does **not** encrypt shared/uploaded file bytes, upload staging, branding/background assets, temporary download snapshots, old plaintext backups, or all filesystem/SHM coordination metadata. Use volume encryption and restricted access for the complete store. A copied encrypted database does not include the external key; protect and separately back up `SENDORA_DB_KEY_FILE` off-host. **Key loss is unrecoverable.** An external `JWT_SECRET` is also not included in a database backup and must be protected separately.

### Importing a database (Admin → Backups → Import database)

Administrators can replace the whole database with a Sendora backup from the web interface.

1. **Check**: choose an encrypted Sendora backup (`.db`). If it was made with a different key, enter that key (64 hex characters) or upload its 32-byte key file; the key is only used for this check, is never stored or logged, and the data is re-encrypted with this server's key. Unencrypted SQLite files are refused. The file is fully verified (integrity checks, migrations, schema compatibility; a database from a newer Sendora version, one without any administrator, one that is not a Sendora database, one with virtual tables, or one with a share ID of unknown format is refused) without touching the live database. An imported file is treated as untrusted input: triggers, views and extra indexes in it are dropped and only this version's own are recreated, sessions, reset/verification codes and the signing secret are cleared (so everyone must sign in again), and every file location is rewritten to this server's `DATA_DIR/shares/<share id>/<file name>`. The review shows users, administrators, shares and files, plus **how many files the imported shares reference that are missing on this server**.
2. **Import**: type `IMPORT` and give your password. Sendora switches to maintenance mode, writes a **pre-import backup** of the current database (`sendora-preimport-…`, listed under Backups and never pruned automatically), swaps the database, clears upload staging, and **restarts itself** (the process re-executes in place; in Docker the container keeps running).

What an import does **not** do: it does not import or move uploaded files (database backups do not contain them), and it keeps no accounts from the current database, so everyone, including you, must sign in again with accounts from the import. **File locations are rebased automatically**: after an import, copy each share's folder into `DATA_DIR/shares/<share id>/` on this server and downloads work; the review shows how many files are still missing. Providers and settings come over as they were in the file, so review them if the backup is not your own.

If the new database fails to open after the swap, the previous database is restored automatically at startup. To undo a successful import, import the pre-import backup the same way (using this server's key). Keep your encryption keys safe: a backup is unreadable without the key it was made with.

### Database backups are not full file backups

Admin → Backups can create, download, and delete database backups. New backups use keyed `sqlcipher_export` to produce a consistent encrypted snapshot with the same external key as the active database, followed by schema/content equivalence and integrity verification. New names are `sendora-encrypted-TIMESTAMP-RANDOM.db`. Legacy `fileshare*.db` backups remain recognized but are excluded from automatic retention; only the new encrypted-backup namespace is automatically pruned. Legacy backups are not automatically encrypted or removed during migration. Treat old plaintext backups as sensitive and protect/retire them deliberately only after verified recovery copies exist. Automatic database backups are enabled by default: the maintenance loop checks every ten minutes, makes its first scheduled backup on a tick after startup, then approximately every 24 hours within that process's lifetime. Default automatic retention is seven backups. This is not an external backup service or a guaranteed fixed-time daily schedule.

For an offline, verified encrypted database snapshot, stop the application and use the same external key with a new destination path:

```sh
sendora-db backup \
  -source /srv/sendora/data/fileshare.db \
  -destination /srv/sendora-recovery/pre-upgrade.db \
  -key-file /run/secrets/sendora/database.key
sendora-db verify \
  -source /srv/sendora-recovery/pre-upgrade.db \
  -key-file /run/secrets/sendora/database.key
```

The destination must not exist; export preserves the source and refuses overwrites. Protect the recovery directory and keep the external key separately.

**Database backups do not include shared files, upload parts, branding, or backgrounds.** A database restored without matching file storage can restore logins yet leave old shares undownloadable. There is no built-in restore/import endpoint.

For a full recovery point:

1. Stop all processes using the source data directory and confirm uploads/downloads/jobs are no longer running.
2. Take an offline backup of the **entire data directory**, preserving permissions and all SQLite files present. Do not copy only `fileshare.db` from a live WAL-mode database; committed changes may still be in its WAL.
3. Save deployment configuration and any externally configured secrets separately.
4. Keep a protected copy outside the application's own volume and test restoration on an isolated copy.

Restore with the application stopped, using matching database and file data, appropriate service-user ownership, and the original path layout (or an explicitly prepared migration). Start only the restored instance and verify existing accounts, old individual-file and ZIP downloads, branding, and relevant settings. Application startup performs schema migrations and maintenance, so keep an untouched pre-upgrade backup before testing a new version.

Default cleanup is also destructive: stale upload sessions older than 24 hours are removed; long-expired share files are automatically cleaned according to `clean_files_days` (default seven); download history older than 90 days is pruned. Review these settings before relying on the volume as an archive.

## Offline plaintext-to-SQLCipher migration

Startup rejects existing plaintext, wrong/missing keys, and unreadable databases **without automatically converting or replacing them**. `SENDORA_DB_INITIALIZE=true` is not a migration option. Build `sendora-db` using the same SQLCipher/CGO environment as the server; the Docker image also includes `/usr/local/bin/sendora-db` for an explicitly offline, isolated utility container. Do not run another process against live application storage.

1. Stop the source service, disable automatic restarts, and confirm all processes using its database have exited. Keep an untouched full offline backup of the entire data directory, including any WAL/SHM files, shared bytes, and deployment secrets. Work on an offline copy, not the only original.
2. Create and separately back up the new external key if needed; export only its **path** as `SENDORA_DB_KEY_FILE`. Preserve the source database and choose output paths that do not exist.
3. Create a verified standalone plaintext rollback snapshot, then a separate encrypted export. Example paths below refer to your isolated offline work directory:

   ```sh
   export SENDORA_DB_KEY_FILE="$HOME/.config/sendora/database.key"
   sendora-db snapshot-plaintext \
     -source /srv/sendora-offline/fileshare.db \
     -destination /srv/sendora-offline/rollback-plaintext.db
   sendora-db migrate \
     -source /srv/sendora-offline/rollback-plaintext.db \
     -destination /srv/sendora-offline/encrypted-export.db
   sendora-db verify -plaintext -source /srv/sendora-offline/rollback-plaintext.db
   sendora-db verify -source /srv/sendora-offline/encrypted-export.db
   ```

   The utility uses SQLCipher's keyed export, verifies source/export schema and content equivalence, checks database/cipher integrity, and refuses to overwrite output files. It is **export-only**: it does not rebase stored file paths, replace the source, swap database files, or restart the application. The plaintext rollback snapshot remains sensitive. Protect it and the original full backup.
4. Plan and perform the cutover manually, still offline. Use the verified encrypted export as the destination `fileshare.db` with matching file storage and service-user ownership. Never pair an exported database with old WAL/SHM sidecars. Preserve the original complete database/sidecars for rollback rather than overwriting your only copy. Resolve recorded file-path mapping as described below before changing mount layouts.
5. Start exactly one destination instance using the original new key and `SENDORA_DB_INITIALIZE=false`. Test accounts, metadata, old individual-file/ZIP downloads, settings, and encrypted backup recovery before retiring the source. Keep the protected plaintext rollback/full backup and separate key backup until your retention policy deliberately retires them.

## Migration between local and Docker storage

**Copying the existing local data directory into `./sendora-data` and mounting it at `/data` is not, by itself, a safe migration.** `files.disk_path` stores the path written when a share was completed, not a portable path automatically rebased against the new `DATA_DIR`. Existing local data may contain absolute paths such as `/srv/sendora/data/shares/...`; those paths do not become `/data/shares/...` when the database is copied. Relative paths likewise depend on the original working directory. The server reads recorded file paths directly.

Moving just the database can preserve accounts while breaking file downloads. The same problem applies when relocating local storage or restoring a backup under a different path. There is currently no automatic path-rewriting migration utility or supported one-command local-to-Docker import.

A path-safe migration requires explicit work:

1. **Stop the source instance** and prevent it from restarting. Never run source and destination against a shared directory/database, even temporarily.
2. Take and retain a full offline backup before any manipulation. Work on a copy, not the only original database.
3. Inspect the recorded `files.disk_path` values and establish the exact old-to-new mapping. Preserve the entire directory tree, not only the `.db` file. Finish/abort pending uploads if possible before migration.
4. Either make the old recorded paths resolve inside the destination with a deliberately preserved mount/working-directory layout, **or** explicitly migrate the copied database's file paths to the corresponding destination paths. A rewrite must be carefully scoped to the actual old root, preserve each file mapping, and verify that every referenced file exists. Do not blindly replace arbitrary strings or assume every row shares the same root.
5. Set the destination's `DATA_DIR`, ownership, public URL, proxy configuration, and external secrets appropriately. Start one destination instance only after the storage/path work is complete.
6. Test existing account login and old single-file and ZIP downloads before retiring the source. Retain the untouched backup and a rollback plan.

A path-preserving mount is an advanced deployment choice, not what the supplied `/data` Compose mount provides. If you cannot establish a trustworthy mapping, do not proceed with an improvised database edit; plan and validate a dedicated migration first.

## Public registration and SMTP

Admin → Settings controls **Allow self registration** (enabled by default) and SMTP host, port, username, password, From address, and security mode. Defaults are port 587 and STARTTLS. `tls` means implicit TLS; `starttls` requires a successful TLS upgrade. Use the mode and port your SMTP provider specifies. `none` is available but should be restricted to an intentionally trusted mail transport, not an untrusted network. A blank SMTP password when saving retains the existing secret.

Usernames are **not case-sensitive**: `Admin`, `admin` and `ADMIN` are the same account (signing in and uniqueness), and the capitalisation typed at creation is kept for display. This covers ASCII letters (A–Z); other alphabets are still compared exactly. If an older database already contains names that differ only by letter case, a warning is logged at startup and the unique index is skipped until you rename them; sign-in then prefers the exact-case match.

Outgoing mail uses the **From address** as sender (the SMTP username is only used to sign in, so the two may differ; Gmail only accepts a From address that is a verified *Send mail as* alias of the account and otherwise replaces it with the account address). An optional **Reply-To address** (Admin → Settings, one address with an optional name) is added to every message so replies go there instead of to the From address.

Public signup **always requires email verification**, regardless of the legacy `require_email_verification` setting. It requires a username, valid email, and 8–72-byte password. If SMTP settings are missing/invalid, signup returns HTTP 503; merely passing the configuration check does not prove the relay is reachable or delivering mail. New registrants must enter the emailed six-digit code before login. Codes expire after 30 minutes; verification attempts and resends are rate-limited.

Configure a canonical HTTPS `APP_URL` and send a real administrator test email before advertising signup. Confirm delivery through your relay/spam filters and configure the sender domain's SPF/DKIM/DMARC as appropriate. Password reset links expire after one hour and require a trusted application URL. Local first-run setup and administrator-created accounts do not depend on public signup SMTP.

Public share links permit unauthenticated downloads, subject to the share's password/expiry/download limit; they are not unrestricted anonymous upload access. Guest uploads require a reverse-share invitation. Review registration and invitation policies before making the service public.

New share links use a readable, cryptographically random identifier: five English words with two seven-digit number blocks, for example `/share/tiger-bike-chest-3485217-river-2765290-cloud`. Independent choices provide at least 86 bits of randomness; words do not come from the filename, username, or share title. Existing hexadecimal share links and stored file paths remain valid and unchanged. Public links remain bearer links: anyone who receives an unprotected link can access its files. Use share passwords and expiry when appropriate.

### Share passwords, expiry, open upload links, previews and QR codes

- **Share password policy** (Settings): optionally require a password on every share, set a minimum length (1-72) and require upper-case, lower-case, digit and/or symbol characters. It is enforced by the server when a share is created; existing shares keep their passwords. Reverse-share guest uploads are exempt. The 72-byte bcrypt limit always applies.
- **Change expiry later**: owners (and administrators) can set a new absolute expiry date or "never expires" for existing shares, shortening or extending it. Non-administrators remain bound by the maximum expiry setting. Files already removed by cleanup are not restored.
- **Open upload links**: besides per-email invitations, a reverse-share link can be an open link for several uploaders, limited by a maximum number of uploads and an expiry date. Each visitor gets an isolated single-use guest session; uploads count against the owner's storage quota and become separate shares owned by the inviter. Owners can see usage and revoke links. Anyone holding an open link can upload until its limit or expiry, so share it deliberately.
- **Preview**: images (PNG, JPEG, WebP, GIF), video (MP4, WebM) and audio (MP3, OGG, WAV) can be viewed in the browser. Type is chosen by extension and verified by content; SVG, HTML and PDF are never served inline. Responses use a sandboxing Content-Security-Policy. Previews are not checksum-verified, do not count as downloads, and are unavailable for shares with a download limit or when disabled in Settings.
- **QR codes** for share and open-link URLs are generated in the browser by a built-in encoder; no external service or library is used.

### Editing shares, activity log, external-only login and gallery view

- **Edit a share** (My shares → Edit): name, description, download limit, and the share password (set one on an unprotected share, change it, or remove it unless a password is required by the policy). Changing or removing the password also invalidates links already unlocked in other browsers.
- **Add and remove files** in an existing share (owner only for adding; owner or administrator for removing). Added files use the normal resumable upload and checksum flow, count toward the owner's quota and the maximum share size (including existing files), and a name that already exists in the share is renamed automatically (`photo.jpg` becomes `photo (2).jpg`). The last file of a share cannot be removed; delete the share instead. Removing a file frees its storage; download-history rows for it remain but may show no file name. Downloads already in progress keep their own snapshot.
- **Registration domains** (Settings): optionally restrict public registration to listed e-mail domains (one per line, exact domains only, subdomains not included). Accounts created by administrators and external login providers are not affected.
- **Page titles**: the browser tab shows upload progress during an upload and the share name on a share page.
- **Activity log** (Settings → Logging, Admin → Logs): off by default. When enabled it records logins (success, failure, lockout), share lifecycle events, downloads and previews, user/invite/settings/backup actions for administrators only. Entries never contain IP addresses, passwords, tokens, share link IDs or typed usernames, and settings changes record only key names. Entries are deleted automatically after the retention period (1–24 hours, never longer), capped at 50,000 rows, and all entries are removed when logging is turned off. Entries are written asynchronously, so a crash can lose the latest few. Administrators can download the visible entries (optionally filtered) as CSV from Admin → Logs; cells that could be read as spreadsheet formulas are neutralised and the export itself is logged.
- **External login only** (Settings → Local login): turning local login off disables username/password sign-in, registration and password reset. External providers, existing sessions, first-run setup, invited guest uploads and share passwords are unaffected. It can only be turned off when an enabled provider exists and **your own account is linked to it**, and while it is off Sendora refuses actions that would remove the last administrator able to sign in externally. **Risk:** if the external provider later stops working you cannot sign in. Recovery: restart the server with `SENDORA_FORCE_LOCAL_LOGIN=true` (a warning is logged at startup), sign in locally, fix the provider or re-enable local login, then remove the variable. The variable does not change the database. Keep at least two externally linked administrators.
- **Gallery view**: the public share page can switch between a list and a grid. Image thumbnails use the preview endpoint (full-size images, loaded lazily), so they are unavailable when previews are disabled or the share has a download limit.

### Multi-factor authentication (TOTP, passkeys and recovery codes)

Administrators can add a second sign-in step for **local accounts** (Settings → Multi-factor authentication, per-user override in Users). Sendora uses the open TOTP standard (RFC 6238: HMAC-SHA-1, 6 digits, 30 seconds, ±1 step of clock drift), so any authenticator app works (Google/Microsoft Authenticator, Authy, 1Password, Bitwarden, Aegis, Apple Passwords); no external service is involved. The shared secret and the hashed recovery codes are stored in the encrypted database.

**Global policy** (`mfa_policy`): `off` (default; MFA is completely disabled: no second step, no enrolment, per-user settings ignored; this is also the emergency switch if everybody is locked out) · `optional` (users enrol themselves in Profile → Security) · `admins` (required for administrators) · `all` (required for every local account). `mfa_totp_enabled` and `mfa_webauthn_enabled` switch the authenticator-app and the passkey/security-key method on or off. A corrupt stored policy fails closed to `all`.

**Per user** (`inherit` / `required` / `exempt`): *required* forces MFA for that account even when the global policy is `optional`; *exempt* is never forced to enrol (for example a service account) — an exempt user who did enrol still uses the factor. Setting someone to exempt is logged. If no method is enabled, nobody can be required to enrol.

| Global | inherit | required | exempt |
| --- | --- | --- | --- |
| off | no MFA | no MFA | no MFA |
| optional | optional | required | optional |
| admins | admins required | required | not forced |
| all | required | required | not forced |

**Sign-in.** After the password (and, for new addresses, the e-mail code) Sendora answers `mfa_required` with a short-lived (5 minutes), single-use token instead of a session; no refresh token exists before the second step is completed. Wrong codes count towards the same lock as wrong passwords (5 failures lock the account for 15 minutes, only a successful second step resets the counter). A code can be used once (replay protection). Enrolment confirms the setup with a code, shows **10 one-time recovery codes once** (store them safely; they are only kept as hashes), and re-asks the account password for sensitive actions (setup, removal, new recovery codes). Every factor change signs out the account's other sessions.

**Required but not enrolled.** The user can sign in but only reaches their profile and the enrolment screens until a factor is added. This starts at the next login or token refresh (access tokens live 15 minutes), not in the middle of a session. A required user cannot remove their last factor. An administrator cannot enforce `admins` or `all` before enrolling their own account (or exempting themselves), to avoid locking themselves out; the order is therefore off → optional → enrol → admins/all.

**Not covered.** Guests and accounts that only sign in through an external provider (Entra, OIDC, Google, GitHub) are outside Sendora's MFA: use the provider's MFA and Conditional Access. SMS and e-mail codes are deliberately not offered. TOTP does not protect against phishing sites that relay codes in real time; passkeys (WebAuthn) are the stronger method.

**Passkeys and security keys (WebAuthn).** Users can register up to 10 passkeys or hardware keys (Touch ID, Windows Hello, a phone passkey, YubiKey and other FIDO2 keys) in Profile → Security, next to or instead of an authenticator app; any registered factor satisfies the second step, and recovery codes work for both. Requirements: the application URL must be a **secure origin** — `https://` with a domain name (set `APP_URL`), or `http://localhost` for development; browsers refuse IP addresses, so with anything else passkeys stay switched off (the bootstrap flag `webauthn_available` and the Profile page say so). The relying-party ID is the host of `APP_URL`: changing the domain later makes existing passkeys unusable, so keep a recovery path. Sendora asks for user verification but does not require it, uses attestation `none` and **does not evaluate attestation trust** (any authenticator model is accepted), supports ES256, EdDSA and RS256 keys, and rejects a signature counter that does not increase (a possibly cloned key). Registration and sign-in challenges are random, single use, expire after 5 minutes and are kept in memory only (bounded), so a restart simply requires starting the step again. Failed passkey attempts count towards the same lock as failed passwords. The implementation is a small, dependency-free verifier (`internal/webauthnlite`) tested against a software authenticator and the standard-library signature checks; it has **not** been verified against real hardware or browsers in this project, so try a real passkey or security key (and keep a recovery code) before you make MFA mandatory.

**Recovery.** A lost authenticator: use a recovery code. Locked-out users: an administrator resets their MFA (Users → reset MFA; all factors and sessions are removed). A locked-out administrator with no other admin: stop Sendora and run `sendora-db mfa-reset -source /path/to/fileshare.db -key-file /path/to/database.key -user <name>`, or set `mfa_policy` to `off` in the database. Audit events (when logging is on): `mfa_enrolled`, `mfa_removed`, `mfa_login_ok`, `mfa_failed`, `mfa_reset`, `mfa_policy_changed` — never codes, secrets or credential IDs. MFA endpoints have their own rate-limit bucket (30 requests per minute per client).

### Automatic sign-out after inactivity

Users are signed out after a period without activity. Default: 60 minutes. Admins change it under **Admin → Settings → Automatic sign-out** (`idle_logout_minutes`, whole minutes 0–10080, `0` = never).

- The browser tracks mouse, keyboard, touch and scroll activity, shared between tabs. One minute before the limit a dialog warns and offers **Stay signed in**. After the limit the user lands on the login page with a notice and returns to the same page after logging in.
- A running upload or download is never inactivity and is never interrupted. While transfers run, the session is silently refreshed. Downloads handed to the browser's own download manager cannot be observed; Sendora assumes they stay busy for `max(60 s, size / 2 MB/s)`, capped at 6 hours.
- Server side, a refresh token older than the limit plus a 20 minute grace period is rejected and deleted, so a stolen browser profile cannot stay signed in forever. This is an approximation based on refresh-token age, not an exact idle timer. Guest sessions are exempt.

### Account password policy

Administrators can set a policy for **local account passwords** (Settings → Account password policy): minimum length (1–72, default 8) and optional upper-case, lower-case, digit and symbol requirements. It is separate from the share password policy and is enforced by the server wherever a local password is set: first-run setup, public registration, password reset, profile changes (including a provider-created account adding a first local password), and administrator-created or edited users. Passwords are always limited to 72 bytes (bcrypt) and must be valid UTF-8. Existing accounts are not forced to change: their current password keeps working until it is next changed, and sign-in itself never checks the policy. The forms show the requirements live; if the stored policy is corrupt the server refuses password changes instead of accepting anything.

### Local password lockout and user storage quotas

Five consecutive failed local-password login attempts lock that account's local password login for **15 minutes**. Administrators can unlock it through user management. This is distinct from network-peer rate limiting: **OIDC/provider login and existing authenticated sessions are unaffected by the local-password lockout**. Password reset and account-management procedures should be tested before public operation.

Administrators configure **Default storage quota (GB)** in Settings. Users follow that setting unless an administrator selects a **personal override** in user management; changing the default immediately affects all users following it, including existing accounts. Profile and user-management storage values use decimal GB: **1 GB = 1,000,000,000 bytes**. Editors preserve byte precision (up to nine decimal places) and accept a decimal point or comma. New installations, including a fresh container volume, start with a global default of **10 GB**. An upgraded database keeps its previous 10-GiB allowance (**10.73741824 GB**) until an administrator changes the default; migration does not silently round or lower limits. Existing limits differing from the old default remain overrides. An old explicitly assigned limit equal to that default cannot be distinguished and becomes inherited; it can be made explicit again in user management. Setting zero means zero storage, not unlimited.

Usage includes stored share-file bytes **plus reserved upload-session bytes**, so concurrent/incomplete uploads cannot bypass the limit. Reverse-share guest uploads consume the **invitation owner's** quota, not a separate guest allowance. Lowering a limit does not delete existing files and does not invalidate already admitted uploads; it blocks new admissions that would exceed the effective limit. This storage quota does not bound bandwidth, encrypted backups, branding, or verified-download snapshots. Monitor actual filesystem capacity independently.

### Welcome email for new users

When an administrator creates a user (Admin → Users → New user) with an email address, the form offers **Send welcome email** (on by default when outgoing email works). The email contains the **username**, the **sign-in link** and a **one-time link to choose their own password**, valid for **7 days**; it never contains the password the administrator typed, which keeps working until the user picks their own. The user is always created, even when the email cannot be sent; the screen then says why (no SMTP configured, no email address, public URL not set, hourly sending limit reached, or delivery failed). **Resend welcome email** in the user's edit form creates a fresh link and invalidates earlier unused welcome links. Using the link verifies the email address (the mailbox owner received it), clears "must change password", signs out other sessions of that user and applies the account password policy. Links are built only from the configured public URL (`APP_URL` / `application_url`) or a loopback host, never from the request's `Host` header, so with a public deployment set `APP_URL` first. The mail uses the new user's email language and counts against the creating administrator's hourly recipient limit. The text is the editable template event **Welcome** (variables `ApplicationName`, `RecipientName`, `Username`, `LoginURL`, `SetPasswordURL`, `ExpiresDays`). Links and message contents are never logged; the audit log only records `welcome=sent|skipped|failed`.

### Email templates and languages

The built-in email languages are **English (`en`, default), Dutch (`nl`), German (`de`), French (`fr`), and Spanish (`es`)**. Administrators can add extra email languages and manually translate all eight mail events under **Admin → Email templates**. This is a shared administrator-managed configuration, not personal user templates or automatic machine translation. Every one of the eight events (share notice, download notice, expiry warning, password reset, email verification, upload invitation, upload completed, welcome) ships with a translated subject and body for all five built-in languages; an automated test checks that no event or language is missing, that translations use the same variables as the English text, and that upgrades only add missing rows and never overwrite templates an administrator has changed. The administrator's **test email** is not a template: it is sent in the language of the signed-in account.

1. Enter a **Language code** (for example `it`, `pt-br`, or `zh-hant`) and human-readable **Language name**, then click **Add email language**.
2. Select the new target language and a mail event: file delivery, download starts, expiry warnings, password reset, verification, upload invitations, completed invited uploads, or the welcome email for new users.
3. Select a source language. Its subject/body appear read-only beside the editable translation. **Copy source to translation** requires confirmation and changes the current event only; translate the text yourself.
4. Preserve placeholders exactly, including spelling and braces, such as `{{.Code}}`, `{{.URL}}`, and `{{.ShareName}}`. Translate surrounding text, not variable names. Keep verification codes/reset links in the appropriate events so recipients can complete their actions.
5. Use **Save**, **Save and preview**, or **Save and send test to my email**. Preview/test saves the current translation first; test mail goes only to the signed-in administrator. Repeat for all eight events.

New languages start with blank subject/body slots. Blank or unavailable parts fall back independently to English; they are not automatically translated. A language becomes selectable immediately, so recipients may receive English until its translations are completed. Languages and edits persist in SQLite and database backups. There is no language-delete/rename UI. At most 32 total email languages are allowed. Codes are lower-case, 2–3 initial letters plus up to two hyphen-separated 2–8-character alphanumeric subtags (maximum 24 bytes); names are nonempty UTF-8, maximum 80 bytes, without control characters.

Subjects are limited to 500 bytes without line breaks; bodies to 64 KiB; rendered templates to 128 KiB. Only plain text and listed simple `{{.Variable}}` placeholders are supported, not HTML mail, loops, functions, conditions, or template definitions. Invalid edits are rejected before any requested template updates are committed.

API: public `GET /api/auth/email-languages` lists `{code,name,built_in}` entries. Admin-only `POST /api/email-languages` accepts `{ "code": "it", "name": "Italiano" }`. Existing admin template endpoints remain `GET/PUT /api/email-templates` and `POST /api/email-templates/preview`; canonical keys are `subj_<event>_<code>` and `tpl_<event>_<code>`. Unknown language keys are rejected.

#### Download, translate offline, and import JSON

Select an existing language and click **Download translation JSON**. The file contains all eight **saved** subject/body pairs; unsaved editor changes are not exported. Send this file to a translator if desired (review its content before sharing), edit it in a UTF-8 text editor, then use **Upload translation JSON**. No automatic external translation service is contacted.

The top-level format is:

```json
{
  "format": "sendora-email-templates",
  "version": 1,
  "language": "it",
  "name": "Italiano",
  "templates": {
    "verify_email": {
      "subject": "Verifica il tuo indirizzo email",
      "body": "Il tuo codice è {{.Code}}"
    }
  }
}
```

New exports use the `sendora-email-templates` format identifier; older exports are still accepted on import.

This abbreviated example shows one event only: an import **must include all event keys** from the exported file (files exported by an older version without the `welcome` event still import; that event then keeps its current text). Keep `format`, `version`, event keys, `subject`/`body` field names and placeholders unchanged. To create another language, change `language` and `name`; translate every subject/body. Preserve valid JSON escaping (for example `\n` inside body strings). Blank strings retain English fallback behavior.

Uploading validates without changing the database and displays the language plus all texts in the file (seven, or eight when it includes the welcome mail; a file exported before the welcome mail existed keeps the current welcome text). **Confirm import** asks for confirmation, then atomically creates the new language or replaces those templates for the existing one. Existing language display names are retained (import is not a rename mechanism). Other languages are untouched. The UI accepts files up to 768 KiB; API requests are capped at 1 MiB. Invalid format/version, unknown fields/events, missing events, unsafe templates, header line breaks and invalid language codes are rejected.

Admin APIs: `GET /api/email-templates/export?language=en` returns downloadable JSON. `POST /api/email-templates/import` accepts `{ "bundle": <exported-object>, "apply": false }` for validation/preview. Applying uses `apply: true`; overwriting an existing language additionally requires `overwrite: true`.

Profile email-language preferences are separate from menu-language preferences. A uniquely matching ordinary account recipient uses its email-language preference; external recipients use the language selected by the sender (English if none is supplied). Missing translations fall back to English. Menu translations remain the five bundled UI languages; manually translated email content is not a menu translation mechanism.

### Download notification semantics

History is accessible only to the share owner or an administrator. Each served download request, **including Range/resume requests**, is counted. Notifications record the **start of serving**, not successful transfer completion or proof that a recipient saved, opened, or read the file. Retries can therefore consume multiple download-limit units.

Download notifications use a persistent queue with a maximum of **1,000 pending/reserved jobs**. If a new notification cannot be reserved because the queue is full, the download is rejected with HTTP 503 and `Retry-After` before serving. SMTP delivery is attempted at most three times, with retry delays of 30 and 60 seconds; after that the job is marked failed. Only the newest 1,000 failed records are retained. Delivery is at-least-once: a crash after SMTP accepts mail but before acknowledgment can cause duplicate messages. Successful delivery is not guaranteed. Other template mail is not promised the same persistent retry behavior. Monitor logs and relay delivery, not just HTTP health.

## Translating the interface

Example language files (Japanese UI and email) are in [`custom-translations/`](custom-translations/README.md).

Buttons, labels and messages are translated through JSON dictionaries. The English text is the key; built-in languages are English, Dutch, German, French and Spanish. Administrators can translate or correct texts at runtime, without rebuilding, under **Admin → Languages**:

1. **Download JSON** for a language (or **Download template** for a new code such as `pt-br`). The file lists every translatable text with its current text: `{"code": "pt-br", "name": "Português (Brasil)", "translations": {"Save": "Salvar", ...}}`.
2. Translate the values offline. Keep the keys and the placeholders (`{n}`, `{name}`, `%s`, `%d`, `%q`) unchanged; they may move within a sentence but must all be present.
3. **Import JSON**. The file is checked in the browser and again on the server, and replaces the stored dictionary of that language atomically.

Rules and limits: at most 32 custom languages (overrides of built-in languages do not count), a language name of 1 to 80 bytes without control characters, at most 2000 bytes per text, at most 3000 texts, and a file of at most 1 MiB. Only known texts are accepted (interface texts and server messages); unknown keys, mismatched placeholders and control characters are rejected and nothing is stored. Texts are always rendered as plain text.

Lookup order is: stored text, built-in text, English. Importing a built-in code (for example `nl`) stores per-key overrides on top of the bundled translation; **Reset to default** removes them. Deleting a custom language switches its users back to English. New languages appear in every language menu, including the sign-in page. Imports and deletions are recorded in the event log (language code and number of texts only).

API (administrators): `GET /api/ui-languages`, `GET /api/ui-languages/{code}/export` (stored texts), `POST /api/ui-languages`, `DELETE /api/ui-languages/{code}`. Public, short-lived and ETag-validated: `GET /api/i18n/languages` and `GET /api/i18n/{code}`. Built-in texts live in the frontend bundle, so the complete download is assembled in the browser from the bundled text plus the stored overrides. The list of known texts is generated at build time (`i18n-keys/` inside the web build); after adding a `t('…')` string to the sources run `node web/scripts/i18n-keys.mjs`.

## Microsoft Entra ID: single-tenant setup

1. Set the canonical HTTPS `APP_URL` first.
2. In Microsoft Entra, register a **Web application** with **Accounts in this organizational directory only** (single tenant).
3. Create a client secret and retain its **value**, not merely its secret ID. Note the Directory (tenant) GUID and Application (client) ID.
4. In Sendora Admin → Login providers → Add provider, choose **Microsoft Entra ID**. Enter a display name, tenant GUID, client ID, and secret. Set **Enabled** and choose whether **Create accounts on first login** should be allowed, then save.
5. Copy the exact displayed callback URL into the Entra app registration's **Web redirect URIs**. Its shape is `https://files.example.com/api/auth/oidc/<provider-uuid>/callback`; the UUID belongs to the saved provider and is not the client or tenant ID.
6. Apply application assignment, MFA, and Conditional Access policies in Entra as required. Test actual sign-in and failure cases with your real tenant before public launch.

The integration uses authorization-code flow with **PKCE S256**, state, and nonce, not implicit flow. It requests `openid profile email`; Microsoft Graph permissions are not needed for this login flow. Tokens are checked for signature, issuer, audience, expiration, nonce, v2.0 claims, tenant, and immutable object identity.

### Entra limitations and account behavior

- Only the configured organizational tenant is accepted, including guest users if Entra allows them. `common`, `organizations`, `consumers`, personal-account tenant authorities, arbitrary hosts, and HTTP Entra issuers are rejected. The supported authority is `https://login.microsoftonline.com/<tenant-guid>/v2.0`; sovereign-cloud authorities are not implemented.
- Identity links use **tenant GUID + object GUID**, not mutable email or UPN. Entra email claims are not treated as verified and are not copied into the account email. Users can supply/verify a local address afterward.
- First-login provisioning creates an ordinary user, never an administrator. Its provider-specific registration switch is separate from public local signup. Disabling provisioning allows only already-linked identities to log in.
- Existing local accounts are **never automatically linked by matching email**, and an authenticated add/link-provider workflow is not yet implemented. Local password login remains available for local accounts; a newly provisioned provider-only account initially has no local password.
- Once users are linked, provider type, issuer/tenant, and client ID cannot be changed. Create another provider instead; secret rotation and disabling are supported. Deleting/recreating a provider is not a transparent identity migration.
- Sendora's own MFA does not apply to Entra sign-in (the tenant's MFA and Conditional Access are authoritative), and Sendora does not itself enforce Conditional Access or Entra app assignment. Logout ends the Sendora session, not Microsoft's global SSO session. Tenant-side revocation does not immediately revoke an already-issued Sendora session.
- Automated tests use a simulated OIDC provider. They do not replace validation of your real registration, redirect URI, tenant policy, or secret lifecycle.

Google, GitHub, and generic OIDC providers are also configured in Admin → Login providers, with their exact saved callback URL registered at the provider. Generic OIDC configuration can accept HTTP issuers; use HTTPS for real deployments and treat provider configuration as privileged/trusted. Matching verified email may populate a **new** non-Entra account when explicitly enabled, but it does not automatically link an existing account.

## Upload and download integrity

### Uploads

Folder selection and folder drag-and-drop automatically skip dot-prefixed files and files under dot-prefixed ancestor folders, filenames beginning with `~` (regardless of extension), `*.tmp` and `*.temp` files (case-insensitive), and `Thumbs.db` and `desktop.ini` (case-insensitive). Explicitly selected or dropped standalone files remain allowed, even when their names match these filters. Browser file APIs do not expose OS hidden attributes, so this is name/path-based filtering rather than detection of every hidden file. `.bak` files and generic `.lock` files are not excluded unless another listed rule matches their name or path.

The browser hashes each file incrementally with SHA-256 before upload and supplies the expected checksum. It sends 8 MiB chunks, retries network interruptions, and resynchronizes offsets with the custom API. The backend permits at most 5,000 files per upload and 64 MiB per chunk. The default maximum share size is **2,048 MiB** (the UI labels it MB); administrators can change it, with zero meaning no configured share-size limit. This per-share limit is separate from the default 10 GB user storage quota (stored plus reserved bytes); it is not a bandwidth limit.

Upload paths reject Windows drive/alternate-stream prefixes, UNC/rooted-backslash paths, reserved device names, control characters, invalid Windows punctuation, and components ending in a dot or space. Relative backslash folder paths and Unicode names remain supported. Historical POSIX absolute/dot-traversal inputs are normalized into relative paths; the resulting stored path must still pass portable archive validation.

At completion, the server checks exact sizes and hashes the staged bytes before publishing. A checksum mismatch returns HTTP 409 without publishing the share. API clients may omit expected checksums, but then only a **server-side baseline** is established: this does not prove that the uploaded bytes matched the client's original file. Existing legacy files establish their baseline on their first integrity-aware download. Chunk resumption does not imply a persistent browser upload manager that survives arbitrary reloads; the selected file data/session must remain available to the client.

### Individual files and ZIPs

ZIP generation revalidates persisted member paths, including legacy records. Unsafe archive metadata blocks the ZIP with HTTP 409 rather than silently renaming entries or rewriting existing records/files. Individual-file downloads do not construct ZIP member paths.

Before serving a download, the server verifies the source bytes as it copies them into a private temporary snapshot. A single-file response contains that file; a multi-file share response is an uncompressed ZIP preserving relative paths. The SHA-256 response digest covers the **exact complete file or ZIP representation**, including ZIP metadata, not just the individual archive members. It is exposed through `X-Checksum-SHA256` and `Repr-Digest`.

The browser reads and hashes the response stream and reports verification success only when the complete byte count and checksum match. In browsers with File System Access API support in a secure context, disk writes are committed only after verification; cancellation/mismatch aborts the write. Other browsers use a bounded in-memory Blob fallback limited to **128 MiB**. Larger verified downloads require a supported browser/secure context or an explicitly selected **unverified native download**. The UI does not silently turn a verification failure into success.

Native download uses normal browser saving and is **not browser-verified**, although the server still prepares and checks its snapshot. It does not bypass the server preparation limit. Native Range/resume responses carry the full representation digest; a partial response alone cannot verify the complete file. Browser-verified downloads require a complete HTTP 200 response.

### Capacity and failure behavior

- Only **two** snapshots may be prepared/served concurrently. A slot is retained until serving finishes; acquisition waits at most five seconds and then returns HTTP 503.
- API request bodies have a 30-second read deadline. Upload PATCH chunks instead renew a 60-second read deadline on progress, allowing long uploads without permitting indefinitely stalled reads. File/ZIP/backup responses renew a 60-second write deadline per bounded write; stalled writes terminate so download snapshots and slots can be released. These are transport deadlines, not a complete fair-use or minimum-bandwidth policy. Configure effective proxy-level deadlines and admission limits as well.
- `FILESHARE_VERIFIED_DOWNLOAD_MAX_BYTES` defaults to **16 GiB per snapshot**, bounding additional snapshot storage to about **32 GiB** at the default concurrency, beyond source files, upload staging, and backups. ZIP overhead also counts against the limit.
- Snapshots require extra disk I/O, temporary space, and preparation time before the response starts. They are removed after serving, with crash remnants cleaned on next startup.
- Oversized preparation returns HTTP 413; source checksum/size failure returns HTTP 409; missing files return HTTP 404; unavailable storage/preparation returns HTTP 503. Failures do not cause unchecked file bytes to be served.

SHA-256 detects mismatches and accidental corruption. It is **not** malware scanning, proof of authorship, end-to-end encryption, or protection against a compromised server replacing both the file and its checksum. It also does not establish whether a recipient later opens the saved file. HTTPS remains necessary.

## Security and operational caveats

- Keep first-run setup private; protect administrator access and service secrets. Do not expose the data directory, backups, or raw SQLite files through a web server.
- Share/invitation URLs are bearer capabilities, including open upload links and QR codes that encode them. Treat them as sensitive, add passwords when appropriate, and distribute passwords through a separate channel. Expiry cannot revoke a copy already downloaded. Previews are served from the same origin with a sandboxing Content-Security-Policy and a fixed content-type allowlist; SVG, HTML and PDF are never previewed inline.
- Passwords are bcrypt hashes. SQLCipher encrypts database-held secrets/mail metadata and new database backups, but not uploaded files, old plaintext backups, or all volume metadata. The application is not a zero-knowledge storage service; the running server has access to the external key and plaintext data.
- **Blocking repeated failed sign-ins (fail2ban-style).** After **10 failed attempts within 10 minutes** from one client address (wrong password, wrong two-step code or recovery code, wrong share password) that address is blocked for **30 minutes** from the sign-in style routes (login, two-step, share unlock, registration/reset routes, invite accept, setup, external login start); it receives HTTP 429 with `Retry-After`. A repeat offender is blocked twice as long each time (30 min, 1 h, 2 h … at most 24 h); strikes are forgotten after 24 hours without a new block or when an administrator unblocks the address. Public downloads, previews, token refresh and already signed-in sessions keep working. IPv6 clients are grouped by /64. Blocks are stored in the database (table `ip_bans`, cleared on database import) so a restart does not lift them. **Admin → Security** shows the blocked addresses with an **Unblock** button, lets you switch the feature off and keep a list of addresses or ranges that are never counted or blocked (put your own network there; the screen shows your current address). The activity log records that a block happened but, as always, no address. This works on the client address from `SENDORA_TRUSTED_PROXIES`: **behind a reverse proxy without it every visitor shares the proxy's address, so one attacker would block everybody** — set it first. It complements, and does not replace, the per-account lockout below. Recovery: start with `SENDORA_DISABLE_IP_BANS=true`.
- Request-origin checks, authentication rate limits, bounded work queues, and file-integrity checks are implemented but are not a complete abuse/DoS defense. Rate budgets are kept per route class (sign-in, token refresh, share unlock, invite accept, setup, public downloads/previews), so flooding one does not block the others, and per client address. By default the client is the TCP peer and forwarded headers are ignored, so **behind a proxy all visitors share one budget until you set `SENDORA_TRUSTED_PROXIES`**; with it set, only those proxies are believed and the right-most untrusted forwarded address is used. Signed-in users may send at most 100 notification/invite emails per hour, share/invite text fields are length-limited, download notifications are limited to one per share per minute, outgoing mail goes through a bounded worker queue, and never-verified registrations are deleted after 48 hours. Registration answers the same for a taken username or email. File and share names may not contain invisible formatting/bidi-override characters (zero-width joiners stay allowed) and path segments are limited to 200 bytes. The web interface is served with a Content-Security-Policy (same-origin only), and HSTS is sent when requests arrive over HTTPS. Accepting an invite is a POST, so link scanners that merely fetch a link cannot consume it. Account lockout remains a deliberate trade-off: anyone who knows a username can lock that account's password sign-in for 15 minutes; administrators can unlock it. Add appropriate proxy-level limits/monitoring rather than blindly trusting client-supplied forwarding headers.
- Configure the canonical public HTTPS URL so cookie security, origin checks, callbacks, and links agree. Changing the signing secret or applying security/session upgrades can require users to sign in again; it should not require recreating accounts.
- There is no built-in antivirus pipeline, bandwidth quota, or guarantee of external mail/provider availability. Per-user stored/reserved-byte quotas do not replace filesystem capacity planning: upload staging, snapshots, and backups still compete for disk capacity.
- Public operation needs privacy/retention and abuse policies, monitoring, capacity limits, protected backups, tested restores, dependency updates, and validation of actual SMTP/OAuth integrations. The proposed Sendora name has not been checked for trademark or domain availability.

## Tests and verification

After configuring SQLCipher 4.20.0 headers/library and `PKG_CONFIG_PATH` as described above, the available checks can be run from the project root:

```sh
export CGO_ENABLED=1
npm --prefix web ci
npm --prefix web run build
go vet ./...
go test -race ./... -cover -timeout 180s
node --test web/src/*.test.mjs
```

There is currently no `npm test` script; the JavaScript tests use Node's built-in test runner directly. Go tests use temporary data directories rather than the running instance's `./data`. The race-enabled Go test command requires a platform/toolchain that supports Go's race detector (including its native compiler requirements).

Tests cover authentication/permissions, resumable uploads, share lifecycle, editing and download limits, share password policy, preview headers and Range handling, open upload links (including concurrency), activity-log retention and exports, external-only login protections, invitations, settings/branding/themes, database backups/migrations, email flows/localization/queue behavior, security regressions, simulated Entra/OIDC checks, upload tampering, ZIP digests, and bounded snapshot behavior. JavaScript tests cover SHA-256 vectors/cancellation, verified-download behavior using browser API mocks, exact GB conversion and display, the share password policy helper, and the built-in QR encoder (structure, Reed–Solomon checks and capacity). The QR encoder was additionally decoded with Apple's Vision framework during development; that check is not part of the automated suite.

These are automated implementation checks, not a full penetration test or a complete browser end-to-end suite. A frontend build is not proof of real-browser behavior. Test supported browsers, large-file transfers, reverse-proxy settings, SMTP delivery, real Entra/provider login, permissions, and disaster recovery in your own deployment. No test results are implied merely by the presence of these commands.

## Current limitations

- Storage is local filesystem data plus SQLite, with exactly one application process per data directory. High availability, multi-replica operation, shared-storage scaling, and object-storage backends are not supported.
- Uploads use Sendora's custom resumable PATCH API, not the tus/tusd protocol. There is no persistent browser upload manager guaranteed to survive page reloads.
- Registration domain restrictions do not apply to external providers; use each provider's own tenant or domain controls.
- There is no PWA, stock-photo background integration, passkey/WebAuthn or SMS/e-mail codes as a second factor (TOTP and recovery codes exist), LDAP login, end-to-end encryption of shared files, or authenticated linking of additional login providers to existing accounts (existing links can be removed). Because of this, an administrator can only turn off local login after an external login was linked at first external sign-in.
- QR codes are generated in the browser for links up to 213 bytes. The grid view uses full-size images, not generated thumbnails. Previews are not checksum-verified and are unavailable for shares with a download limit. Gallery and preview behavior in real browsers should be tested in your deployment.
- The activity log is best-effort and asynchronous; a crash can lose the latest entries, and it does not replace external audit logging.
- There is no automatic data import, file-path rebasing, or built-in backup restore endpoint. Database-only backups do not replace full storage backups.
- Antivirus scanning, bandwidth quotas, and encryption of uploaded files/the full volume are not built in. SQLCipher protects the database/new database backups only; per-user stored/reserved-byte quotas are implemented. Browser-verified large downloads depend on browser support and temporary disk capacity.
- Real SMTP delivery, OAuth provider configuration, browser behavior, reverse-proxy settings, and disaster recovery must be validated for each deployment; automated tests do not establish production readiness.

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
