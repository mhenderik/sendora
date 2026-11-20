# Threat model

What Sendora protects, against whom, what it does not protect, and where the checks are. It is the starting point for a security audit; the file format is specified separately in [FILE-FORMAT.md](FILE-FORMAT.md).

Status: written for 0.1.5 from the code and from independent code reviews; updated for the object storage of 0.2.0 (in development, [STORAGE.md](STORAGE.md)). Sendora has not been audited by an independent party yet.

## 1. Assets

| Asset | Where it lives | Protected by |
|---|---|---|
| Shared file contents | `DATA_DIR/shares/<link id>/`, or (0.2.0) an object store under `<prefix>files/<random>` | AES-256-GCM per file (new uploads; always for files in an object store); volume encryption for older local files |
| Share metadata, accounts, settings, secrets, audit log | `DATA_DIR/sendora.db` | SQLCipher (AES-256-CBC + HMAC-SHA512 per page) |
| The root key | the file named by `SENDORA_DB_KEY_FILE` | file mode/owner checks, never logged, never in a connection string |
| Share link IDs (about 86 bits) | database, URLs given to recipients, and the names of the share folders in `DATA_DIR/shares/` | `crypto/rand`, unguessable; optional share password; the folder names are visible to anyone with a copy of the volume (see 5.1) |
| Account credentials, sessions, MFA factors | database | bcrypt, hashed refresh/reset tokens, TOTP/WebAuthn |
| Backups | `DATA_DIR/backups/`; optionally a copy in the object store (`<prefix>backups/`) | same SQLCipher key |
| Object storage credentials | settings and `storage_locations` in the database; optionally `*_FILE` Secrets | SQLCipher; write-only in the interface; step-up for changes; never written to the audit log |

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
| Object storage provider (0.2.0, optional) | Stores, serves and may read, delete, replace or roll back the objects in the bucket; sees access patterns. Does not have the key |
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
| Only ciphertext reaches an object store, under keys unrelated to links and names; a tampered database row cannot reach other objects | `putUploadRemote` (encrypt before `Put`), `storage.NewFileKey`, `storage.ValidFileKey`, `store.FileObjectKey`, migration encrypts legacy plain files | storage and migration tests |
| Downloads are verified | SHA-256 recorded at upload, checked before a verified download is served | integrity tests |
| The audit log is append-only and tamper-evident | hash chain, checkpoints, `sendora-db audit-verify` | 0.1.5 audit tests |

## 5. Threats and residual risk

### 5.1 Disk, backup or snapshot theft (no key)
Mitigated for the database, for new share files and for backups. **Residual:** files uploaded before 0.1.2; upload parts in progress and temporary download snapshots (plain, short-lived); branding images; file names, sizes, counts and modification times (no padding). **Share link IDs are the folder names under `DATA_DIR/shares/`**, so a key-less copy of the volume reveals them: with a link ID a share can be opened as long as it is active and has no password. Protect the volume and its snapshots accordingly, use share passwords for sensitive data, and keep expiry short.

### 5.1a Object storage provider (0.2.0, optional)
With `storage_backend` set to `s3`, `azure` or `gcs`, share files are encrypted **before** they leave the server; the provider only receives ciphertext under random names (`files/<32 hex>`), so this setup avoids the 5.1 residuals for those files: no link IDs, no file names in object names, no plain legacy files (migration encrypts them). Remote backup copies are the SQLCipher backups. The provider sees object sizes (plaintext size + 53 + 16 per 64 KiB, no padding), counts, write/read/delete times and request addresses, and could correlate uploads made at the same moment.
- **Tampering** (modified, truncated, extended, reordered chunks, an object copied over another): detected by the GCM tags and the file-ID binding of format v2; the download is refused (409) and `file_decrypt_failed` is recorded. A migration verifies every copy completely before switching.
- **Deletion, withholding or rollback** of objects (or of a whole bucket): an availability risk, not a confidentiality risk. `sendora-db verify-files` reports missing or failing objects; objects are never overwritten by Sendora (every write uses a new random key), so a rollback can only remove data. Use provider versioning/soft delete and monitoring (see [STORAGE.md](STORAGE.md#9-recommended-bucket-settings)).
- **Credentials** are stored in the SQLCipher database (per location, so old files stay readable after a switch) or come from `*_FILE` Secrets or workload identity. They are write-only in the interface and never audited. Changing a storage setting, testing a connection and starting a migration need a step-up. A stored secret is sent only to the endpoint/account it was entered for: changing the endpoint, account or access key requires entering it again, so a stolen administrator session cannot redirect it (relevant for GCS, where bearer tokens are sent to the endpoint). Plain `http://` endpoints are accepted only for loopback, private and cluster-internal hosts.
- **Residual:** the provider learns sizes, timing and access patterns; versioned or soft-deleted copies of deleted or erased files persist at the provider until their retention ends (still encrypted, but decryptable with the database key); whoever holds both a copy of the bucket and the database key (or the database including its credentials and the key) can read everything; a provider outage makes downloads (503) and uploads fail; credentials with more rights than needed (an Azure account key, a broad IAM policy) widen the impact of a server compromise.

### 5.2 Malicious or compromised administrator
- Cannot read shares, download history or share names through the interface or the API (statistics are counts). The audit log is readable by administrators and auditors alike; it names shares only as `share#N`, so an administrator can see when shares are created, changed or downloaded and by whom, but not what they are called or what they contain. Every export of the log and every backup download is recorded before it is served.
- Cannot silently take over an account (see 4), cannot switch off or wipe the audit log.
- **Residual:** an administrator can still delete a user (and so that user's shares), change settings (including the storage backend, after a step-up: new files then go to another bucket), import a database and, with access to the host, everything the host operator can. Administrators can see user names and email addresses.

### 5.3 Host operator or memory access
Not defended. The key and plain data are in process memory; the key file is readable by the service account. Mitigations are operational: disable core dumps, restrict host access, keep the key apart from the data, forward the audit log off the host (standard output, syslog or webhook with a periodic chain-head heartbeat; see [configuration](configuration.md#forwarding-the-audit-log)).

### 5.4 Account attacks
Credential stuffing, password guessing, MFA bypass, session theft. Mitigated by bcrypt (cost 12), lockout, IP bans, TOTP and WebAuthn, rotating refresh tokens with reuse detection, short access tokens (15 min), absolute session limit (30 days), a minimum of 8 characters with screening of common, personal and trivial passwords (optionally the Have I Been Pwned range API, k-anonymity), email notifications about password, factor and recovery-code changes and sign-ins from new browsers or networks, and step-up re-authentication plus an optional network allowlist for critical administrator actions. A stolen administrator session therefore cannot delete users, change roles, providers or security settings, or download backups without the password or second factor. **Residual:** account lockout can be used to annoy a known username; OIDC logins rely on the identity provider's own MFA.

### 5.5 Share link attacks
Link guessing (86 bits, 404/410 uniform), password guessing (bcrypt, per-IP and per-share throttle), download-slot exhaustion (bounded; see known limits), recipients forwarding links (inherent to link sharing; expiry, download limits and passwords are the controls).

### 5.6 Web attacks
XSS (no inline script, strict CSP, escaped rendering, validated theme/translation imports), CSRF (`Sec-Fetch-Site`/`Origin` check, bearer-token authorisation), clickjacking (`frame-ancestors 'none'`), content sniffing (`nosniff`, downloads are `application/octet-stream` attachments, previews only for a type allowlist with `CSP default-src 'none'; sandbox`), SSRF through the OIDC issuer (loopback and link-local targets refused).

### 5.7 Supply chain
SQLCipher 4.20.0 is built from a checksum-verified source tarball. Base images are pinned by digest (Debian 13 "trixie" runtime) and kept current by Dependabot; apt packages are not version-pinned. Release images carry build-provenance and SBOM attestations and a cosign signature (from 0.1.5). Go modules are pinned by `go.sum`.

### 5.8 Personal data and erasure
Users can export their own data and delete their account; administrators can erase an account. Erasure deletes shares, files, invites, sessions, second factors and linked sign-ins and pseudonymises the account row (`erased-<id>`, no name, email or password). **Legitimate-interest exception:** audit records written while the account existed keep their `actor_name` (the former username) and target labels. They are not rewritten because the audit log is a tamper-evident chain kept for the security of the service and the accountability of administrators (GDPR art. 6(1)(f); art. 17(3)(e) for legal claims). They are removed by the normal retention (`audit_retention_days`, 90 to 365 days). Backups made before the erasure still contain the account until they are rotated out (`backup_keep`, also for remote backup copies). Files in an object store are deleted through a queue right after the erasure (retried while the store is unreachable); copies kept by the provider's versioning or soft delete remain until their retention ends.

## 6. Known limits (documented, not fixed)
- The root key is used raw by SQLCipher and, as input, for the file-key derivation (no formal key separation; no known attack).
- No key rotation once encrypted files exist.
- Whole-database rollback or page replay is not detected.
- Plain upload parts and download snapshots on disk during transfers (also with an object store: parts are assembled and encrypted locally).
- Object sizes and access patterns are visible to an object storage provider (no padding).
- Verified-download slots are global (3) and can be held by a client that reads slowly.
- Not independently audited.
