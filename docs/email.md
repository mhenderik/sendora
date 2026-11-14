# Email, registration and templates

Public registration, SMTP, welcome mail, editable templates and notification semantics.

[Back to the documentation index](README.md)

## Public registration and SMTP

Admin → Settings controls **Allow self registration** (off by default) and SMTP host, port, username, password, From address, and security mode. Defaults are port 587 and STARTTLS. `tls` means implicit TLS; `starttls` requires a successful TLS upgrade. Use the mode and port your SMTP provider specifies. `none` is available but should be restricted to an intentionally trusted mail transport, not an untrusted network. A blank SMTP password when saving retains the existing secret.

Usernames are **not case-sensitive**: `Admin`, `admin` and `ADMIN` are the same account (signing in and uniqueness), and the capitalisation typed at creation is kept for display. This covers ASCII letters (A–Z); other alphabets are still compared exactly. If an older database already contains names that differ only by letter case, a warning is logged at startup and the unique index is skipped until you rename them; sign-in then prefers the exact-case match.

Outgoing mail uses the **From address** as sender (the SMTP username is only used to sign in, so the two may differ; Gmail only accepts a From address that is a verified *Send mail as* alias of the account and otherwise replaces it with the account address). An optional **Reply-To address** (Admin → Settings, one address with an optional name) is added to every message so replies go there instead of to the From address.

Public signup **always requires email verification**, regardless of the legacy `require_email_verification` setting. It requires a username, valid email, and 8–72-byte password. If SMTP settings are missing/invalid, signup returns HTTP 503; merely passing the configuration check does not prove the relay is reachable or delivering mail. New registrants must enter the emailed six-digit code before login. Codes expire after 30 minutes; verification attempts and resends are rate-limited.

Configure a canonical HTTPS `APP_URL` and send a real administrator test email before advertising signup. Confirm delivery through your relay/spam filters and configure the sender domain's SPF/DKIM/DMARC as appropriate. Password reset links expire after one hour and require a trusted application URL. Local first-run setup and administrator-created accounts do not depend on public signup SMTP.

Public share links permit unauthenticated downloads, subject to the share's password/expiry/download limit; they are not unrestricted anonymous upload access. Guest uploads require a reverse-share invitation. Review registration and invitation policies before making the service public.

New share links use a readable, cryptographically random identifier: five English words with two seven-digit number blocks, for example `/share/tiger-bike-chest-3485217-river-2765290-cloud`. Independent choices provide at least 86 bits of randomness; words do not come from the filename, username, or share title. Existing hexadecimal share links and stored file paths remain valid and unchanged. Public links remain bearer links: anyone who receives an unprotected link can access its files. Use share passwords and expiry when appropriate.

**SMTP password protection.** The stored SMTP password is only ever sent to the server it was entered for: changing the SMTP server, port, user or security mode requires typing the password again. The same applies to a login provider's client secret when its type, client ID or issuer changes. The *Send test email* button reports a short reason (cannot connect, sign-in failed, secure connection failed); the technical details are only in the server log.

**Mail limits.** Each signed-in user may trigger at most 100 notification/invitation emails per hour, and the whole instance at most **500 per hour**, so several (self-registered) accounts cannot turn your mail domain into a spam relay.

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

Profile email-language preferences are separate from menu-language preferences. A uniquely matching ordinary account recipient uses its email-language preference; external recipients use the language selected by the sender (English if none is supplied). Missing translations fall back to English. Menu (interface) languages are separate: they are translated through JSON under Admin → Languages (see [Translations](translations.md)).

### Download notification semantics

History is accessible only to the share owner or an administrator. Each served download request, **including Range/resume requests**, is counted. Notifications record the **start of serving**, not successful transfer completion or proof that a recipient saved, opened, or read the file. Retries can therefore consume multiple download-limit units.

Download notifications use a persistent queue with a maximum of **1,000 pending/reserved jobs**. If a new notification cannot be reserved because the queue is full, the download is rejected with HTTP 503 and `Retry-After` before serving. SMTP delivery is attempted at most three times, with retry delays of 30 and 60 seconds; after that the job is marked failed. Only the newest 1,000 failed records are retained. Delivery is at-least-once: a crash after SMTP accepts mail but before acknowledgment can cause duplicate messages. Successful delivery is not guaranteed. Other template mail is not promised the same persistent retry behavior. Monitor logs and relay delivery, not just HTTP health.
