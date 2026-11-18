<script setup>
import { ref, onMounted } from 'vue'
import { api, fmtDate, session } from '../api'
import { t } from '../i18n'
const data = ref(null), status = ref(null), err = ref(''), offset = ref(0), busy = ref(false)
const f = ref({ q: '', category: '', action: '', actor: '', outcome: '', from: '', to: '' })
const limit = 50
const categories = ['auth', 'account', 'admin', 'security', 'share', 'system']
const outcomes = ['ok', 'denied', 'failed', 'attempted']
function filters() {
  const params = new URLSearchParams()
  for (const [k, v] of Object.entries(f.value)) if (String(v).trim()) params.set(k, String(v).trim())
  return params
}
async function load(o = offset.value) {
  busy.value = true; err.value = ''
  try {
    const params = filters()
    params.set('limit', limit); params.set('offset', o)
    data.value = await api('/audit?' + params)
    offset.value = o
  } catch (e) { err.value = e.message } finally { busy.value = false }
}
async function loadStatus() {
  try { status.value = await api('/audit/status') } catch (e) { status.value = null; err.value = e.message }
}
const search = () => load(0)
async function download(format) {
  err.value = ''
  try {
    const params = filters()
    params.set('format', format)
    const r = await fetch('/api/audit/export?' + params, { headers: { Authorization: 'Bearer ' + session.token } })
    if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || t('Download failed'))
    const a = document.createElement('a')
    a.href = URL.createObjectURL(await r.blob()); a.download = 'sendora-audit.' + format
    document.body.appendChild(a); a.click(); a.remove()
    setTimeout(() => URL.revokeObjectURL(a.href), 1000)
  } catch (e) { err.value = e.message }
}
onMounted(() => { load(0); loadStatus() })
</script>
<template>
  <div class="card">
    <div class="actions" style="margin:0 0 12px"><h1 style="margin:0">{{ t('Audit log') }}</h1>
      <button class="small secondary" :disabled="busy" @click="load(); loadStatus()">{{ t('Refresh') }}</button>
      <button class="small secondary" :disabled="!data" @click="download('csv')">{{ t('Download CSV') }}</button>
      <button class="small secondary" :disabled="!data" @click="download('jsonl')">{{ t('Download JSONL') }}</button></div>
    <div v-if="status" :class="status.verified ? 'ok' : 'err'" :role="status.verified ? 'status' : 'alert'">
      <strong v-if="status.verified">{{ t('Chain verified: no record has been changed, removed or added.') }}</strong>
      <strong v-else>{{ t('Chain BROKEN at record {n}: {reason}', { n: status.first_broken_seq, reason: status.reason }) }}</strong>
      <div class="muted">{{ t('Records: {n}', { n: status.count }) }} · {{ t('Oldest: {d}', { d: fmtDate(status.oldest_ts) }) }} · {{ t('Retention: {n} day(s)', { n: status.retention_days }) }} · {{ t('Checkpoint: {n}', { n: status.checkpoint.seq }) }} · {{ t('Write failures: {n}', { n: status.write_failures }) }}</div>
    </div>
    <form class="audit-filters" @submit.prevent="search">
      <input v-model="f.q" :placeholder="t('Filter (action, user, target)')" />
      <select v-model="f.category" :aria-label="t('Category')"><option value="">{{ t('All categories') }}</option><option v-for="c in categories" :key="c" :value="c">{{ c }}</option></select>
      <input v-model="f.action" :placeholder="t('Action')" />
      <input v-model="f.actor" :placeholder="t('User')" />
      <select v-model="f.outcome" :aria-label="t('Outcome')"><option value="">{{ t('All outcomes') }}</option><option v-for="o in outcomes" :key="o" :value="o">{{ o }}</option></select>
      <input v-model="f.from" type="date" :aria-label="t('From')" /><input v-model="f.to" type="date" :aria-label="t('To')" />
      <button class="small secondary">{{ t('Search') }}</button></form>
    <p v-if="err" class="err">{{ err }}</p>
    <p v-if="data && !data.records.length" class="muted">—</p>
    <div v-if="data && data.records.length" class="table-wrap"><table>
      <tr><th>#</th><th>{{ t('Time') }}</th><th>{{ t('Category') }}</th><th>{{ t('User') }}</th><th>{{ t('Action') }}</th><th>{{ t('Target') }}</th><th>{{ t('Outcome') }}</th><th>{{ t('Details') }}</th><th>IP</th></tr>
      <tr v-for="l in data.records" :key="l.seq"><td>{{ l.seq }}</td><td>{{ fmtDate(l.ts) }}</td><td>{{ l.category }}</td><td>{{ l.actor }} <span class="muted">({{ l.actor_role }})</span></td><td>{{ l.action }}</td><td>{{ l.target }}</td><td>{{ l.outcome }}</td><td>{{ l.detail }}</td><td>{{ l.ip }}</td></tr>
    </table></div>
    <div v-if="data" class="actions"><span>{{ data.total }}</span>
      <button class="small secondary" :disabled="busy || offset === 0" @click="load(Math.max(0, offset - limit))">{{ t('Previous') }}</button>
      <button class="small secondary" :disabled="busy || offset + limit >= data.total" @click="load(offset + limit)">{{ t('Next') }}</button></div>
  </div>
</template>
<style scoped>
.audit-filters{display:grid;grid-template-columns:repeat(auto-fit,minmax(170px,1fr));gap:10px;margin:12px 0;align-items:center}
.audit-filters button{justify-self:start}
</style>
