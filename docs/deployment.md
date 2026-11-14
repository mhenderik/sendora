# Deployment

Docker behind HTTPS and the Kubernetes example.

[Back to the documentation index](README.md)

## Docker deployment behind HTTPS

The multi-stage Dockerfile builds the frontend with Node 22, compiles Go 1.26 with CGO, and builds **official SQLCipher 4.20.0 source** with OpenSSL on Debian Bookworm. The source URL is `https://github.com/sqlcipher/sqlcipher/archive/refs/tags/v4.20.0.tar.gz`, pinned to SHA-256 `38705f51b61ac0636a5fd064f9a77fafabc7227b2aa2513f1b14c8bb1102affc`. Build and runtime stages check `PRAGMA cipher_version`; the application also checks the engine when opening a database. The runtime is Debian Bookworm slim with SQLCipher/OpenSSL shared libraries, CA certificates, timezone data, and UID/GID 10001. It includes the `sendora-db` offline utility and SQLCipher CLI; the Go executable is **not static**. It declares `/data` as a volume and includes a health check for **`http://127.0.0.1:8080/api/health`** inside the container. This endpoint reports process health only (no version number, which is shown to administrators under Statistics), not complete storage or SMTP readiness.

1. Provision storage that UID 10001 can write and mount the separately protected database key as described in Quick start.
2. Set `APP_URL: https://files.example.com` in the Compose environment (an example is commented in the file).
3. Build/start the service and finish administrator setup while access is restricted.
4. Configure an HTTPS reverse proxy to forward the public origin to the backend. For a proxy on the same host, the loopback mapping can remain unchanged. A proxy in another container cannot reach this service through its own `127.0.0.1`; arrange an appropriate private Docker network/upstream instead.
5. Configure SMTP and login providers, then test actual delivery, sign-in, file transfer, and restore procedures.

Keep the backend inaccessible except through the trusted proxy. Allow the application's methods, including upload `PATCH`, and accommodate 8 MiB browser chunks (the API permits up to 64 MiB per chunk), download preparation delays, and long transfers. Avoid proxy transformations/compression of download responses that would invalidate their exact size/checksum representation.

If changing the container's `PORT`, also adjust the published port, proxy upstream, and Docker health check: the health check is hard-coded to 8080. Changing only the host port mapping does not require changing the container port.

Run **exactly one Sendora process/container per data directory**. Multiple live instances, shared SQLite mounts, and replica scaling are not supported. Startup recovery and background jobs assume exclusive use, in addition to upload/integrity locks being process-local.

## Kubernetes

See the step-by-step [Kubernetes guide](kubernetes.md): ConfigMap and Secrets, first-run setup, Ingress, upgrades, backups and troubleshooting.
