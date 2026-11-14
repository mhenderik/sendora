# Quick start

Run Sendora in Docker in a few minutes. For a build from source see [Build and development](build.md).

[Back to the documentation index](README.md)

> **Ready-made Compose files** that use the published image (no build), including an HTTPS variant with automatic certificates, are in [`deploy/compose/`](../deploy/compose/README.md). The steps below build the image from source with the `docker-compose.yml` in the project root.

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

Open **http://localhost:8080** and complete the first-run setup to create the administrator account. Setup asks for a **one-time setup code**: Sendora prints it in its log when it starts without any account (`docker compose logs sendora`, look for `first-run setup code`). It changes on every restart until setup is done. Do this before allowing anyone else to access the service: setup is available while the database contains no users. Use a strong password (local account passwords must be 8–72 bytes).

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

For an existing local instance, read [Migration](backup-and-migration.md#migration-between-local-and-docker-storage) before changing mounts or copying data.
