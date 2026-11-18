<script setup>
// Security-related settings (two-step verification, account password policy, audit log),
// shown on the Security tab. Only these keys are sent when saving.
import { ref, onMounted } from 'vue'
import { api } from '../../api'
import { loadBoot } from '../../state'
import { t, tr } from '../../i18n'

const KEYS = ['mfa_policy', 'mfa_reminder_days', 'mfa_totp_enabled', 'mfa_webauthn_enabled',
  'account_password_require_upper', 'account_password_require_lower', 'account_password_require_digit', 'account_password_require_symbol', 'account_password_min_length',
  'audit_retention_days', 'audit_log_ip']
const s = ref(null), managed = ref([]), msg = ref(''), err = ref(''), busy = ref(false)
const m = k => managed.value.includes(k)
const accountFlags = [['account_password_require_upper', tr('Require an uppercase letter')], ['account_password_require_lower', tr('Require a lowercase letter')], ['account_password_require_digit', tr('Require a digit')], ['account_password_require_symbol', tr('Require a symbol')]]

async function load() {
  const settings = await api('/settings')
  managed.value = settings.managed_settings || []
  s.value = Object.fromEntries(KEYS.map(k => [k, settings[k]]))
}
onMounted(async () => { try { await load() } catch (e) { err.value = e.message } })

async function save() {
  msg.value = err.value = ''; busy.value = true
  try {
    // Configured (read-only) settings are not sent, so they never cause an error.
    const body = Object.fromEntries(Object.entries(s.value).filter(([k]) => !m(k)))
    await api('/settings', { method: 'PUT', body })
    await loadBoot(); await load(); msg.value = t('Saved')
  } catch (e) { err.value = e.message } finally { busy.value = false }
}
</script>
<template>
  <p v-if="!s && err" class="err" role="alert">{{ err }}</p>
  <form v-if="s" class="card" @submit.prevent="save">
    <h1>{{ t('Security settings') }}</h1>
    <p v-if="KEYS.some(m)" class="muted">{{ t('Settings set in the server configuration (environment or Kubernetes ConfigMap) are read-only here: {n}.', { n: KEYS.filter(m).join(', ') }) }}</p>

    <h2 style="margin-top:8px">{{ t('Multi-factor authentication') }}</h2>
    <label for="mfa-policy">{{ t('Two-step verification policy') }}</label>
    <select :disabled="m('mfa_policy')" id="mfa-policy" v-model="s.mfa_policy"><option value="off">{{ t('Off') }}</option><option value="optional">{{ t('Optional (users choose)') }}</option><option value="admins">{{ t('Required for administrators') }}</option><option value="all">{{ t('Required for everyone') }}</option></select>
    <label for="mfa-reminder">{{ t('Remind users who skipped two-step setup again after (days, 0 = at every sign-in)') }}</label><input id="mfa-reminder" :disabled="m('mfa_reminder_days')" v-model="s.mfa_reminder_days" type="number" min="0" max="365" />
    <p class="muted">{{ t('Accounts with a local password and no second factor see a reminder after signing in. Dismissing it hides it for this many days in that browser.') }}</p>
    <label class="check"><input :disabled="m('mfa_totp_enabled')" type="checkbox" :checked="s.mfa_totp_enabled !== 'false'" @change="s.mfa_totp_enabled = String($event.target.checked)" /> {{ t('Allow authenticator apps (TOTP)') }}</label>
    <label class="check"><input :disabled="m('mfa_webauthn_enabled')" type="checkbox" :checked="s.mfa_webauthn_enabled !== 'false'" @change="s.mfa_webauthn_enabled = String($event.target.checked)" /> {{ t('Allow passkeys and security keys') }}</label>
    <p class="muted">{{ t('Applies to local accounts. Sign-ins through an external provider (Entra, OpenID Connect, Google, GitHub) use that provider\'s own multi-factor checks. Passkeys need https with a fixed application URL, or localhost. To require it for administrators or everyone, first set up two-step verification for your own account (Profile), then choose the policy. Users can be set to required or exempt individually under Users. Turning the policy off disables all two-step prompts.') }}</p>

    <h2 style="margin-top:20px">{{ t('Account password policy') }}</h2>
    <label v-for="[k, l] in accountFlags" :key="k" class="check"><input :disabled="m(k)" type="checkbox" :checked="s[k] === 'true'" @change="s[k] = String($event.target.checked)" /> {{ t(l) }}</label>
    <label for="acct-min">{{ t('Minimum password length (1-72)') }}</label><input id="acct-min" :disabled="m('account_password_min_length')" v-model="s.account_password_min_length" type="number" min="1" max="72" />
    <p class="muted">{{ t('Applies to local accounts when a password is set or changed (setup, registration, reset, profile, administrators). Existing passwords keep working until they are next changed.') }}</p>

    <h2 style="margin-top:20px">{{ t('Logging') }}</h2>
    <label for="audit-retention">{{ t('Audit log retention (days, 90-365)') }}</label><input id="audit-retention" :disabled="m('audit_retention_days')" v-model="s.audit_retention_days" type="number" min="90" max="365" />
    <label class="check"><input :disabled="m('audit_log_ip')" type="checkbox" :checked="s.audit_log_ip === 'true'" @change="s.audit_log_ip = String($event.target.checked)" /> {{ t('Store the IP address of each audit log entry') }}</label>
    <p class="muted">{{ t('The audit log is always on and tamper-evident; it cannot be switched off or edited. Entries older than the retention period are deleted automatically. Every change to these settings is itself recorded.') }}</p>

    <p v-if="msg" class="ok" role="status">{{ msg }}</p><p v-if="err" class="err" role="alert">{{ err }}</p>
    <div class="actions"><button :disabled="busy">{{ t('Save') }}</button></div>
  </form>
</template>
