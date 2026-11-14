# Contributing

Thanks for helping. Please read this first; it is short.

## Setup

```sh
git clone https://github.com/mhenderik/sendora.git
cd sendora
```

Requirements: Go (see `go.mod`), Node 22, and an official **SQLCipher 4.20.0** with
`pkg-config` (the Docker build compiles it; see the `Dockerfile`). The
[build guide](docs/build.md) has the commands. In short:

```sh
export CGO_ENABLED=1
npm --prefix web ci
npm --prefix web run build      # the Go binary embeds web/dist
go build -trimpath -o sendora .
```

## Tests

```sh
node --test web/src/*.test.mjs    # frontend logic and translations
go vet ./...
go test -race -timeout 40m ./...        # the app package takes several minutes
```

Please add or update a test with every behaviour change, especially anything
touching authentication, shares, uploads, quotas, or the database.

## Rules of the project

- **Security first.** No new network calls to third parties, no logging of
  secrets, addresses or tokens, additive database migrations only.
- **Never open the database while another query or transaction is open**; the
  store uses one pooled connection.
- **Translations:** English text is the key. Wrap user-visible text in `t('…')`,
  then run `node web/scripts/i18n-keys.mjs` to refresh `keys.json`. Dutch must stay
  complete (a test checks it); other languages may be partial. Example language
  files live in `custom-translations/`.
- Keep the documentation in `docs/` accurate: it is the user documentation.
- No personal data, real hostnames or credentials in code, tests or examples
  (use `example.com` and similar).

## Pull requests

Keep them focused, describe the problem and the fix, and make sure the tests above
pass. By contributing you agree that your work is released under the MIT License.
