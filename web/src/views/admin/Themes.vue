<script setup>
import { terr } from '../../i18n/mark.mjs'
import { ref, onMounted } from 'vue'
import { api } from '../../api'
import { applyTheme } from '../../state'
import { t, tr } from '../../i18n'
import { validateThemeBundle, serializeThemeBundle } from '../../theme-bundle'
const themes = ref([]), edit = ref(null), err = ref(''), importFile = ref(null)
const colorLabels = { primary: tr('Primary color'), bg: tr('Background color'), surface: tr('Surface color'), text: tr('Text color'), muted: tr('Muted text color'), border: tr('Border color'), danger: tr('Danger color') }
const keys = Object.keys(colorLabels)
const defaultColors = { primary: '#2563eb', bg: '#f8fafc', surface: '#ffffff', text: '#0f172a', muted: '#64748b', border: '#e2e8f0', danger: '#dc2626' }
function newTheme() {
  const base = themes.value.find(th => th.active)?.data || defaultColors
  edit.value = { id: 0, name: '', data: Object.fromEntries(keys.map(k => [k, base[k] || defaultColors[k]])) }
}
const load = async () => (themes.value = await api('/themes'))
onMounted(load)
async function activate(th) { await api(`/themes/${th.id}/activate`, { method: 'POST' }); applyTheme(th); await load() }
async function save() {
  err.value = ''
  try { const bundle = validateThemeBundle({ name: edit.value.name, data: edit.value.data }); await api('/themes', { method: 'POST', body: { ...bundle, id: edit.value.id } }); edit.value = null; await load() } catch (e) { err.value = e.message }
}
async function del(th) { try { await api('/themes/' + th.id, { method: 'DELETE' }); await load() } catch (e) { err.value = e.message } }
function exportT(th) {
  err.value = ''
  try {
    const url = URL.createObjectURL(new Blob([serializeThemeBundle(th)], { type: 'application/json' }))
    const link = document.createElement('a'); link.href = url; link.download = `theme-${th.id}.json`
    document.body.appendChild(link); link.click(); link.remove()
    setTimeout(() => URL.revokeObjectURL(url), 10000)
  } catch (e) { err.value = e.message }
}
async function importT(e) {
  const file = e.target.files?.[0]; e.target.value = ''; err.value = ''
  if (!file) return
  try {
    if (file.size > 16 * 1024) throw terr('Theme JSON must be smaller than 16 KiB.')
    const bundle = validateThemeBundle(JSON.parse(await file.text()))
    if (edit.value && !confirm(t('Replace unsaved theme changes?'))) return
    edit.value = { id: 0, ...bundle }
  } catch (e) { err.value = e.message }
}
</script>
<template>
  <div class="card"><div class="actions" style="margin:0 0 12px"><h1 style="margin:0">{{ t('Themes') }}</h1>
    <button class="small" @click="newTheme">+ {{ t('New theme') }}</button>
    <button class="small secondary" @click="importFile.click()">{{ t('Import') }}</button><input ref="importFile" type="file" accept=".json,application/json" hidden @change="importT" /></div>
    <p class="muted">{{ t('Import opens a new theme for review. Save to apply; existing themes stay unchanged.') }}</p>
    <p v-if="err" class="err">{{ err }}</p>
    <form v-if="edit" class="card" @submit.prevent="save">
      <label>{{ t('Name') }}</label><input v-model="edit.name" required />
      <div class="row"><div v-for="k in keys" :key="k"><label>{{ t(colorLabels[k]) }}</label><input type="color" v-model="edit.data[k]" /></div></div>
      <div class="actions"><button>{{ t('Save') }}</button><button type="button" class="secondary" @click="edit = null">{{ t('Cancel') }}</button></div>
    </form>
    <table><tr v-for="th in themes" :key="th.id">
      <td><span v-for="k in keys" :key="k" class="sw" :style="{ background: th.data[k] }" /></td>
      <td>{{ th.bundled ? t(th.name) : th.name }} <span v-if="th.active" class="badge on">{{ t('Active') }}</span> <span v-if="th.bundled" class="badge">{{ t('Built-in') }}</span></td>
      <td class="actions" style="margin:0"><button v-if="!th.active" class="small" @click="activate(th)">{{ t('Activate') }}</button>
        <button class="small secondary" @click="exportT(th)">{{ t('Export') }}</button>
        <button v-if="!th.bundled" class="small secondary" @click="edit = { ...th, data: { ...th.data } }">{{ t('Edit') }}</button>
        <button v-if="!th.bundled && !th.active" class="small danger" @click="del(th)">{{ t('Delete') }}</button></td></tr></table>
  </div>
</template>
