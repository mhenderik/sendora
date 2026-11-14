# Sharing, uploads and downloads

Share options, editing, the activity log and the integrity guarantees of uploads and downloads.

[Back to the documentation index](README.md)

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
- `SENDORA_VERIFIED_DOWNLOAD_MAX_BYTES` defaults to **16 GiB per snapshot**, bounding additional snapshot storage to about **32 GiB** at the default concurrency, beyond source files, upload staging, and backups. ZIP overhead also counts against the limit.
- Snapshots require extra disk I/O, temporary space, and preparation time before the response starts. They are removed after serving, with crash remnants cleaned on next startup.
- Oversized preparation returns HTTP 413; source checksum/size failure returns HTTP 409; missing files return HTTP 404; unavailable storage/preparation returns HTTP 503. Failures do not cause unchecked file bytes to be served.

SHA-256 detects mismatches and accidental corruption. It is **not** malware scanning, proof of authorship, end-to-end encryption, or protection against a compromised server replacing both the file and its checksum. It also does not establish whether a recipient later opens the saved file. HTTPS remains necessary.
