<script setup>
import { ref, onMounted } from 'vue'
import { api, fmtBytes, fmtDate } from '../../api'
import { t } from '../../i18n'
const shares = ref([]), err = ref(''), editing = ref(null), value = ref('')
const load = async () => (shares.value = await api('/shares/all'))
onMounted(load)
async function expire(s) { await api(`/shares/${s.id}/expire`, { method: 'POST' }); load() }
function edit(s) {
  editing.value = s
  const d = new Date(s.expires_at || Date.now() + 7 * 864e5), pad = n => String(n).padStart(2, '0')
  value.value = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}
async function save(body) {
  err.value = ''
  try { await api(`/shares/${editing.value.id}/expiry`, { method: 'PUT', body }); editing.value = null; load() } catch (e) { err.value = e.message }
}
async function del(s) { if (confirm(t('Delete this share and its files?'))) { await api('/shares/' + s.id, { method: 'DELETE' }); load() } }
</script>
<template>
  <div class="card"><h1>{{ t('All shares') }}</h1>
    <div class="table-wrap"><table>
      <tr><th>{{ t('Name') }}</th><th>{{ t('Owner') }}</th><th>{{ t('Size') }}</th><th>{{ t('Downloads') }}</th><th>{{ t('Expires') }}</th><th /></tr>
      <tr v-for="s in shares" :key="s.id"><td><a :href="'/share/' + s.long_id" target="_blank">{{ s.name }}</a>
        <span class="badge" :class="s.expired ? 'off' : 'on'">{{ s.expired ? t('Expired') : t('Active') }}</span></td>
        <td>{{ s.owner }}</td><td>{{ fmtBytes(s.size) }}</td><td>{{ s.download_count }}</td><td>{{ fmtDate(s.expires_at) }}</td>
        <td class="actions" style="margin:0"><button class="small secondary" @click="edit(s)">{{ t('Set expiry') }}</button><button v-if="!s.expired" class="small secondary" @click="expire(s)">{{ t('Expire now') }}</button>
          <button class="small danger" @click="del(s)">{{ t('Delete') }}</button></td></tr>
    </table></div>
    <div v-if="editing" class="card actions"><label style="margin:0">{{ editing.name }} — {{ t('Expires') }}</label><input v-model="value" type="datetime-local" style="width:auto" />
      <button class="small" @click="value && save({ expires_at: new Date(value).toISOString() })">{{ t('Save') }}</button>
      <button class="small secondary" @click="save({ never: true })">{{ t('Never expires') }}</button>
      <button class="small secondary" @click="editing = null">{{ t('Cancel') }}</button></div>
    <p v-if="err" class="err">{{ err }}</p></div>
</template>
