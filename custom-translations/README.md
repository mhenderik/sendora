# Custom translations (examples)

Ready-to-import language files for **Admin → Languages** (interface) and
**Admin → Email templates** (mail). Nothing here is bundled in the app: import a
file to add or replace a language at runtime, without rebuilding.

| Folder | Interface (`sendora-ui-<code>.json`) | Email (`sendora-email-<code>.json`) |
|---|---|---|
| `ja/` | Japanese, all 791 texts | Japanese, all 8 mail events |

## Import

- **Interface:** Admin → Languages → *Add a language* → *Import JSON* (or *Import JSON* on an existing row).
- **Email:** Admin → Email templates → select the language → *Upload translation JSON*, review, then *Confirm import*.

## File formats

Interface file:

```json
{ "code": "ja", "name": "日本語", "translations": { "English text": "translated text" } }
```

The English text is the key. Keep placeholders such as `{n}`, `{year}`, `%s` unchanged. Unknown keys are rejected; missing keys fall back to English.

Email file: `format` `sendora-email-templates`, `version` `1`, `language`, `name`, and a `templates` object with a `subject` and `body` per event. Keep `{{.Variable}}` placeholders unchanged.

## Make your own language

Download the current file of a language from the admin screens, translate the values (never the keys, event names or placeholders), change `code` and `name`, and import it.

## Note

These example translations are machine-assisted and have not been reviewed by a native speaker. Review them before production use. They contain no personal or deployment-specific data and are released under the project's license.
