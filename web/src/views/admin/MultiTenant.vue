<script setup>
// Converting this single-tenant installation to multi-tenant mode: the
// conversion is scheduled here and runs at the next start of the server
// (internal/app/mt_convert.go, docs/MULTI-TENANT.md section 7).
import { ref, reactive, computed, onMounted } from 'vue'
import { api } from '../../api'
import { t } from '../../i18n'
import PasswordHints from '../../components/PasswordHints.vue'
import { platformPolicy, passwordChecklist } from '../../password-check.mjs'

const st = ref(null), err = ref(''), msg = ref(''), busy = ref(false), confirmed = ref(false)
const f = reactive({ base_domain: '', slug: '', name: '', admin_username: '', admin_email: '', admin_password: '', mfa_required: true, use_custom_domain: true, custom_domain: '' })
async function load() {
  st.value = await api('/multitenant')
  if (!f.custom_domain) f.custom_domain = st.value.suggested_custom_domain || ''
  f.use_custom_domain = !!f.custom_domain
}
onMounted(async () => { try { await load() } catch (e) { err.value = e.message } })
const slugOK = computed(() => /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(f.slug) && !f.slug.includes('--'))
const pwPolicy = computed(() => platformPolicy(st.value || {}))
const ready = computed(() => f.base_domain && slugOK.value && f.admin_username && f.admin_email && passwordChecklist(f.admin_password, pwPolicy.value).ok && confirmed.value)

async function schedule() {
  err.value = msg.value = ''; busy.value = true
  try {
    await api('/multitenant', { method: 'POST', body: { base_domain: f.base_domain, slug: f.slug, name: f.name, admin_username: f.admin_username, admin_email: f.admin_email,
      admin_password: f.admin_password, mfa_required: f.mfa_required, custom_domain: f.use_custom_domain ? f.custom_domain : '' } })
    f.admin_password = ''; confirmed.value = false
    msg.value = t('The conversion is scheduled. It runs at the next start of the server.')
    await load()
  } catch (e) { err.value = e.message } finally { busy.value = false }
}
async function cancel() {
  err.value = msg.value = ''
  try { await api('/multitenant', { method: 'DELETE' }); msg.value = t('The scheduled conversion was cancelled.'); await load() } catch (e) { err.value = e.message }
}
async function restart() {
  err.value = msg.value = ''
  try { await api('/multitenant/restart', { method: 'POST' }); msg.value = t('The server is restarting. Sign in again in a minute at the new addresses.') } catch (e) { err.value = e.message }
}
</script>
<template>
  <p v-if="!st && err" class="err" role="alert">{{ err }}</p>
  <div v-if="st" class="card">
    <h1>{{ t('Multi-tenant mode') }}</h1>
    <p>{{ t('Convert this installation into a platform that serves several organisations. This installation becomes the first organisation (tenant); a platform console on admin.<base domain> manages the organisations.') }}</p>
    <p v-if="st.last_error" class="err" role="alert">{{ t('The last conversion failed ({stage}): {error}. Nothing was changed; the installation runs single-tenant as before.', { stage: st.last_error.stage, error: st.last_error.error }) }}</p>
    <p v-if="msg" class="ok" role="status">{{ msg }}</p>
    <p v-if="err" class="err" role="alert">{{ err }}</p>

    <template v-if="st.pending">
      <p class="notice" role="status">{{ t('A conversion is scheduled by {by} on {at}: base domain {base}, first organisation {slug}, platform administrator {admin}. It runs at the next start of the server.', { by: st.pending.requested_by, at: st.pending.requested_at, base: st.pending.base_domain, slug: st.pending.slug, admin: st.pending.admin_username }) }}</p>
      <p v-if="!st.supervisor" class="muted">{{ t('Restart the server (for example docker restart or a new pod) to run the conversion.') }}</p>
      <div class="actions">
        <button v-if="st.supervisor" @click="restart">{{ t('Restart now') }}</button>
        <button class="secondary" @click="cancel">{{ t('Cancel the conversion') }}</button>
      </div>
    </template>

    <form v-else @submit.prevent="schedule">
      <h2>{{ t('Requirements') }}</h2>
      <ul>
        <li>{{ t('A wildcard DNS record *.<base domain> pointing to this server, and a TLS certificate for <base domain> and *.<base domain> (DNS-01).') }}</li>
        <li>{{ t('The reverse proxy or ingress must pass every host name of the base domain (and custom domains) to Sendora.') }}</li>
        <li>{{ t('Set SENDORA_BASE_DOMAIN in the deployment later if you prefer; the mode and base domain are stored in the data directory.') }}</li>
      </ul>
      <h2>{{ t('What happens') }}</h2>
      <ul>
        <li>{{ t('At the next start a full encrypted backup of the database is made, then the installation is converted with the verified tenant import: database, shares, files, backups and the audit log move to the first organisation. Share files are not rewritten.') }}</li>
        <li>{{ t('The original database and backups are kept in pre-tenant-import-<time>/ in the data directory. If anything fails, the conversion is undone and the server starts single-tenant as before.') }}</li>
        <li v-if="st.jwt_secret_set">{{ t('JWT_SECRET is set: everyone has to sign in again after the conversion (each organisation gets its own signing secret).') }}</li>
        <li>{{ t('Users then sign in at <slug>.<base domain>. With the current address as custom domain, existing share links keep working.') }}</li>
      </ul>

      <h2>{{ t('Platform') }}</h2>
      <label for="mt-base">{{ t('Base domain') }}</label><input id="mt-base" v-model="f.base_domain" placeholder="sendora.example.com" required autocomplete="off" />
      <div class="row">
        <div><label for="mt-slug">{{ t('Subdomain of this organisation') }}</label><input id="mt-slug" v-model="f.slug" required autocomplete="off" />
          <p v-if="f.slug && f.base_domain" class="muted">{{ f.slug }}.{{ f.base_domain }}</p></div>
        <div><label for="mt-name">{{ t('Name of this organisation') }}</label><input id="mt-name" v-model="f.name" /></div>
      </div>
      <label class="check"><input type="checkbox" v-model="f.use_custom_domain" /> {{ t('Keep the current address as a custom domain of this organisation (existing links keep working)') }}</label>
      <input v-if="f.use_custom_domain" id="mt-custom" v-model="f.custom_domain" :aria-label="t('Custom domain')" placeholder="files.example.com" />
      <h2>{{ t('First platform administrator') }}</h2>
      <div class="row">
        <div><label for="mt-user">{{ t('Username') }}</label><input id="mt-user" v-model="f.admin_username" required autocomplete="off" /></div>
        <div><label for="mt-mail">{{ t('Email') }}</label><input id="mt-mail" v-model="f.admin_email" type="email" required /></div>
      </div>
      <label for="mt-pass">{{ t('Password') }}</label><input id="mt-pass" v-model="f.admin_password" type="password" autocomplete="new-password" minlength="12" required />
      <PasswordHints :password="f.admin_password" :policy="pwPolicy" />
      <label class="check"><input type="checkbox" v-model="f.mfa_required" /> {{ t('Platform administrators must use an authenticator app') }}</label>
      <label class="check"><input type="checkbox" v-model="confirmed" /> {{ t('I have read the requirements; schedule the conversion for the next start') }}</label>
      <div class="actions"><button :disabled="busy || !ready">{{ t('Schedule the conversion') }}</button></div>
    </form>
  </div>
</template>
