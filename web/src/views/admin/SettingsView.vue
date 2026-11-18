<script setup>
import { ref, onMounted } from 'vue'
import { api } from '../../api'
import { loadBoot } from '../../state'
import { languages, tr } from '../../i18n'
import { t } from '../../i18n'
import { gbToBytes, bytesToGB } from '../../quota.mjs'
const s = ref(null), msg = ref(''), err = ref(''), quotaGB = ref(''), managed = ref([])
// Settings from the server configuration (ConfigMap) are shown read-only.
const m = k => managed.value.includes(k)
async function load() {
  const settings = await api('/settings')
  quotaGB.value = bytesToGB(settings.default_storage_quota_bytes)
  managed.value = settings.managed_settings || []
  s.value = settings
}
onMounted(async () => { try { await load() } catch (e) { err.value = t(e.message) } })
const flags = [['allow_reverse_shares', tr('Allow reverse shares (guest invites)')], ['allow_self_registration', tr('Allow self registration')], ['show_language_selector', tr('Show language selector')], ['auto_clean_files', tr('Delete files of expired shares')], ['backups_enabled', tr('Daily database backups')], ['allow_preview', tr('Allow preview of images, video and audio')]]
const nums = [['default_expiry_time', tr('Default expiry (days)')], ['max_expiry_time', tr('Max expiry (days, 0 = none)')], ['max_share_size', tr('Max share size (MB, 0 = none)')], ['clean_files_days', tr('Delete expired files after (days)')], ['expiry_warning_days', tr('Warn owners N days before expiry (0 = off)')], ['backup_keep', tr('Backups to keep')]]
const accountFlags = [['account_password_require_upper', tr('Require an uppercase letter')], ['account_password_require_lower', tr('Require a lowercase letter')], ['account_password_require_digit', tr('Require a digit')], ['account_password_require_symbol', tr('Require a symbol')]]
const policyFlags = [['require_share_password', tr('Require a password on every share')], ['share_password_require_upper', tr('Require an uppercase letter')], ['share_password_require_lower', tr('Require a lowercase letter')], ['share_password_require_digit', tr('Require a digit')], ['share_password_require_symbol', tr('Require a symbol')]]
async function save() {
  msg.value = err.value = ''
  try {
    const body = { ...s.value, default_storage_quota_bytes: String(gbToBytes(quotaGB.value)) }
    await api('/settings', { method: 'PUT', body }); await loadBoot(); await load(); msg.value = t('Saved')
    return true
  } catch (e) { err.value = t(e.message); return false }
}
async function test() { try { if (!await save()) return; await api('/email-templates/test', { method: 'POST' }); msg.value = t('Test email sent') } catch (e) { err.value = e.message } }
</script>
<template>
  <p v-if="!s && err" class="err">{{ err }}</p>
  <form v-if="s" class="card" @submit.prevent="save">
    <h1>{{ t('Settings') }}</h1>
    <p v-if="managed.length" class="muted">{{ t('Settings set in the server configuration (environment or Kubernetes ConfigMap) are read-only here: {n}.', { n: managed.join(', ') }) }}</p>
    <div class="row">
      <div><label>{{ t('Application name') }}</label><input :disabled="m('application_name')" v-model="s.application_name" /></div>
      <div><label>{{ t('Public URL') }}</label><input :disabled="m('application_url')" v-model="s.application_url" placeholder="https://files.example.com" /></div>
      <div><label>{{ t('Default language') }}</label><select :disabled="m('default_language')" v-model="s.default_language"><option v-for="(n, k) in languages" :key="k" :value="k">{{ n }}</option></select></div>
    </div>
    <label>{{ t('Default storage quota (GB)') }}</label><input :disabled="m('default_storage_quota_bytes')" v-model="quotaGB" type="text" inputmode="decimal" pattern="[0-9]+([.,][0-9]{1,9})?" maxlength="64" required />
    <p class="muted">{{ t('Changes apply to all users following the default. Personal overrides stay unchanged.') }}</p>
    <div class="row"><div v-for="[k, l] in nums" :key="k"><label>{{ t(l) }}</label><input :disabled="m(k)" v-model="s[k]" type="number" min="0" /></div></div>
    <label v-for="[k, l] in flags" :key="k" class="check"><input :disabled="m(k)" type="checkbox" :checked="s[k] === 'true'" @change="s[k] = String($event.target.checked)" /> {{ t(l) }}</label>
    <p class="muted">{{ t('Public registration always requires email verification and working SMTP.') }}</p>
    <p v-if="s.allow_self_registration === 'true' && !s.smtp_host" class="err">{{ t('Self registration is on but outgoing email (SMTP) is not configured, so the Create account option stays hidden.') }}</p>
    <label>{{ t('Allowed email domains for registration (one per line, empty = any)') }}</label>
    <textarea :disabled="m('registration_email_domains')" v-model="s.registration_email_domains" rows="3" placeholder="example.com" spellcheck="false" />
    <p class="muted">{{ t('Exact domains only; subdomains are not included. Applies to public registration, not to accounts created by administrators or external providers.') }}</p>
    <h2 style="margin-top:20px">{{ t('Share password policy') }}</h2>
    <label v-for="[k, l] in policyFlags" :key="k" class="check"><input :disabled="m(k)" type="checkbox" :checked="s[k] === 'true'" @change="s[k] = String($event.target.checked)" /> {{ t(l) }}</label>
    <label>{{ t('Minimum password length (1-72)') }}</label><input :disabled="m('share_password_min_length')" v-model="s.share_password_min_length" type="number" min="1" max="72" />
    <p class="muted">{{ t('Applies to passwords set on new shares. Existing shares keep their passwords.') }}</p>
    <h2 style="margin-top:20px">{{ t('Account password policy') }}</h2>
    <label v-for="[k, l] in accountFlags" :key="k" class="check"><input :disabled="m(k)" type="checkbox" :checked="s[k] === 'true'" @change="s[k] = String($event.target.checked)" /> {{ t(l) }}</label>
    <label>{{ t('Minimum password length (1-72)') }}</label><input :disabled="m('account_password_min_length')" v-model="s.account_password_min_length" type="number" min="1" max="72" />
    <p class="muted">{{ t('Applies to local accounts when a password is set or changed (setup, registration, reset, profile, administrators). Existing passwords keep working until they are next changed.') }}</p>
    <h2 style="margin-top:20px">{{ t('Multi-factor authentication') }}</h2>
    <label for="mfa-policy">{{ t('Two-step verification policy') }}</label>
    <select :disabled="m('mfa_policy')" id="mfa-policy" v-model="s.mfa_policy"><option value="off">{{ t('Off') }}</option><option value="optional">{{ t('Optional (users choose)') }}</option><option value="admins">{{ t('Required for administrators') }}</option><option value="all">{{ t('Required for everyone') }}</option></select>
    <label class="check"><input :disabled="m('mfa_totp_enabled')" type="checkbox" :checked="s.mfa_totp_enabled !== 'false'" @change="s.mfa_totp_enabled = String($event.target.checked)" /> {{ t('Allow authenticator apps (TOTP)') }}</label>
    <label class="check"><input :disabled="m('mfa_webauthn_enabled')" type="checkbox" :checked="s.mfa_webauthn_enabled !== 'false'" @change="s.mfa_webauthn_enabled = String($event.target.checked)" /> {{ t('Allow passkeys and security keys') }}</label>
    <p class="muted">{{ t('Applies to local accounts. Sign-ins through an external provider (Entra, OpenID Connect, Google, GitHub) use that provider\'s own multi-factor checks. Passkeys need https with a fixed application URL, or localhost. To require it for administrators or everyone, first set up two-step verification for your own account (Profile), then choose the policy. Users can be set to required or exempt individually under Users. Turning the policy off disables all two-step prompts.') }}</p>
    <h2 style="margin-top:20px">{{ t('Logging') }}</h2>
    <label>{{ t('Audit log retention (days, 1-365)') }}</label><input :disabled="m('audit_retention_days')" v-model="s.audit_retention_days" type="number" min="1" max="365" />
    <label class="check"><input :disabled="m('audit_log_ip')" type="checkbox" :checked="s.audit_log_ip === 'true'" @change="s.audit_log_ip = String($event.target.checked)" /> {{ t('Store the IP address of each audit log entry') }}</label>
    <p class="muted">{{ t('The audit log is always on and tamper-evident; it cannot be switched off or edited. Entries older than the retention period are deleted automatically. Every change to these settings is itself recorded.') }}</p>
    <h2 style="margin-top:20px">{{ t('Automatic sign-out') }}</h2>
    <label>{{ t('Sign out after inactivity (minutes, 0 = never)') }}</label><input :disabled="m('idle_logout_minutes')" v-model="s.idle_logout_minutes" type="number" min="0" max="10080" />
    <p class="muted">{{ t('Users are signed out after this many minutes without activity, with a warning one minute before. Running uploads and downloads never count as inactive and are not interrupted. 0 turns this off.') }}</p>
    <h2 style="margin-top:20px">{{ t('Local login') }}</h2>
    <label class="check"><input :disabled="m('local_login_enabled')" type="checkbox" :checked="s.local_login_enabled !== 'false'" @change="s.local_login_enabled = String($event.target.checked)" /> {{ t('Allow signing in with a local username and password') }}</label>
    <p class="muted">{{ t('Turn off to allow external login providers only. Only possible when you are linked to an enabled provider yourself. If the provider stops working, restart the server with SENDORA_FORCE_LOCAL_LOGIN=true to sign in locally again.') }}</p>
    <h2 style="margin-top:20px">SMTP</h2>
    <div class="row">
      <div><label>{{ t('Host') }}</label><input :disabled="m('smtp_host')" v-model="s.smtp_host" /></div>
      <div><label>{{ t('Port') }}</label><input :disabled="m('smtp_port')" v-model="s.smtp_port" /></div>
      <div><label>{{ t('Security') }}</label><select :disabled="m('smtp_tls')" v-model="s.smtp_tls"><option value="starttls">STARTTLS</option><option value="tls">TLS</option><option value="none">{{ t('None') }}</option></select></div>
    </div>
    <div class="row">
      <div><label>{{ t('Username') }}</label><input :disabled="m('smtp_user')" v-model="s.smtp_user" autocomplete="off" /></div>
      <div><label>{{ t('Password') }}</label><input :disabled="m('smtp_pass')" v-model="s.smtp_pass" type="password" autocomplete="new-password" :placeholder="s.smtp_pass_set ? '••••••••' : ''" /></div>
      <div><label>{{ t('From address') }}</label><input :disabled="m('smtp_from')" v-model="s.smtp_from" placeholder="files@example.com" /></div>
    </div>
    <div class="row">
      <div><label>{{ t('Reply-To address (optional)') }}</label><input :disabled="m('smtp_reply_to')" v-model="s.smtp_reply_to" placeholder="support@example.com" /></div>
    </div>
    <p class="muted">{{ t('Replies to Sendora emails go to the Reply-To address instead of the From address. Leave empty to reply to the sender.') }}</p>
    <p v-if="msg" class="ok">{{ msg }}</p><p v-if="err" class="err">{{ err }}</p>
    <div class="actions"><button>{{ t('Save') }}</button><button type="button" class="secondary" @click="test">{{ t('Send test email to me') }}</button></div>
  </form>
</template>
