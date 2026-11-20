<script setup>
// Platform console (multi-tenant mode): served only on admin.<base domain>.
// Platform administrators manage tenants, never tenant content.
import { ref, reactive, computed, onMounted } from 'vue'
import QrCode from '../components/QrCode.vue'
import { t, tr, lang, setLang, builtinLanguages } from '../i18n'
import { gbToBytes, bytesToGB } from '../quota.mjs'
import { pconsole, slugProblem, fmtSize, quotaPercent, parseStorageConfig } from '../platform-console.mjs'

const boot = ref(null), stage = ref('loading'), me = ref(null), err = ref(''), notice = ref(''), busy = ref(false)
const tab = ref('tenants')
const f = reactive({ setup_code: '', username: '', email: '', password: '', code: '' })
const enroll = ref(null)

async function load() {
  err.value = ''
  boot.value = await pconsole('/bootstrap').catch(() => null)
  if (boot.value?.setup_needed) { stage.value = 'setup'; return }
  try {
    const d = await pconsole('/me')
    me.value = d.admin; stage.value = d.stage
    if (d.stage === 'full') await loadTenants()
  } catch { stage.value = 'login' }
}
onMounted(load)

async function run(fn) {
  err.value = ''; notice.value = ''; busy.value = true
  try { await fn() } catch (e) { err.value = e.message } finally { busy.value = false }
}
const doSetup = () => run(async () => {
  const d = await pconsole('/setup', { method: 'POST', body: { setup_code: f.setup_code, username: f.username, email: f.email, password: f.password } })
  f.password = ''; f.setup_code = ''; stage.value = d.stage
})
const doLogin = () => run(async () => {
  const d = await pconsole('/login', { method: 'POST', body: { username: f.username, password: f.password } })
  f.password = ''; stage.value = d.stage
})
const doTotp = () => run(async () => {
  await pconsole('/login/totp', { method: 'POST', body: { code: f.code } })
  f.code = ''; await load()
})
const beginEnroll = () => run(async () => { enroll.value = await pconsole('/mfa/begin', { method: 'POST' }) })
const confirmEnroll = () => run(async () => {
  await pconsole('/mfa/confirm', { method: 'POST', body: { code: f.code } })
  f.code = ''; enroll.value = null; await load()
})
const logout = () => run(async () => { await pconsole('/logout', { method: 'POST' }); me.value = null; stage.value = 'login' })

// ---------- tenants ----------
const tenants = ref([]), q = ref(''), sel = ref(null), created = ref(null)
const nt = reactive({ slug: '', name: '', admin_email: '', admin_username: '', max_users: '0', max_storage_gb: '0' })
const slugHint = computed(() => nt.slug ? t(slugProblem(nt.slug)) : '')
async function loadTenants() { tenants.value = (await pconsole('/tenants?q=' + encodeURIComponent(q.value))).tenants }
const search = () => run(loadTenants)
const createTenant = () => run(async () => {
  const d = await pconsole('/tenants', { method: 'POST', body: {
    slug: nt.slug.trim(), name: nt.name, admin_email: nt.admin_email, admin_username: nt.admin_username,
    max_users: Number(nt.max_users) || 0, max_storage_bytes: gbToBytes(String(nt.max_storage_gb || '0')) } })
  created.value = d
  Object.assign(nt, { slug: '', name: '', admin_email: '', admin_username: '', max_users: '0', max_storage_gb: '0' })
  await loadTenants()
})
const quota = reactive({ max_users: '0', max_storage_gb: '0' })
const storage = reactive({ mode: 'platform', prefix: '', config: '{}' })
const domainInput = ref(''), del = reactive({ confirm: '', code: '' })
async function openTenant(id) {
  await run(async () => {
    const d = await pconsole('/tenants/' + id)
    sel.value = d
    quota.max_users = String(d.tenant.max_users)
    quota.max_storage_gb = bytesToGB(d.tenant.max_storage_bytes)
    storage.mode = d.storage?.mode || 'platform'
    storage.prefix = d.storage?.prefix || ''
    storage.config = JSON.stringify(d.storage?.config || {}, null, 2)
    del.confirm = ''; del.code = ''; created.value = null
  })
}
const refreshSel = () => openTenant(sel.value.tenant.id)
const tid = () => sel.value.tenant.id
const setStatus = s => run(async () => { await pconsole(`/tenants/${tid()}/${s}`, { method: 'POST' }); await refreshSel(); await loadTenants() })
const saveQuota = () => run(async () => {
  await pconsole(`/tenants/${tid()}/quotas`, { method: 'PUT', body: { max_users: Number(quota.max_users) || 0, max_storage_bytes: gbToBytes(String(quota.max_storage_gb || '0')) } })
  notice.value = t('Saved'); await loadTenants()
})
const addDomain = () => run(async () => { await pconsole(`/tenants/${tid()}/domains`, { method: 'POST', body: { domain: domainInput.value } }); domainInput.value = ''; await refreshSel() })
const verifyDomain = d => run(async () => { await pconsole(`/tenants/${tid()}/domains/${encodeURIComponent(d)}/verify`, { method: 'POST' }); await refreshSel() })
const removeDomain = d => run(async () => { await pconsole(`/tenants/${tid()}/domains/${encodeURIComponent(d)}`, { method: 'DELETE' }); await refreshSel() })
const makePrimary = d => run(async () => { await pconsole(`/tenants/${tid()}/primary-domain`, { method: 'PUT', body: { domain: d } }); await refreshSel() })
const saveStorage = () => run(async () => {
  await pconsole(`/tenants/${tid()}/storage`, { method: 'PUT', body: { mode: storage.mode, prefix: storage.prefix, config: parseStorageConfig(storage.config) } })
  notice.value = t('Saved')
})
const deleteTenant = () => run(async () => {
  await pconsole(`/tenants/${tid()}/delete`, { method: 'POST', body: { confirm: del.confirm, code: del.code } })
  sel.value = null; notice.value = t('Tenant deleted.'); await loadTenants()
})

// ---------- settings, audit, administrators ----------
const settings = ref(null), managed = ref([]), smtpPass = ref('')
const settingKeys = [
  ['tenant_idle_minutes', tr('Close idle tenants after (minutes)')], ['max_open_tenants', tr('Maximum open tenants')],
  ['tenant_maintenance_hours', tr('Maintenance of idle tenants every (hours)')], ['default_max_users', tr('Default user limit for new tenants (0 = unlimited)')],
  ['default_max_storage_bytes', tr('Default storage limit for new tenants in bytes (0 = unlimited)')], ['platform_audit_retention_days', tr('Platform audit log retention (days)')],
  ['platform_smtp_host', tr('Default SMTP server')], ['platform_smtp_port', tr('SMTP port')], ['platform_smtp_user', tr('SMTP username')],
  ['platform_smtp_from', tr('Sender address')], ['platform_smtp_tls', tr('SMTP security (none, tls, starttls)')]]
async function loadSettings() { const d = await pconsole('/settings'); settings.value = d.settings; managed.value = d.managed }
const saveSettings = () => run(async () => {
  const body = {}
  for (const [k] of settingKeys) if (!managed.value.includes(k)) body[k] = String(settings.value[k] ?? '')
  if (smtpPass.value && !managed.value.includes('platform_smtp_pass')) body.platform_smtp_pass = smtpPass.value
  await pconsole('/settings', { method: 'PUT', body }); smtpPass.value = ''; notice.value = t('Saved'); await loadSettings()
})
const audit = ref([]), auditStatus = ref(null)
async function loadAudit() { audit.value = (await pconsole('/audit')).records; auditStatus.value = await pconsole('/audit/status') }
const admins = ref([]), na = reactive({ username: '', email: '', password: '', code: '' }), adminCode = ref('')
async function loadAdmins() { admins.value = (await pconsole('/admins')).admins }
const createAdmin = () => run(async () => { await pconsole('/admins', { method: 'POST', body: { ...na } }); Object.assign(na, { username: '', email: '', password: '', code: '' }); await loadAdmins() })
const deleteAdmin = a => run(async () => { await pconsole(`/admins/${a.id}/delete`, { method: 'POST', body: { code: adminCode.value } }); adminCode.value = ''; await loadAdmins() })
const resetAdminMfa = a => run(async () => { await pconsole(`/admins/${a.id}/mfa-reset`, { method: 'POST', body: { code: adminCode.value } }); adminCode.value = ''; notice.value = t('Authenticator removed; they set up a new one at their next sign-in.') })
function show(name) {
  tab.value = name; sel.value = null; err.value = ''; notice.value = ''
  run(name === 'tenants' ? loadTenants : name === 'settings' ? loadSettings : name === 'audit' ? loadAudit : loadAdmins)
}
const statusLabel = s => (s === 'active' ? t('Active') : s === 'suspended' ? t('Suspended') : s)
const fmtTime = v => (v ? new Date(v).toLocaleString() : '—')
</script>

<template>
  <div class="shell">
    <nav class="nav">
      <span class="brand"><img src="/logo.svg" alt="" class="logo" />{{ t('Sendora platform') }}</span>
      <div class="row" style="flex:0 1 auto;align-items:center">
        <select :value="lang" :aria-label="t('Language')" @change="setLang($event.target.value)">
          <option v-for="(n, c) in builtinLanguages" :key="c" :value="c">{{ n }}</option>
        </select>
        <button v-if="me" class="secondary small" @click="logout">{{ t('Sign out') }}</button>
      </div>
    </nav>
    <main style="width:calc(100% - 32px);max-width:1040px;margin:24px auto">
      <p v-if="stage === 'loading'" class="muted">…</p>

      <form v-else-if="stage === 'setup'" class="card narrow" @submit.prevent="doSetup">
        <h1>{{ t('Set up the platform console') }}</h1>
        <p class="muted">{{ t('Create the first platform administrator. The setup code is printed in the server log.') }}</p>
        <label for="p-code">{{ t('Setup code') }}</label><input id="p-code" v-model="f.setup_code" autocomplete="off" required />
        <label for="p-user">{{ t('Username') }}</label><input id="p-user" v-model="f.username" autocomplete="username" required />
        <label for="p-mail">{{ t('Email') }}</label><input id="p-mail" v-model="f.email" type="email" />
        <label for="p-pass">{{ t('Password') }}</label><input id="p-pass" v-model="f.password" type="password" autocomplete="new-password" minlength="12" required />
        <p class="muted">{{ t('At least 12 characters; an authenticator app is required next.') }}</p>
        <p v-if="err" class="err" role="alert">{{ err }}</p>
        <div class="actions"><button :disabled="busy">{{ t('Create administrator') }}</button></div>
      </form>

      <form v-else-if="stage === 'login'" class="card narrow" @submit.prevent="doLogin">
        <h1>{{ t('Platform console') }}</h1>
        <label for="l-user">{{ t('Username') }}</label><input id="l-user" v-model="f.username" autocomplete="username" autofocus required />
        <label for="l-pass">{{ t('Password') }}</label><input id="l-pass" v-model="f.password" type="password" autocomplete="current-password" required />
        <p v-if="err" class="err" role="alert">{{ err }}</p>
        <div class="actions"><button :disabled="busy">{{ t('Sign in') }}</button></div>
      </form>

      <form v-else-if="stage === 'mfa'" class="card narrow" @submit.prevent="doTotp">
        <h1>{{ t('Two-step verification') }}</h1>
        <label for="t-code">{{ t('Authenticator code') }}</label>
        <input id="t-code" v-model="f.code" inputmode="numeric" autocomplete="one-time-code" autofocus maxlength="12" :placeholder="t('6-digit code')" required />
        <p v-if="err" class="err" role="alert">{{ err }}</p>
        <div class="actions"><button :disabled="busy">{{ t('Verify') }}</button><button type="button" class="secondary" @click="logout">{{ t('Back') }}</button></div>
      </form>

      <div v-else-if="stage === 'enroll'" class="card narrow">
        <h1>{{ t('Set up two-step verification') }}</h1>
        <p class="muted">{{ t('Platform administrators must use an authenticator app.') }}</p>
        <div v-if="!enroll" class="actions"><button :disabled="busy" @click="beginEnroll">{{ t('Start') }}</button></div>
        <form v-else @submit.prevent="confirmEnroll">
          <QrCode :value="enroll.uri" :label="t('QR code for your authenticator app')" filename="sendora-platform-totp.svg" />
          <p class="muted">{{ t('Or enter this key manually:') }} <code>{{ enroll.secret }}</code></p>
          <label for="e-code">{{ t('Authenticator code') }}</label>
          <input id="e-code" v-model="f.code" inputmode="numeric" autocomplete="one-time-code" maxlength="12" required />
          <div class="actions"><button :disabled="busy">{{ t('Confirm') }}</button></div>
        </form>
        <p v-if="err" class="err" role="alert">{{ err }}</p>
      </div>

      <template v-else-if="stage === 'full'">
        <div class="tabs" role="tablist">
          <a v-for="[k, label] in [['tenants', tr('Tenants')], ['settings', tr('Platform settings')], ['audit', tr('Platform audit log')], ['admins', tr('Administrators')]]" :key="k"
            href="#" role="tab" :aria-selected="tab === k" :class="{ 'router-link-active': tab === k }" @click.prevent="show(k)">{{ t(label) }}</a>
        </div>
        <p v-if="err" class="err" role="alert">{{ err }}</p>
        <p v-if="notice" class="ok" role="status">{{ notice }}</p>

        <template v-if="tab === 'tenants' && !sel">
          <div class="card">
            <h2>{{ t('New tenant') }}</h2>
            <form @submit.prevent="createTenant">
              <div class="row">
                <div><label for="n-slug">{{ t('Subdomain') }}</label><input id="n-slug" v-model="nt.slug" required autocomplete="off" />
                  <p class="muted" v-if="nt.slug && !slugHint">{{ nt.slug }}.{{ boot?.base_domain }}</p><p v-if="slugHint" class="err">{{ slugHint }}</p></div>
                <div><label for="n-name">{{ t('Name') }}</label><input id="n-name" v-model="nt.name" /></div>
              </div>
              <div class="row">
                <div><label for="n-mail">{{ t('Email of the first administrator') }}</label><input id="n-mail" v-model="nt.admin_email" type="email" required /></div>
                <div><label for="n-user">{{ t('Username of the first administrator') }}</label><input id="n-user" v-model="nt.admin_username" :placeholder="t('default: the part before @')" /></div>
              </div>
              <div class="row">
                <div><label for="n-mu">{{ t('Maximum users (0 = unlimited)') }}</label><input id="n-mu" v-model="nt.max_users" inputmode="numeric" /></div>
                <div><label for="n-ms">{{ t('Maximum storage in GB (0 = unlimited)') }}</label><input id="n-ms" v-model="nt.max_storage_gb" inputmode="decimal" /></div>
              </div>
              <div class="actions"><button :disabled="busy || !!slugHint">{{ t('Create tenant') }}</button></div>
            </form>
            <div v-if="created" class="card" role="status" style="margin-top:16px">
              <p class="ok">{{ t('Tenant {name} created.', { name: created.tenant.slug }) }} <a :href="created.tenant.url" rel="noopener">{{ created.tenant.url }}</a></p>
              <p v-if="created.welcome_emailed">{{ t('A welcome email with a link to choose a password was sent to the first administrator.') }}</p>
              <template v-else>
                <p>{{ t('No email could be sent. Give this one-time link to the first administrator (valid for 7 days, shown only now):') }}</p>
                <p><code style="word-break:break-all">{{ created.setup_link }}</code></p>
              </template>
            </div>
          </div>
          <div class="card">
            <form class="row" @submit.prevent="search">
              <div><label for="q">{{ t('Search') }}</label><input id="q" v-model="q" :placeholder="t('Subdomain, name or domain')" /></div>
              <div style="flex:0 0 auto"><button class="secondary">{{ t('Search') }}</button></div>
            </form>
            <div class="table-wrap">
              <table>
                <thead><tr><th>{{ t('Subdomain') }}</th><th>{{ t('Name') }}</th><th>{{ t('Status') }}</th><th>{{ t('Users') }}</th><th>{{ t('Storage') }}</th><th></th></tr></thead>
                <tbody>
                  <tr v-for="tn in tenants" :key="tn.id">
                    <td><a :href="tn.url" rel="noopener">{{ tn.primary_host }}</a></td>
                    <td>{{ tn.name }}</td>
                    <td><span class="badge">{{ statusLabel(tn.status) }}</span></td>
                    <td>{{ tn.user_count }}<span v-if="tn.max_users" class="muted"> / {{ tn.max_users }}</span></td>
                    <td>{{ fmtSize(tn.storage_used_bytes) }}<span v-if="tn.max_storage_bytes" class="muted"> / {{ fmtSize(tn.max_storage_bytes) }} ({{ quotaPercent(tn.storage_used_bytes, tn.max_storage_bytes) }}%)</span></td>
                    <td><button class="small secondary" @click="openTenant(tn.id)">{{ t('Manage') }}</button></td>
                  </tr>
                  <tr v-if="!tenants.length"><td colspan="6" class="muted">{{ t('No tenants yet.') }}</td></tr>
                </tbody>
              </table>
            </div>
            <p class="muted">{{ t('Counts are refreshed while a tenant is in use and by the periodic maintenance. The console never shows tenant content.') }}</p>
          </div>
        </template>

        <template v-if="tab === 'tenants' && sel">
          <div class="card">
            <div class="actions" style="margin-top:0"><button class="secondary small" @click="sel = null">{{ t('Back') }}</button></div>
            <h2>{{ sel.tenant.name }} <span class="badge">{{ statusLabel(sel.tenant.status) }}</span></h2>
            <p class="muted">{{ sel.tenant.url }} · {{ t('Created') }} {{ fmtTime(sel.tenant.created_at) }} · ID <code>{{ sel.tenant.id }}</code>
              <span v-if="sel.tenant.key_mode === 'legacy'"> · {{ t('imported from a single-tenant installation') }}</span></p>
            <div class="actions">
              <button v-if="sel.tenant.status === 'active'" class="secondary" @click="setStatus('suspend')">{{ t('Suspend') }}</button>
              <button v-else @click="setStatus('resume')">{{ t('Resume') }}</button>
            </div>
          </div>
          <form class="card" @submit.prevent="saveQuota">
            <h3>{{ t('Quotas') }}</h3>
            <p class="muted">{{ t('In use: {users} users, {storage}.', { users: sel.tenant.user_count, storage: fmtSize(sel.tenant.storage_used_bytes) }) }}</p>
            <div class="row">
              <div><label for="q-mu">{{ t('Maximum users (0 = unlimited)') }}</label><input id="q-mu" v-model="quota.max_users" inputmode="numeric" /></div>
              <div><label for="q-ms">{{ t('Maximum storage in GB (0 = unlimited)') }}</label><input id="q-ms" v-model="quota.max_storage_gb" inputmode="decimal" /></div>
            </div>
            <div class="actions"><button :disabled="busy">{{ t('Save') }}</button></div>
          </form>
          <div class="card">
            <h3>{{ t('Domains') }}</h3>
            <div class="table-wrap"><table>
              <tbody>
                <tr><td><code>{{ sel.subdomain }}</code></td><td>{{ t('Subdomain') }}</td><td><span v-if="sel.tenant.primary_host === sel.subdomain" class="badge">{{ t('Primary') }}</span>
                  <button v-else class="small secondary" @click="makePrimary(sel.subdomain)">{{ t('Make primary') }}</button></td><td></td></tr>
                <tr v-for="d in sel.domains" :key="d.domain">
                  <td><code>{{ d.domain }}</code></td>
                  <td><template v-if="d.verified">{{ t('Verified') }}</template>
                    <template v-else>{{ t('Add this DNS TXT record, then verify:') }}<br /><code>{{ d.record_name }}</code> → <code style="word-break:break-all">{{ d.record_value }}</code></template></td>
                  <td><span v-if="sel.tenant.primary_host === d.domain" class="badge">{{ t('Primary') }}</span>
                    <button v-else-if="d.verified" class="small secondary" @click="makePrimary(d.domain)">{{ t('Make primary') }}</button>
                    <button v-else class="small" @click="verifyDomain(d.domain)">{{ t('Verify') }}</button></td>
                  <td><button class="small danger" @click="removeDomain(d.domain)">{{ t('Remove') }}</button></td>
                </tr>
              </tbody>
            </table></div>
            <form class="row" @submit.prevent="addDomain">
              <div><label for="d-new">{{ t('Custom domain') }}</label><input id="d-new" v-model="domainInput" placeholder="files.example.com" /></div>
              <div style="flex:0 0 auto"><button class="secondary">{{ t('Add') }}</button></div>
            </form>
            <p class="muted">{{ t('A custom domain needs a DNS record pointing to this platform and a TLS certificate. Other addresses of the tenant redirect to its primary domain.') }}</p>
          </div>
          <form class="card" @submit.prevent="saveStorage">
            <h3>{{ t('Storage') }}</h3>
            <p class="muted">{{ t('Where this tenant\'s files are stored once object storage is available. Until then files stay in the tenant\'s directory.') }}</p>
            <div class="row">
              <div><label for="s-mode">{{ t('Storage') }}</label>
                <select id="s-mode" v-model="storage.mode"><option value="platform">{{ t('Platform default (own prefix)') }}</option><option value="own">{{ t('Tenant\'s own bucket') }}</option></select></div>
              <div><label for="s-prefix">{{ t('Prefix') }}</label><input id="s-prefix" v-model="storage.prefix" /></div>
            </div>
            <label for="s-config">{{ t('Configuration (JSON)') }}</label>
            <textarea id="s-config" v-model="storage.config" rows="6" spellcheck="false" style="font-family:monospace"></textarea>
            <p class="muted">{{ t('Secret values are shown as ******** and kept when saved unchanged.') }}</p>
            <div class="actions"><button :disabled="busy">{{ t('Save') }}</button></div>
          </form>
          <form class="card" @submit.prevent="deleteTenant">
            <h3>{{ t('Delete tenant') }}</h3>
            <p class="err">{{ t('This permanently removes the tenant\'s database, files and backups. It cannot be undone.') }}</p>
            <div class="row">
              <div><label for="x-c">{{ t('Type the subdomain to confirm') }}</label><input id="x-c" v-model="del.confirm" autocomplete="off" /></div>
              <div><label for="x-t">{{ t('Authenticator code') }}</label><input id="x-t" v-model="del.code" inputmode="numeric" autocomplete="one-time-code" maxlength="12" /></div>
            </div>
            <div class="actions"><button class="danger" :disabled="busy || del.confirm !== sel.tenant.slug">{{ t('Delete tenant') }}</button></div>
          </form>
        </template>

        <form v-if="tab === 'settings' && settings" class="card" @submit.prevent="saveSettings">
          <h2>{{ t('Platform settings') }}</h2>
          <p class="muted">{{ t('Base domain') }}: <code>{{ boot?.base_domain }}</code> ({{ t('set by SENDORA_BASE_DOMAIN') }})</p>
          <template v-for="[k, label] in settingKeys" :key="k">
            <label :for="'s-' + k">{{ t(label) }}</label>
            <input :id="'s-' + k" v-model="settings[k]" :disabled="managed.includes(k)" />
            <p v-if="managed.includes(k)" class="muted">{{ t('Set by the server configuration.') }}</p>
          </template>
          <label for="s-pass">{{ t('SMTP password') }}</label>
          <input id="s-pass" v-model="smtpPass" type="password" autocomplete="new-password" :disabled="managed.includes('platform_smtp_pass')" :placeholder="settings.platform_smtp_pass_set ? t('unchanged') : ''" />
          <p class="muted">{{ t('Tenants without their own SMTP server send email through this default server.') }}</p>
          <div class="actions"><button :disabled="busy">{{ t('Save') }}</button></div>
        </form>

        <div v-if="tab === 'audit'" class="card">
          <h2>{{ t('Platform audit log') }}</h2>
          <p v-if="auditStatus" :class="auditStatus.ok ? 'ok' : 'err'">{{ auditStatus.ok ? t('The audit log is intact.') : t('The audit log does not verify.') }}</p>
          <div class="table-wrap"><table>
            <thead><tr><th>#</th><th>{{ t('Time') }}</th><th>{{ t('Who') }}</th><th>{{ t('Action') }}</th><th>{{ t('Target') }}</th><th>{{ t('Details') }}</th></tr></thead>
            <tbody><tr v-for="r in audit" :key="r.seq"><td>{{ r.seq }}</td><td>{{ fmtTime(r.ts) }}</td><td>{{ r.actor_name }}</td><td>{{ r.action }} <span v-if="r.outcome !== 'ok'" class="badge">{{ r.outcome }}</span></td><td>{{ r.target }}</td><td>{{ r.detail }}</td></tr></tbody>
          </table></div>
        </div>

        <div v-if="tab === 'admins'" class="card">
          <h2>{{ t('Administrators') }}</h2>
          <label for="a-code">{{ t('Your authenticator code (required for changes)') }}</label>
          <input id="a-code" v-model="adminCode" inputmode="numeric" autocomplete="one-time-code" maxlength="12" />
          <div class="table-wrap"><table>
            <thead><tr><th>{{ t('Username') }}</th><th>{{ t('Email') }}</th><th>{{ t('Authenticator') }}</th><th></th></tr></thead>
            <tbody><tr v-for="a in admins" :key="a.id"><td>{{ a.username }}</td><td>{{ a.email }}</td><td>{{ a.totp_enabled ? t('Active') : t('Not set up') }}</td>
              <td><template v-if="a.id !== me?.id"><button class="small secondary" @click="resetAdminMfa(a)">{{ t('Reset authenticator') }}</button> <button class="small danger" @click="deleteAdmin(a)">{{ t('Remove') }}</button></template></td></tr></tbody>
          </table></div>
          <h3>{{ t('New administrator') }}</h3>
          <form @submit.prevent="createAdmin">
            <div class="row">
              <div><label for="na-u">{{ t('Username') }}</label><input id="na-u" v-model="na.username" required /></div>
              <div><label for="na-e">{{ t('Email') }}</label><input id="na-e" v-model="na.email" type="email" /></div>
            </div>
            <div class="row">
              <div><label for="na-p">{{ t('Password') }}</label><input id="na-p" v-model="na.password" type="password" autocomplete="new-password" minlength="12" required /></div>
              <div><label for="na-c">{{ t('Your authenticator code') }}</label><input id="na-c" v-model="na.code" inputmode="numeric" autocomplete="one-time-code" maxlength="12" required /></div>
            </div>
            <div class="actions"><button :disabled="busy">{{ t('Create administrator') }}</button></div>
          </form>
        </div>
      </template>
    </main>
  </div>
</template>
