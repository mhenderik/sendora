<script setup>
import { ref, onMounted } from 'vue'
import { api, fmtDate, session } from '../../api'
import { t } from '../../i18n'
const data = ref(null), status = ref(null), err = ref(''), q = ref(''), category = ref(''), offset = ref(0), busy = ref(false)
const limit = 50
// Share events (uploads, downloads, previews, invites) are not visible to administrators.
const categories = ['auth', 'account', 'admin', 'security', 'system']
function filters() {
  const params = new URLSearchParams()
  if (q.value.trim()) params.set('q', q.value.trim())
  if (category.value) params.set('category', category.value)
  return params
}
async function load(o = offset.value) {
  busy.value = true; err.value = ''
  try {
    const params = filters()
    params.set('limit', limit); params.set('offset', o)
    data.value = await api('/logs?' + params)
    offset.value = o
  } catch (e) { err.value = e.message } finally { busy.value = false }
}
async function loadStatus() {
  try { status.value = await api('/logs/status') } catch { status.value = null }
}
const search = () => load(0)
async function download() {
  err.value = ''
  try {
    const params = filters().toString()
    const r = await fetch('/api/logs/export' + (params ? '?' + params : ''), { headers: { Authorization: 'Bearer ' + session.token } })
    if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || t('Download failed'))
    const a = document.createElement('a')
    a.href = URL.createObjectURL(await r.blob()); a.download = 'sendora-logs.csv'
    document.body.appendChild(a); a.click(); a.remove()
    setTimeout(() => URL.revokeObjectURL(a.href), 1000)
  } catch (e) { err.value = e.message }
}
onMounted(() => { load(0); loadStatus() })
</script>
<template>
  <div class="card">
    <div class="actions" style="margin:0 0 12px"><h1 style="margin:0">{{ t('Logs') }}</h1>
      <button class="small secondary" :disabled="busy" @click="load(); loadStatus()">{{ t('Refresh') }}</button>
      <button class="small secondary" :disabled="!data" @click="download">{{ t('Download CSV') }}</button></div>
    <p v-if="status && status.verified" class="ok" role="status">{{ t('Log integrity verified: no record has been changed, removed or added.') }}</p>
    <p v-else-if="status" class="err" role="alert">{{ t('Log integrity check FAILED: the log has been tampered with or damaged. Contact the auditor.') }}</p>
    <p v-if="data" class="muted">{{ t('The log is tamper-evident and cannot be switched off. Entries are kept for {n} day(s). Events about shares are only visible to auditors.', { n: data.retention_days }) }}</p>
    <form class="actions" style="margin:0 0 12px" @submit.prevent="search">
      <input v-model="q" :placeholder="t('Filter (action, user, target)')" style="max-width:320px" />
      <select v-model="category" :aria-label="t('Category')" @change="search"><option value="">{{ t('All categories') }}</option><option v-for="c in categories" :key="c" :value="c">{{ c }}</option></select>
      <button class="small secondary">{{ t('Search') }}</button></form>
    <p v-if="err" class="err">{{ err }}</p>
    <p v-if="data && !data.logs.length" class="muted">—</p>
    <div v-if="data && data.logs.length" class="table-wrap"><table>
      <tr><th>{{ t('Time') }}</th><th>{{ t('User') }}</th><th>{{ t('Category') }}</th><th>{{ t('Action') }}</th><th>{{ t('Target') }}</th><th>{{ t('Outcome') }}</th><th>{{ t('Details') }}</th></tr>
      <tr v-for="l in data.logs" :key="l.id"><td>{{ fmtDate(l.created_at) }}</td><td>{{ l.actor }} <span v-if="l.role" class="muted">({{ l.role }})</span></td><td>{{ l.category }}</td><td>{{ l.action }}</td><td>{{ l.target }}</td><td>{{ l.outcome }}</td><td>{{ l.detail }}</td></tr>
    </table></div>
    <div v-if="data" class="actions"><span>{{ data.total }}</span>
      <button class="small secondary" :disabled="busy || offset === 0" @click="load(Math.max(0, offset - limit))">{{ t('Previous') }}</button>
      <button class="small secondary" :disabled="busy || offset + limit >= data.total" @click="load(offset + limit)">{{ t('Next') }}</button></div>
  </div>
</template>
