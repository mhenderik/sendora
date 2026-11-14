# Backups, restore and migration

Where the data lives, how to back it up and how to move or import a database.

[Back to the documentation index](README.md)

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

Imported databases are inspected without trusting their schema (`trusted_schema` off). Files with generated columns are refused, and share link IDs and usernames must be unique (a database where two shares use one link ID is refused, since they would share a storage folder).

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
