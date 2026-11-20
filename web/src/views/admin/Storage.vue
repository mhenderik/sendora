<script setup>
// Object storage: where new share files go (local, S3, Azure Blob, GCS), the
// connection test, where existing files live and moving them to the current
// location. Only the storage keys are sent when saving.
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { api, fmtBytes, fmtDate } from '../../api'
import { t } from '../../i18n'

const KEYS = ['storage_backend',
  'storage_s3_endpoint', 'storage_s3_region', 'storage_s3_bucket', 'storage_s3_prefix', 'storage_s3_path_style', 'storage_s3_auth', 'storage_s3_access_key', 'storage_s3_secret_key',
  'storage_azure_account', 'storage_azure_container', 'storage_azure_endpoint', 'storage_azure_prefix', 'storage_azure_auth', 'storage_azure_account_key', 'storage_azure_sas_token',
  'storage_gcs_bucket', 'storage_gcs_endpoint', 'storage_gcs_prefix', 'storage_gcs_auth', 'storage_gcs_credentials_json',
  'backup_remote_enabled']
const SECRETS = ['storage_s3_secret_key', 'storage_azure_account_key', 'storage_azure_sas_token', 'storage_gcs_credentials_json']

const s = ref(null), isSet = ref({}), managed = ref([]), status = ref(null)
const msg = ref(''), err = ref(''), busy = ref(false), testResult = ref(null), testing = ref(false), migMsg = ref(''), migErr = ref('')
const m = k => managed.value.includes(k)
const ph = k => (isSet.value[k] ? '••••••••' : '')
const running = computed(() => status.value?.migration?.status === 'running')
let timer = null

async function loadStatus() {
  status.value = await api('/storage')
  if (running.value && !timer) timer = setInterval(() => loadStatus().catch(() => {}), 2000)
  if (!running.value && timer) { clearInterval(timer); timer = null }
}
async function loadSettings() {
  const settings = await api('/settings')
  managed.value = settings.managed_settings || []
  s.value = Object.fromEntries(KEYS.map(k => [k, settings[k] ?? '']))
  isSet.value = Object.fromEntries(SECRETS.map(k => [k, !!settings[k + '_set']]))
}
async function load() { await Promise.all([loadSettings(), loadStatus()]) }
onMounted(async () => { try { await load() } catch (e) { err.value = e.message } })
onUnmounted(() => { if (timer) clearInterval(timer) })

// Configured (read-only) settings and blank secrets (= keep) are not sent.
const body = () => Object.fromEntries(Object.entries(s.value).filter(([k, v]) => !m(k) && !(SECRETS.includes(k) && v === '')))

async function save() {
  msg.value = err.value = ''; testResult.value = null; busy.value = true
  try {
    await api('/settings', { method: 'PUT', body: body() })
    await load(); msg.value = t('Saved')
  } catch (e) {
    err.value = e.message
    try { await loadStatus() } catch {}
  } finally { busy.value = false }
}
async function test() {
  msg.value = err.value = ''; testResult.value = null; testing.value = true
  try { testResult.value = await api('/storage/test', { method: 'POST', body: body() }) } catch (e) { err.value = e.message } finally { testing.value = false }
}
async function migrate() {
  migMsg.value = migErr.value = ''
  if (!confirm(t('Move all files to the current storage location? Each file is copied, verified and only then removed from its old location.'))) return
  try { await api('/storage/migrate', { method: 'POST' }); migMsg.value = t('Migration started.'); await loadStatus() } catch (e) { migErr.value = e.message }
}
async function cancelMigration() {
  migMsg.value = migErr.value = ''
  try { await api('/storage/migrate/cancel', { method: 'POST' }); await loadStatus() } catch (e) { migErr.value = e.message }
}
const flag = k => s.value[k] === 'true'
const setFlag = (k, e) => { s.value[k] = String(e.target.checked) }
const statusLabel = st => ({ running: t('running'), finished: t('finished'), failed: t('failed'), cancelled: t('cancelled') }[st] || st)
</script>
<template>
  <p v-if="!s && err" class="err" role="alert">{{ err }}</p>
  <div v-if="status" class="card">
    <h1>{{ t('Storage') }}</h1>
    <p>{{ t('New files are stored in: {where}', { where: status.description }) }}</p>
    <p v-if="status.error" class="err" role="alert">{{ t('The storage backend is not usable: {error}', { error: status.error }) }}</p>
    <p class="muted">{{ t('Files are encrypted before they leave the server: an object store only ever receives encrypted data under random names that reveal neither share links nor file names.') }}</p>
    <h2 style="margin-top:20px">{{ t('Where files live') }}</h2>
    <div class="table-wrap"><table>
      <tr><th>{{ t('Location') }}</th><th>{{ t('Files') }}</th><th>{{ t('Size') }}</th><th></th></tr>
      <tr v-for="l in status.locations" :key="l.id"><td>{{ l.description }}</td><td>{{ l.files }}</td><td>{{ fmtBytes(l.bytes) }}</td>
        <td><span v-if="l.current" class="muted">{{ t('current') }}</span></td></tr>
    </table></div>
    <p class="muted">{{ t('Files in an earlier location stay readable from there until they are moved.') }}</p>
    <p v-if="status.pending_deletes > 0" class="muted">{{ t('{n} removed file(s) are still waiting to be deleted from an object store; deletion is retried automatically.', { n: status.pending_deletes }) }}</p>

    <h2 style="margin-top:20px">{{ t('Move files') }}</h2>
    <template v-if="running">
      <p role="status">{{ t('Moving files: {done} of {total} done, {failed} failed ({bytes}).', { done: status.migration.done, total: status.migration.total, failed: status.migration.failed, bytes: fmtBytes(status.migration.bytes) }) }}</p>
      <div class="actions"><button class="secondary" @click="cancelMigration">{{ t('Cancel') }}</button></div>
    </template>
    <template v-else>
      <p v-if="status.remaining > 0">{{ t('{n} file(s) are not in the current location.', { n: status.remaining }) }}</p>
      <p v-else class="muted">{{ t('All files are in the current location.') }}</p>
      <div v-if="status.remaining > 0" class="actions"><button @click="migrate">{{ t('Move files to the current location') }}</button></div>
    </template>
    <p v-if="status.migration && !running" class="muted">{{ t('Last migration: {status} on {date} ({done} of {total} moved, {failed} failed).', { status: statusLabel(status.migration.status), date: fmtDate(status.migration.finished_at || status.migration.started_at), done: status.migration.done, total: status.migration.total, failed: status.migration.failed }) }}</p>
    <p v-if="status.migration && !running && status.migration.error" class="err">{{ status.migration.error }}</p>
    <p v-if="migMsg" class="ok" role="status">{{ migMsg }}</p><p v-if="migErr" class="err" role="alert">{{ migErr }}</p>
  </div>

  <form v-if="s" class="card" @submit.prevent="save">
    <h1>{{ t('Storage settings') }}</h1>
    <p v-if="KEYS.some(m)" class="muted">{{ t('Settings set in the server configuration (environment or Kubernetes ConfigMap) are read-only here: {n}.', { n: KEYS.filter(m).join(', ') }) }}</p>
    <label for="st-backend">{{ t('Storage backend') }}</label>
    <select id="st-backend" :disabled="m('storage_backend')" v-model="s.storage_backend">
      <option value="local">{{ t('Local data directory') }}</option>
      <option value="s3">{{ t('S3-compatible (AWS S3, MinIO, Cloudflare R2, Wasabi, Backblaze B2)') }}</option>
      <option value="azure">Azure Blob Storage</option>
      <option value="gcs">Google Cloud Storage</option>
    </select>

    <template v-if="s.storage_backend === 's3'">
      <div class="row">
        <div><label for="s3-endpoint">{{ t('Endpoint (empty = AWS)') }}</label><input id="s3-endpoint" :disabled="m('storage_s3_endpoint')" v-model="s.storage_s3_endpoint" placeholder="https://s3.example.com" /></div>
        <div><label for="s3-region">{{ t('Region') }}</label><input id="s3-region" :disabled="m('storage_s3_region')" v-model="s.storage_s3_region" placeholder="eu-west-1" /></div>
      </div>
      <div class="row">
        <div><label for="s3-bucket">{{ t('Bucket') }}</label><input id="s3-bucket" :disabled="m('storage_s3_bucket')" v-model="s.storage_s3_bucket" /></div>
        <div><label for="s3-prefix">{{ t('Prefix') }}</label><input id="s3-prefix" :disabled="m('storage_s3_prefix')" v-model="s.storage_s3_prefix" placeholder="sendora/" /></div>
      </div>
      <label class="check"><input :disabled="m('storage_s3_path_style')" type="checkbox" :checked="flag('storage_s3_path_style')" @change="setFlag('storage_s3_path_style', $event)" /> {{ t('Path-style addressing (MinIO and most self-hosted services)') }}</label>
      <label for="s3-auth">{{ t('Credentials') }}</label>
      <select id="s3-auth" :disabled="m('storage_s3_auth')" v-model="s.storage_s3_auth">
        <option value="static">{{ t('Access key and secret key') }}</option>
        <option value="default">{{ t('AWS default credential chain (IRSA, web identity, instance role)') }}</option>
      </select>
      <div v-if="s.storage_s3_auth === 'static'" class="row">
        <div><label for="s3-ak">{{ t('Access key') }}</label><input id="s3-ak" :disabled="m('storage_s3_access_key')" v-model="s.storage_s3_access_key" autocomplete="off" /></div>
        <div><label for="s3-sk">{{ t('Secret key') }}</label><input id="s3-sk" :disabled="m('storage_s3_secret_key')" v-model="s.storage_s3_secret_key" type="password" autocomplete="new-password" :placeholder="ph('storage_s3_secret_key')" /></div>
      </div>
    </template>

    <template v-if="s.storage_backend === 'azure'">
      <div class="row">
        <div><label for="az-account">{{ t('Storage account') }}</label><input id="az-account" :disabled="m('storage_azure_account')" v-model="s.storage_azure_account" /></div>
        <div><label for="az-container">{{ t('Container') }}</label><input id="az-container" :disabled="m('storage_azure_container')" v-model="s.storage_azure_container" /></div>
      </div>
      <div class="row">
        <div><label for="az-endpoint">{{ t('Endpoint (empty = https://<account>.blob.core.windows.net)') }}</label><input id="az-endpoint" :disabled="m('storage_azure_endpoint')" v-model="s.storage_azure_endpoint" /></div>
        <div><label for="az-prefix">{{ t('Prefix') }}</label><input id="az-prefix" :disabled="m('storage_azure_prefix')" v-model="s.storage_azure_prefix" placeholder="sendora/" /></div>
      </div>
      <label for="az-auth">{{ t('Authentication') }}</label>
      <select id="az-auth" :disabled="m('storage_azure_auth')" v-model="s.storage_azure_auth">
        <option value="shared_key">{{ t('Account key (shared key)') }}</option>
        <option value="sas">{{ t('SAS token') }}</option>
        <option value="identity">{{ t('Managed or workload identity (DefaultAzureCredential)') }}</option>
      </select>
      <template v-if="s.storage_azure_auth === 'shared_key'"><label for="az-key">{{ t('Account key') }}</label><input id="az-key" :disabled="m('storage_azure_account_key')" v-model="s.storage_azure_account_key" type="password" autocomplete="new-password" :placeholder="ph('storage_azure_account_key')" /></template>
      <template v-if="s.storage_azure_auth === 'sas'"><label for="az-sas">{{ t('SAS token') }}</label><input id="az-sas" :disabled="m('storage_azure_sas_token')" v-model="s.storage_azure_sas_token" type="password" autocomplete="new-password" :placeholder="ph('storage_azure_sas_token')" /></template>
    </template>

    <template v-if="s.storage_backend === 'gcs'">
      <div class="row">
        <div><label for="gcs-bucket">{{ t('Bucket') }}</label><input id="gcs-bucket" :disabled="m('storage_gcs_bucket')" v-model="s.storage_gcs_bucket" /></div>
        <div><label for="gcs-prefix">{{ t('Prefix') }}</label><input id="gcs-prefix" :disabled="m('storage_gcs_prefix')" v-model="s.storage_gcs_prefix" placeholder="sendora/" /></div>
      </div>
      <label for="gcs-endpoint">{{ t('Endpoint (empty = https://storage.googleapis.com)') }}</label><input id="gcs-endpoint" :disabled="m('storage_gcs_endpoint')" v-model="s.storage_gcs_endpoint" />
      <label for="gcs-auth">{{ t('Authentication') }}</label>
      <select id="gcs-auth" :disabled="m('storage_gcs_auth')" v-model="s.storage_gcs_auth">
        <option value="service_account">{{ t('Service account JSON key') }}</option>
        <option value="default">{{ t('Workload identity or application default credentials') }}</option>
        <option value="none">{{ t('None (emulator only)') }}</option>
      </select>
      <template v-if="s.storage_gcs_auth === 'service_account'"><label for="gcs-json">{{ t('Service account JSON key') }}</label>
        <textarea id="gcs-json" :disabled="m('storage_gcs_credentials_json')" v-model="s.storage_gcs_credentials_json" rows="4" spellcheck="false" autocomplete="off" :placeholder="ph('storage_gcs_credentials_json')" /></template>
    </template>

    <p v-if="s.storage_backend !== 'local'" class="muted">{{ t('Endpoints must use https; http is only accepted for localhost and private networks (for example MinIO next to Sendora). Secrets are never shown again; leave them empty to keep the stored value. A changed configuration is tested before it is saved.') }}</p>
    <label class="check"><input :disabled="m('backup_remote_enabled')" type="checkbox" :checked="flag('backup_remote_enabled')" @change="setFlag('backup_remote_enabled', $event)" /> {{ t('Also copy the daily database backups to the object store') }}</label>
    <p class="muted">{{ t('Backups are already encrypted (SQLCipher) and are stored under backups/ next to the files; the number of backups to keep also applies to the remote copies.') }}</p>

    <p v-if="testResult && testResult.ok" class="ok" role="status">{{ t('Connection works: a test object was written, read and deleted in {ms} ms ({where}).', { ms: testResult.ms, where: testResult.description }) }}</p>
    <p v-if="testResult && !testResult.ok" class="err" role="alert">{{ t('Connection test failed: {error}', { error: testResult.error }) }}</p>
    <p v-if="msg" class="ok" role="status">{{ msg }}</p><p v-if="err" class="err" role="alert">{{ err }}</p>
    <div class="actions"><button :disabled="busy">{{ t('Save') }}</button>
      <button v-if="s.storage_backend !== 'local'" type="button" class="secondary" :disabled="testing" @click="test">{{ testing ? t('Testing…') : t('Test connection') }}</button></div>
  </form>
</template>
