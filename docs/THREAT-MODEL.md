# Threat model

What Sendora protects, against whom, what it does not protect, and where the checks are. It is the starting point for a security audit; the file format is specified separately in [FILE-FORMAT.md](FILE-FORMAT.md).

Status: written for 0.1.5 from the code and from independent code reviews. Sendora has not been audited by an independent party yet.

## 1. Assets

| Asset | Where it lives | Protected by |
|---|---|---|
| Shared file contents | `DATA_DIR/shares/<link id>/` | AES-256-GCM per file (new uploads); volume encryption for older files |
| Share metadata, accounts, settings, secrets, audit log | `DATA_DIR/sendora.db` | SQLCipher (AES-256-CBC + HMAC-SHA512 per page) |
| The root key | the file named by `SENDORA_DB_KEY_FILE` | file mode/owner checks, never logged, never in a connection string |
| Share link IDs (about 86 bits) | database, URLs given to recipients | `crypto/rand`, unguessable; optional share password |
| Account credentials, sessions, MFA factors | database | bcrypt, hashed refresh/reset tokens, TOTP/WebAuthn |
| Backups | `DATA_DIR/backups/` | same SQLCipher key |

## 2. Actors

| Actor | Capability assumed |
|---|---|
| Anonymous internet user | Reaches the public routes; may hold a share link |
| Share recipient | Holds a link (and maybe a password); can download and preview |
| Authenticated user | Uploads and manages their own shares |
| Guest (reverse-share invite) | Upload-only session for one invite |
| Administrator | Manages users and settings. **Must not read other users' shares.** |
| Auditor (0.1.5) | Read-only access to the audit log; can manage nothing |
| Host operator | Root on the server or the container host |
| Disk/backup thief | Has a copy of the volume or a backup, but not the key |
| Network attacker | Between browser and server; Sendora expects TLS in front |

## 3. Trust boundaries and assumptions

- TLS is terminated in front of Sendora (reverse proxy or Ingress). `SENDORA_TRUSTED_PROXIES` tells which addresses may set `X-Forwarded-For`.
- The server process holds the key in memory. Anyone who controls the running process or the key file can read everything: **Sendora is not end-to-end encrypted.**
- The browser is trusted to run the shipped JavaScript; there is no inline script (CSP `script-src 'self'`).

## 4. Security properties and where they are enforced

| Property | Enforced in | Tested by |
|---|---|---|
| A stolen disk or backup without the key reveals no database content and no new share file content | `internal/store/cipher.go`, `internal/filecrypt` | `cipher_params_test.go`, `encryption_e2e_test.go` |
| A modified, truncated, extended, reordered or swapped encrypted file is never served as valid data | `internal/filecrypt` (per-chunk tags, last-chunk flag, file ID binding in format v2) | `filecrypt_test.go`, vectors, e2e tamper tests |
| Only the uploader manages a share and reads its download history; administrators cannot | `ownedShare`, `user_id` in every UPDATE | `share_*_test.go`, `stats_privacy_test.go` |
| The administrator statistics contain counts only | `admin.go` stats | `TestAdminStatsDoNotRevealShares` |
| An administrator cannot take over another account silently | admin cannot set a password for others; email change unverifies; MFA reset needs the user's confirmation; user is notified; actions are in the audit log | 0.1.5 tests |
| Brute force is limited | per-IP rate classes, IP ban, per-account lockout, per-share unlock throttle, setup throttle | `security_test.go` family |
| Tokens cannot be confused | HS256 only, `purpose` claim, per-purpose HKDF subkeys, credential-version claim | auth tests |
| Share files cannot escape their directory | `store.SharePath`, `validateUploadPath`, `validateArchivePath` | `archive_path_test.go` and others |
| Downloads are verified | SHA-256 recorded at upload, checked before a verified download is served | integrity tests |
| The audit log is append-only and tamper-evident | hash chain, checkpoints, `sendora-db audit-verify` | 0.1.5 audit tests |

## 5. Threats and residual risk

### 5.1 Disk, backup or snapshot theft (no key)
Mitigated for the database, for new share files and for backups. **Residual:** files uploaded before 0.1.2; upload parts in progress and temporary download snapshots (plain, short-lived); branding images; file names, sizes, counts and modification times (no padding).

### 5.2 Malicious or compromised administrator
- Cannot read shares, download history or share names through the interface or the API (statistics are counts; the audit log hides share-level events from administrators).
- Cannot silently take over an account (see 4), cannot switch off or wipe the audit log.
- **Residual:** an administrator can still delete a user (and so that user's shares), change settings, import a database and, with access to the host, everything the host operator can. Administrators can see user names and email addresses.

### 5.3 Host operator or memory access
Not defended. The key and plain data are in process memory; the key file is readable by the service account. Mitigations are operational: disable core dumps, restrict host access, keep the key apart from the data, forward the audit log off the host.

### 5.4 Account attacks
Credential stuffing, password guessing, MFA bypass, session theft. Mitigated by bcrypt (cost 12), lockout, IP bans, TOTP and WebAuthn, rotating refresh tokens with reuse detection, short access tokens (15 min), absolute session limit (30 days). **Residual:** account lockout can be used to annoy a known username; OIDC logins rely on the identity provider's own MFA.

### 5.5 Share link attacks
Link guessing (86 bits, 404/410 uniform), password guessing (bcrypt, per-IP and per-share throttle), download-slot exhaustion (bounded; see known limits), recipients forwarding links (inherent to link sharing; expiry, download limits and passwords are the controls).

### 5.6 Web attacks
XSS (no inline script, strict CSP, escaped rendering, validated theme/translation imports), CSRF (`Sec-Fetch-Site`/`Origin` check, bearer-token authorisation), clickjacking (`frame-ancestors 'none'`), content sniffing (`nosniff`, downloads are `application/octet-stream` attachments, previews only for a type allowlist with `CSP default-src 'none'; sandbox`), SSRF through the OIDC issuer (loopback and link-local targets refused).

### 5.7 Supply chain
SQLCipher 4.20.0 is built from a checksum-verified source tarball. Base images are referenced by tag (not yet by digest) and apt packages are not pinned: see the hardening notes. Go modules are pinned by `go.sum`.

## 6. Known limits (documented, not fixed)
- The root key is used raw by SQLCipher and, as input, for the file-key derivation (no formal key separation; no known attack).
- No key rotation once encrypted files exist.
- Whole-database rollback or page replay is not detected.
- Plain upload parts and download snapshots on disk during transfers.
- Verified-download slots are global (3) and can be held by a client that reads slowly.
- Not independently audited.
