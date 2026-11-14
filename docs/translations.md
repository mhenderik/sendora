# Translations

Translate the interface and the emails through JSON files.

[Back to the documentation index](README.md)

## Translating the interface

Example language files (Japanese UI and email) are in [`custom-translations/`](../custom-translations/README.md).

Buttons, labels and messages are translated through JSON dictionaries. The English text is the key; built-in languages are English, Dutch, German, French and Spanish. Administrators can translate or correct texts at runtime, without rebuilding, under **Admin → Languages**:

1. **Download JSON** for a language (or **Download template** for a new code such as `pt-br`). The file lists every translatable text with its current text: `{"code": "pt-br", "name": "Português (Brasil)", "translations": {"Save": "Salvar", ...}}`.
2. Translate the values offline. Keep the keys and the placeholders (`{n}`, `{name}`, `%s`, `%d`, `%q`) unchanged; they may move within a sentence but must all be present.
3. **Import JSON**. The file is checked in the browser and again on the server, and replaces the stored dictionary of that language atomically.

Rules and limits: at most 32 custom languages (overrides of built-in languages do not count), a language name of 1 to 80 bytes without control characters, at most 2000 bytes per text, at most 3000 texts, and a file of at most 1 MiB. Only known texts are accepted (interface texts and server messages); unknown keys, mismatched placeholders and control characters are rejected and nothing is stored. Texts are always rendered as plain text.

Lookup order is: stored text, built-in text, English. Importing a built-in code (for example `nl`) stores per-key overrides on top of the bundled translation; **Reset to default** removes them. Deleting a custom language switches its users back to English. New languages appear in every language menu, including the sign-in page. Imports and deletions are recorded in the event log (language code and number of texts only).

API (administrators): `GET /api/ui-languages`, `GET /api/ui-languages/{code}/export` (stored texts), `POST /api/ui-languages`, `DELETE /api/ui-languages/{code}`. Public, short-lived and ETag-validated: `GET /api/i18n/languages` and `GET /api/i18n/{code}`. Built-in texts live in the frontend bundle, so the complete download is assembled in the browser from the bundled text plus the stored overrides. The list of known texts is generated at build time (`i18n-keys/` inside the web build); after adding a `t('…')` string to the sources run `node web/scripts/i18n-keys.mjs`.
