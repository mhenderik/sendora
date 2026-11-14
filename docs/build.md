# Build, develop and test

How to build Sendora from source, run it locally and run the test suites.

[Back to the documentation index](README.md)

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

The terminal shows a line `first-run setup code: XXXX-XXXX-XXXX`; open http://127.0.0.1:8080, enter that code and create the administrator account. After setup, stop and restart with `SENDORA_DB_INITIALIZE=false ./sendora`. For an existing encrypted installation, reuse its key and keep initialization false. Every `go run` example below requires the exported key path and SQLCipher/CGO configuration; none bypass the initialization or key checks.

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
