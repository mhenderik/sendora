# Security policy

## Reporting a vulnerability

Please **do not** open a public issue for a security problem. Use GitHub's
private vulnerability reporting for this repository (Security tab → *Report a
vulnerability*). Include the version or commit, how to reproduce the problem, and
what an attacker gains. Do not include real user data or credentials.

You can expect an acknowledgement within a few days. Please allow reasonable time
for a fix before you disclose the problem publicly.

## Supported versions

Only the latest release (or the `main` branch) receives security fixes.

## Scope

In scope: the Go server, the web interface, authentication (passwords, MFA, OIDC),
sessions, shares and uploads, the database encryption handling, the import and
backup tooling, and the container image and example manifests.

Out of scope: problems that need an already compromised server, administrator
account or database key; denial of service through unlimited traffic (put a proxy
in front, see `SENDORA_TRUSTED_PROXIES` in [Configuration](https://github.com/mhenderik/sendora/wiki/Configuration)); social engineering;
missing hardening of your own deployment.

## Independent review

Sendora was written largely by an AI coding assistant for a maintainer who is not a software developer. It has automated tests but has **not** been audited by an independent security professional. Reviews and responsible disclosure are very welcome.

## Known trade-offs

These are documented, deliberate decisions, not undisclosed bugs (details in the
[Security notes](https://github.com/mhenderik/sendora/wiki/Security-Notes)):

- Anyone who knows a username can lock that account's password sign-in for 15
  minutes. Administrators can unlock it.
- Invite tokens are stored in the database in plain text.
- Outgoing OpenID Connect requests are not restricted; only administrators can
  configure providers.
- Passkey support is verified against a software authenticator and test vectors,
  not against every real device. Keep a recovery code.

## Secrets

The database key (`SENDORA_DB_KEY_FILE`) is the only secret that cannot be
regenerated: losing it makes the database and its backups unreadable. Keep a
separate, protected copy and never commit it.
