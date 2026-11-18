<script setup>
import { ref, onMounted } from 'vue'
import { api, session, fmtDate } from '../../api'
import { bytesToGB, fmtGBShort, quotaPayload } from '../../quota.mjs'
import { t, tr } from '../../i18n'
import { app } from '../../state'
import { accountPolicy, passwordProblems } from '../../sharepolicy.mjs'
import PasswordHints from '../../components/PasswordHints.vue'
const users = ref([]), err = ref(''), edit = ref(null), defaultQuotaGB = ref(null), info = ref(''), smtpReady = ref(false), busy = ref(false)
// Where an account comes from: local password and/or linked sign-in providers.
const SOURCES = { entra: 'Microsoft Entra ID', oidc: 'OpenID Connect', google: 'Google', github: 'GitHub' }
const sourceLabel = u => (u.sources || []).map(s => s === 'local' ? t('Local') : (SOURCES[s] || s)).join(' + ') || '—'
const mfaOn = () => app.boot?.mfa_policy && app.boot.mfa_policy !== 'off'
const blank = () => ({ id: 0, username: '', name: '', email: '', password: '', admin: false, auditor: false, must_change_password: true, mfa_policy: 'inherit', send_welcome: true, quota_inherits_default: true, quota_gb: defaultQuotaGB.value })
function open(u) { edit.value = { ...u, password: '', mfa_policy: u.mfa_policy || 'inherit', quota_gb: bytesToGB(u.quota_bytes) } }
function inheritanceChanged() { if (edit.value.quota_inherits_default) edit.value.quota_gb = defaultQuotaGB.value }
async function load() {
  try {
    const [list, settings] = await Promise.all([api('/users'), api('/settings')])
    defaultQuotaGB.value = bytesToGB(settings.default_storage_quota_bytes)
    smtpReady.value = !!String(settings.smtp_host || '').trim()
    users.value = list
  } catch (e) { err.value = t(e.message) }
}
onMounted(load)
async function unlock(u) {
  if (!confirm(t('Unlock local login for {n}?', { n: u.username }))) return
  try { await api(`/users/${u.id}/unlock-login`, { method: 'POST' }); await load() }
  catch (e) { err.value = t(e.message) }
}
async function save() {
  err.value = ''
  const u = edit.value
  if (u.password || !u.id) {
    const problems = passwordProblems(u.password, accountPolicy(app.boot))
    if (problems.length) { err.value = t('Needs:') + ' ' + problems.map(p => t(p.key, { n: p.n })).join(', '); return }
  }
  try {
    const body = { username: u.username, name: u.name, email: u.email, password: u.password, admin: u.admin, auditor: !!u.auditor, must_change_password: u.must_change_password, ...quotaPayload(u.quota_inherits_default, u.quota_gb) }
    if (u.id && mfaOn()) body.mfa_policy = u.mfa_policy
    err.value = info.value = ''
    if (u.id) await api('/users/' + u.id, { method: 'PUT', body })
    else {
      body.send_welcome = welcomeAvailable(u) && u.send_welcome
      const created = await api('/users', { method: 'POST', body })
      info.value = welcomeMessage(created, u.email)
    }
    edit.value = null; await load()
  } catch (e) { err.value = t(e.message) }
}
// The welcome email needs a working SMTP server and an address to send to.
const welcomeAvailable = u => smtpReady.value && !!String(u.email || '').trim()
function welcomeMessage(created, email) {
  if (created.welcome_email === 'sent') return t('User created. Welcome email sent to {e}.', { e: email })
  if (created.welcome_email) return t('User created, but no welcome email was sent: {note}', { note: t(created.welcome_note || tr('unknown reason')) })
  return t('User created.')
}
async function resendWelcome(u) {
  err.value = info.value = ''; busy.value = true
  try { await api(`/users/${u.id}/welcome`, { method: 'POST' }); info.value = t('Welcome email sent to {e}.', { e: u.email }) }
  catch (e) { err.value = t(e.message) } finally { busy.value = false }
}
async function resetMfa(u) {
  if (!confirm(t('Remove all two-step verification methods and sessions for {n}?', { n: u.username }))) return
  err.value = info.value = ''
  try { await api(`/users/${u.id}/mfa/reset`, { method: 'POST' }); info.value = t('Two-step verification reset for {n}.', { n: u.username }); await load() }
  catch (e) { err.value = t(e.message) }
}
async function del(u) { if (confirm(t('Delete user {n} and all their shares?', { n: u.username }))) { try { await api('/users/' + u.id, { method: 'DELETE' }); await load() } catch (e) { err.value = t(e.message) } } }
async function force(u) { await api(`/users/${u.id}/force-reset-password`, { method: 'POST' }); err.value = t('Password change required at next login.') }
</script>
<template>
  <div class="card">
    <div class="actions" style="margin:0 0 12px"><h1 style="margin:0">{{ t('Users') }}</h1><button class="small" :disabled="defaultQuotaGB === null" @click="edit = blank()">+ {{ t('New user') }}</button></div>
    <p v-if="err" class="err">{{ err }}</p>
    <p v-if="info" class="ok" role="status">{{ info }}</p>
    <form v-if="edit" class="card" @submit.prevent="save">
      <div class="row">
        <div><label>{{ t('Username') }}</label><input v-model="edit.username" :disabled="!!edit.id" required /></div>
        <div v-if="edit.id"><label>{{ t('Created') }}</label><input :value="fmtDate(edit.created_at)" disabled /></div>
        <div><label>{{ t('Name') }}</label><input v-model="edit.name" /></div>
        <div><label>{{ t('Email') }}</label><input v-model="edit.email" type="email" /></div>
        <div><label>{{ t('Password') }}</label><input v-model="edit.password" type="password" autocomplete="new-password" :required="!edit.id" :placeholder="edit.id ? t('unchanged') : ''" />
</div>
      </div>
      <PasswordHints :password="edit.password" :optional="!!edit.id" />
      <label class="check"><input type="checkbox" v-model="edit.quota_inherits_default" @change="inheritanceChanged" /> {{ t('Use global storage default') }} ({{ defaultQuotaGB }} GB)</label>
      <label>{{ t('Storage quota (GB)') }}</label><input v-model="edit.quota_gb" :disabled="edit.quota_inherits_default" type="text" inputmode="decimal" pattern="[0-9]+([.,][0-9]{1,9})?" maxlength="64" required />
      <label class="check"><input type="checkbox" v-model="edit.admin" :disabled="edit.id === session.user.id" @change="edit.admin && (edit.auditor = false)" /> {{ t('Admin') }}</label>
      <label class="check"><input type="checkbox" v-model="edit.auditor" :disabled="edit.admin" /> {{ t('Auditor (read-only access to the audit log)') }}</label>
      <template v-if="edit.id && mfaOn()">
        <label for="user-mfa">{{ t('Two-step verification') }}</label>
        <select id="user-mfa" v-model="edit.mfa_policy"><option value="inherit">{{ t('Follow global setting') }}</option><option value="required">{{ t('Required for this user') }}</option><option value="exempt">{{ t('Exempt (not required)') }}</option></select>
        <p class="muted">{{ t('Exempt skips the requirement only; the user can still turn two-step verification on. Sign-ins through an external provider use that provider\'s own checks.') }}</p>
      </template>
      <label v-if="!edit.id" class="check"><input type="checkbox" v-model="edit.must_change_password" /> {{ t('Require password change at first login') }}</label>
      <template v-if="!edit.id">
        <label class="check"><input type="checkbox" v-model="edit.send_welcome" :disabled="!welcomeAvailable(edit)" /> {{ t('Send welcome email') }}</label>
        <p class="muted">{{ !smtpReady ? t('Set up outgoing email (SMTP) in Settings to send a welcome email.') : !String(edit.email || '').trim() ? t('Enter an email address to send a welcome email.') : t('The email contains the username, a sign-in link and a one-time link to choose a password. The password you typed is never emailed.') }}</p>
      </template>
      <div v-else-if="edit.email" class="actions" style="margin:8px 0"><button type="button" class="secondary small" :disabled="busy || !smtpReady" :title="smtpReady ? '' : t('Set up outgoing email (SMTP) in Settings to send a welcome email.')" @click="resendWelcome(edit)">{{ t('Resend welcome email') }}</button></div>
      <div class="actions"><button>{{ t('Save') }}</button><button type="button" class="secondary" @click="edit = null">{{ t('Cancel') }}</button></div>
    </form>
    <div class="table-wrap"><table>
      <tr><th>{{ t('Username') }}</th><th>{{ t('Source') }}</th><th>{{ t('Name') }}</th><th>{{ t('Email') }}</th><th>{{ t('Storage quota') }}</th><th /></tr>
      <tr v-for="u in users" :key="u.id"><td>{{ u.username }} <span v-if="u.admin" class="badge on">{{ t('Admin') }}</span> <span v-if="u.auditor" class="badge">{{ t('Auditor') }}</span> <span v-if="mfaOn() && u.mfa_enabled" class="badge on" :title="t('Two-step verification is on')">MFA</span> <span v-if="mfaOn() && u.mfa_policy === 'required'" class="badge" :title="t('Required for this user')">{{ t('MFA required') }}</span> <span v-if="mfaOn() && u.mfa_policy === 'exempt'" class="badge off" :title="t('Exempt (not required)')">{{ t('MFA exempt') }}</span></td><td>{{ sourceLabel(u) }}</td><td>{{ u.name }}</td><td>{{ u.email }}</td>
        <td>{{ fmtGBShort(u.quota_bytes) }}<br /><span class="muted">{{ t(u.quota_inherits_default ? 'Global default' : 'Personal override') }}<br />{{ t('Stored files') }}: {{ fmtGBShort(u.storage_used_bytes) }}<br />{{ t('Reserved uploads') }}: {{ fmtGBShort(u.storage_reserved_bytes) }}</span>
          <p v-if="u.login_locked_until * 1000 > Date.now()" class="err">{{ t('Local login locked until') }} {{ fmtDate(new Date(u.login_locked_until * 1000).toISOString()) }}</p></td>
        <td class="actions" style="margin:0"><button class="small secondary" @click="open(u)">{{ t('Edit') }}</button>
          <button v-if="u.login_locked_until * 1000 > Date.now()" class="small secondary" @click="unlock(u)">{{ t('Unlock local login') }}</button>
          <button class="small secondary" @click="force(u)">{{ t('Force reset') }}</button>
          <button v-if="mfaOn() && u.mfa_enabled" class="small secondary" @click="resetMfa(u)">{{ t('Reset MFA') }}</button>
          <button class="small danger" :disabled="u.id === session.user.id" @click="del(u)">{{ t('Delete') }}</button></td></tr>
    </table></div>
  </div>
</template>
