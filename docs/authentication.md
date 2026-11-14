# Accounts and authentication

Two-step verification, password policy, lockout, automatic sign-out and Microsoft Entra ID.

[Back to the documentation index](README.md)

### Multi-factor authentication (TOTP, passkeys and recovery codes)

Administrators can add a second sign-in step for **local accounts** (Settings → Multi-factor authentication, per-user override in Users). Sendora uses the open TOTP standard (RFC 6238: HMAC-SHA-1, 6 digits, 30 seconds, ±1 step of clock drift), so any authenticator app works (Google/Microsoft Authenticator, Authy, 1Password, Bitwarden, Aegis, Apple Passwords); no external service is involved. The shared secret and the hashed recovery codes are stored in the encrypted database.

**Global policy** (`mfa_policy`): `off` (default; MFA is completely disabled: no second step, no enrolment, per-user settings ignored; this is also the emergency switch if everybody is locked out) · `optional` (users enrol themselves in Profile → Security) · `admins` (required for administrators) · `all` (required for every local account). `mfa_totp_enabled` and `mfa_webauthn_enabled` switch the authenticator-app and the passkey/security-key method on or off. A corrupt stored policy fails closed to `all`.

**Per user** (`inherit` / `required` / `exempt`): *required* forces MFA for that account even when the global policy is `optional`; *exempt* is never forced to enrol (for example a service account) — an exempt user who did enrol still uses the factor. Setting someone to exempt is logged. If no method is enabled, nobody can be required to enrol.

| Global | inherit | required | exempt |
| --- | --- | --- | --- |
| off | no MFA | no MFA | no MFA |
| optional | optional | required | optional |
| admins | admins required | required | not forced |
| all | required | required | not forced |

**Sign-in.** After the password (and, for new addresses, the e-mail code) Sendora answers `mfa_required` with a short-lived (5 minutes), single-use token instead of a session; no refresh token exists before the second step is completed. Wrong codes count towards the same lock as wrong passwords (5 failures lock the account for 15 minutes, only a successful second step resets the counter). A code can be used once (replay protection). Enrolment confirms the setup with a code, shows **10 one-time recovery codes once** (store them safely; they are only kept as hashes), and re-asks the account password for sensitive actions (setup, removal, new recovery codes). Every factor change signs out the account's other sessions.

**Required but not enrolled.** The user can sign in but only reaches their profile and the enrolment screens until a factor is added. This starts at the next login or token refresh (access tokens live 15 minutes), not in the middle of a session. A required user cannot remove their last factor. An administrator cannot enforce `admins` or `all` before enrolling their own account (or exempting themselves), to avoid locking themselves out; the order is therefore off → optional → enrol → admins/all.

**Not covered.** Guests and accounts that only sign in through an external provider (Entra, OIDC, Google, GitHub) are outside Sendora's MFA: use the provider's MFA and Conditional Access. SMS and e-mail codes are deliberately not offered. TOTP does not protect against phishing sites that relay codes in real time; passkeys (WebAuthn) are the stronger method.

**Passkeys and security keys (WebAuthn).** Users can register up to 10 passkeys or hardware keys (Touch ID, Windows Hello, a phone passkey, YubiKey and other FIDO2 keys) in Profile → Security, next to or instead of an authenticator app; any registered factor satisfies the second step, and recovery codes work for both. Requirements: the application URL must be a **secure origin** — `https://` with a domain name (set `APP_URL`), or `http://localhost` for development; browsers refuse IP addresses, so with anything else passkeys stay switched off (the bootstrap flag `webauthn_available` and the Profile page say so). The relying-party ID is the host of `APP_URL`: changing the domain later makes existing passkeys unusable, so keep a recovery path. Sendora asks for user verification but does not require it, uses attestation `none` and **does not evaluate attestation trust** (any authenticator model is accepted), supports ES256, EdDSA and RS256 keys, and rejects a signature counter that does not increase (a possibly cloned key). Registration and sign-in challenges are random, single use, expire after 5 minutes and are kept in memory only (bounded), so a restart simply requires starting the step again. Failed passkey attempts count towards the same lock as failed passwords. The implementation is a small, dependency-free verifier (`internal/webauthnlite`) tested against a software authenticator and the standard-library signature checks; it has **not** been verified against real hardware or browsers in this project, so try a real passkey or security key (and keep a recovery code) before you make MFA mandatory.

**Recovery.** A lost authenticator: use a recovery code. Locked-out users: an administrator resets their MFA (Users → reset MFA; all factors and sessions are removed). A locked-out administrator with no other admin: stop Sendora and run `sendora-db mfa-reset -source /path/to/fileshare.db -key-file /path/to/database.key -user <name>`, or set `mfa_policy` to `off` in the database. Audit events (when logging is on): `mfa_enrolled`, `mfa_removed`, `mfa_login_ok`, `mfa_failed`, `mfa_reset`, `mfa_policy_changed` — never codes, secrets or credential IDs. MFA endpoints have their own rate-limit bucket (30 requests per minute per client).


### Sessions and account changes

- A session lasts at most **30 days** after signing in. Refreshing keeps it alive within that time but never extends it beyond, so a stolen or forgotten browser cannot stay signed in forever.
- A refresh token works once. If an already used (rotated) token is presented again more than 60 seconds later, Sendora assumes it was copied, signs out **all** sessions of that account and records `session_reuse_detected` in the activity log. Within those 60 seconds (two tabs refreshing at once, a lost response) it is only refused. Changing your email, password or two-step settings keeps the original sign-in time, so it never extends the 30-day limit.
- Automatic sign-out after inactivity applies to every account, also those that sign in only through a provider.
- Accounts that sign in only through an external provider (no local password) lose their session at the next refresh when the provider is disabled or their link is removed.
- Such accounts cannot get a local password through *Forgot password*. Setting a password or changing the email address in their profile requires having signed in within the last **10 minutes**; otherwise Sendora asks them to sign in again.
- **Usernames** for new accounts may only contain the letters a–z and A–Z, digits, dot, underscore and hyphen (1–64 characters): no look-alike letters from other alphabets, no invisible characters and nothing that could pass for an email address on the sign-in form. Existing accounts keep their names.
- Registration reports a taken **username**, but a taken **email address** gets the same answer as a successful registration (no account is created and no mail is sent). This stops casual probing; a determined attacker can still find out by trying to sign in afterwards, see [Security and limitations](security.md). Only accounts created by public registration that never confirmed their address are removed after 48 hours; an existing account that changes its address is never removed. Failed sign-ins take the same time whether or not the account exists.

### Automatic sign-out after inactivity

Users are signed out after a period without activity. Default: 60 minutes. Admins change it under **Admin → Settings → Automatic sign-out** (`idle_logout_minutes`, whole minutes 0–10080, `0` = never).

- The browser tracks mouse, keyboard, touch and scroll activity, shared between tabs. One minute before the limit a dialog warns and offers **Stay signed in**. After the limit the user lands on the login page with a notice and returns to the same page after logging in.
- A running upload or download is never inactivity and is never interrupted. While transfers run, the session is silently refreshed. Downloads handed to the browser's own download manager cannot be observed; Sendora assumes they stay busy for `max(60 s, size / 2 MB/s)`, capped at 6 hours.
- Server side, a refresh token older than the limit plus a 20 minute grace period is rejected and deleted, so a stolen browser profile cannot stay signed in forever. This is an approximation based on refresh-token age, not an exact idle timer. Guest sessions are exempt.


### Account password policy

Administrators can set a policy for **local account passwords** (Settings → Account password policy): minimum length (1–72, default 8) and optional upper-case, lower-case, digit and symbol requirements. It is separate from the share password policy and is enforced by the server wherever a local password is set: first-run setup, public registration, password reset, profile changes (including a provider-created account adding a first local password), and administrator-created or edited users. Passwords are always limited to 72 bytes (bcrypt) and must be valid UTF-8. Existing accounts are not forced to change: their current password keeps working until it is next changed, and sign-in itself never checks the policy. The forms show the requirements live; if the stored policy is corrupt the server refuses password changes instead of accepting anything.


### Local password lockout and user storage quotas

Five consecutive failed local-password login attempts lock that account's local password login for **15 minutes**. Administrators can unlock it through user management. This is distinct from network-peer rate limiting: **OIDC/provider login and existing authenticated sessions are unaffected by the local-password lockout**. Password reset and account-management procedures should be tested before public operation.

Administrators configure **Default storage quota (GB)** in Settings. Users follow that setting unless an administrator selects a **personal override** in user management; changing the default immediately affects all users following it, including existing accounts. Profile and user-management storage values use decimal GB: **1 GB = 1,000,000,000 bytes**. Editors preserve byte precision (up to nine decimal places) and accept a decimal point or comma. New installations, including a fresh container volume, start with a global default of **10 GB**. An upgraded database keeps its previous 10-GiB allowance (**10.73741824 GB**) until an administrator changes the default; migration does not silently round or lower limits. Existing limits differing from the old default remain overrides. An old explicitly assigned limit equal to that default cannot be distinguished and becomes inherited; it can be made explicit again in user management. Setting zero means zero storage, not unlimited.

Usage includes stored share-file bytes **plus reserved upload-session bytes**, so concurrent/incomplete uploads cannot bypass the limit. Reverse-share guest uploads consume the **invitation owner's** quota, not a separate guest allowance. Lowering a limit does not delete existing files and does not invalidate already admitted uploads; it blocks new admissions that would exceed the effective limit. This storage quota does not bound bandwidth, encrypted backups, branding, or verified-download snapshots. Monitor actual filesystem capacity independently.


## Microsoft Entra ID: single-tenant setup

1. Set the canonical HTTPS `APP_URL` first.
2. In Microsoft Entra, register a **Web application** with **Accounts in this organizational directory only** (single tenant).
3. Create a client secret and retain its **value**, not merely its secret ID. Note the Directory (tenant) GUID and Application (client) ID.
4. In Sendora Admin → Login providers → Add provider, choose **Microsoft Entra ID**. Enter a display name, tenant GUID, client ID, and secret. Set **Enabled** and choose whether **Create accounts on first login** should be allowed, then save.
5. Copy the exact displayed callback URL into the Entra app registration's **Web redirect URIs**. Its shape is `https://files.example.com/api/auth/oidc/<provider-uuid>/callback`; the UUID belongs to the saved provider and is not the client or tenant ID.
6. Apply application assignment, MFA, and Conditional Access policies in Entra as required. Test actual sign-in and failure cases with your real tenant before public launch.

The integration uses authorization-code flow with **PKCE S256**, state, and nonce, not implicit flow. It requests `openid profile email`; Microsoft Graph permissions are not needed for this login flow. Tokens are checked for signature, issuer, audience, expiration, nonce, v2.0 claims, tenant, and immutable object identity.

### Entra limitations and account behavior

- Only the configured organizational tenant is accepted, including guest users if Entra allows them. `common`, `organizations`, `consumers`, personal-account tenant authorities, arbitrary hosts, and HTTP Entra issuers are rejected. The supported authority is `https://login.microsoftonline.com/<tenant-guid>/v2.0`; sovereign-cloud authorities are not implemented.
- Identity links use **tenant GUID + object GUID**, not mutable email or UPN. The Entra **email claim** is ignored. The Entra **user name** (`preferred_username`, normally the user principal name such as `jan@example.com`) becomes the account's email address when it looks like an address and no other account uses it: it is stored as **unverified** and is only shown and used for notifications. It is never used to link or find accounts, and unverified addresses cannot be used for a password reset. An empty email is filled this way at the next sign-in; an existing address is never overwritten. Users can supply/verify another address afterward.
- First-login provisioning creates an ordinary user, never an administrator. Its provider-specific registration switch is separate from public local signup. Disabling provisioning allows only already-linked identities to log in.
- Existing local accounts are **never automatically linked by matching email**, and an authenticated add/link-provider workflow is not yet implemented. Local password login remains available for local accounts; a newly provisioned provider-only account initially has no local password.
- Once users are linked, provider type, issuer/tenant, and client ID cannot be changed. Create another provider instead; secret rotation and disabling are supported. Deleting/recreating a provider is not a transparent identity migration.
- Sendora's own MFA does not apply to Entra sign-in (the tenant's MFA and Conditional Access are authoritative), and Sendora does not itself enforce Conditional Access or Entra app assignment. Logout ends the Sendora session, not Microsoft's global SSO session. Tenant-side revocation does not immediately revoke an already-issued Sendora session.
- Automated tests use a simulated OIDC provider. They do not replace validation of your real registration, redirect URI, tenant policy, or secret lifecycle.

Google, GitHub, and generic OIDC providers are also configured in Admin → Login providers, with their exact saved callback URL registered at the provider. Generic OIDC configuration can accept HTTP issuers; use HTTPS for real deployments and treat provider configuration as privileged/trusted. Matching verified email may populate a **new** non-Entra account when explicitly enabled, but it does not automatically link an existing account.
