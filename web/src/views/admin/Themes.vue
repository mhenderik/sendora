<script setup>
import { terr } from '../../i18n/mark.mjs'
import { ref, watch, onMounted, onBeforeUnmount } from 'vue'
import { api } from '../../api'
import { applyTheme, app } from '../../state'
import { t, tr } from '../../i18n'
import { validateThemeBundle, serializeThemeBundle } from '../../theme-bundle'
import { previewData, swatchHex, copyName, nameClashes, isHex } from '../../theme-draft.mjs'
const themes = ref([]), edit = ref(null), err = ref(''), importFile = ref(null)
const colorLabels = { primary: tr('Primary color'), bg: tr('Background color'), surface: tr('Surface color'), text: tr('Text color'), muted: tr('Muted text color'), border: tr('Border color'), danger: tr('Danger color') }
const keys = Object.keys(colorLabels)
const defaultColors = { primary: '#2563eb', bg: '#f8fafc', surface: '#ffffff', text: '#0f172a', muted: '#64748b', border: '#e2e8f0', danger: '#dc2626' }
const themeName = th => th.bundled ? t(th.name) : th.name

// Live preview: the theme in effect before editing started is kept and restored on leaving the editor.
let original = null, previewing = false
function startPreview() { if (!previewing) { original = app.theme; previewing = true } }
function stopPreview() { if (previewing) { previewing = false; applyTheme(original) } }
function preview() { if (edit.value) applyTheme({ ...edit.value, data: previewData(edit.value.data, original?.data) }) }
function open(value) { startPreview(); edit.value = value; err.value = ''; preview() }
watch(() => edit.value && { ...edit.value.data }, d => { if (d) preview(); else stopPreview() })
onBeforeUnmount(stopPreview)

function newTheme() {
  const base = themes.value.find(th => th.active)?.data || defaultColors
  open({ id: 0, name: '', data: Object.fromEntries(keys.map(k => [k, base[k] || defaultColors[k]])) })
}
function editTheme(th) {
  if (edit.value && !confirm(t('Replace unsaved theme changes?'))) return
  // Built-in themes are never overwritten: editing one prepares a new custom copy.
  if (th.bundled) open({ id: 0, name: copyName(themeName(th), themes.value.map(themeName)), data: { ...th.data }, copyOf: th.id })
  else open({ ...th, data: { ...th.data } })
}
const load = async () => (themes.value = await api('/themes'))
onMounted(load)
async function activate(th) {
  if (edit.value && !confirm(t('Replace unsaved theme changes?'))) return
  edit.value = null; stopPreview()
  await api(`/themes/${th.id}/activate`, { method: 'POST' }); applyTheme(th); await load()
}
async function save() {
  err.value = ''
  try {
    const bundle = validateThemeBundle({ name: edit.value.name, data: edit.value.data })
    const own = themes.value.find(x => x.id === edit.value.id)
    if (nameClashes(bundle.name, themes.value.map(themeName), own && themeName(own))) throw terr('A theme with this name already exists.')
    await api('/themes', { method: 'POST', body: { ...bundle, id: edit.value.id } })
    edit.value = null; await load()
    stopPreview()
    const active = themes.value.find(x => x.active)
    if (active) applyTheme(active)
  } catch (e) { err.value = e.message }
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
    open({ id: 0, ...bundle })
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
      <p v-if="edit.copyOf" class="muted">{{ t('Built-in themes cannot be changed. Saving creates a new theme under the name above.') }}</p>
      <p class="muted">{{ t('Colour changes are previewed on this page immediately; they are applied for everyone only after saving and activating.') }}</p>
      <div class="row"><div v-for="k in keys" :key="k"><label>{{ t(colorLabels[k]) }}</label>
        <div class="colorfield" :class="{ bad: !isHex(edit.data[k]) }">
          <input type="color" class="swatch" :value="swatchHex(edit.data[k])" :aria-label="t(colorLabels[k])" @input="edit.data[k] = $event.target.value" />
          <input class="hex" :value="edit.data[k]" maxlength="9" spellcheck="false" autocomplete="off" :aria-label="t(colorLabels[k])" @input="edit.data[k] = $event.target.value.trim()" />
        </div></div></div>
      <div class="actions"><button>{{ t('Save') }}</button><button type="button" class="secondary" @click="edit = null">{{ t('Cancel') }}</button></div>
    </form>
    <table><tr v-for="th in themes" :key="th.id">
      <td><span v-for="k in keys" :key="k" class="sw" :style="{ background: th.data[k] }" /></td>
      <td>{{ themeName(th) }} <span v-if="th.active" class="badge on">{{ t('Active') }}</span> <span v-if="th.bundled" class="badge">{{ t('Built-in') }}</span></td>
      <td class="actions" style="margin:0"><button v-if="!th.active" class="small" @click="activate(th)">{{ t('Activate') }}</button>
        <button class="small secondary" @click="exportT(th)">{{ t('Export') }}</button>
        <button class="small secondary" @click="editTheme(th)">{{ th.bundled ? t('Copy and edit') : t('Edit') }}</button>
        <button v-if="!th.bundled && !th.active" class="small danger" @click="del(th)">{{ t('Delete') }}</button></td></tr></table>
  </div>
</template>
