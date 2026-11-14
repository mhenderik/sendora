<script setup>
import { ref, onMounted, computed } from 'vue'
import { api, fmtBytes } from '../../api'
import { t } from '../../i18n'
const s = ref(null)
onMounted(async () => (s.value = await api('/stats')))
const max = computed(() => Math.max(1, ...(s.value?.downloads_per_day || []).map(d => d.count)))
const up = n => { const d = Math.floor(n / 86400), h = Math.floor(n % 86400 / 3600), m = Math.floor(n % 3600 / 60); return `${d}d ${h}h ${m}m` }
</script>
<template>
  <div v-if="s">
    <div class="stats">
      <div class="card stat"><b>{{ s.users }}</b>{{ t('Users') }}</div>
      <div class="card stat"><b>{{ s.active_shares }}/{{ s.shares }}</b>{{ t('Active') }} / {{ t('All shares') }}</div>
      <div class="card stat"><b>{{ s.files }}</b>{{ t('Files') }}</div>
      <div class="card stat"><b>{{ fmtBytes(s.storage_bytes) }}</b>{{ t('Storage') }}</div>
      <div class="card stat"><b>{{ s.downloads }}</b>{{ t('Downloads') }}</div>
    </div>
    <div class="card"><h2>{{ t('Downloads, last 14 days') }}</h2>
      <div class="chart"><i v-for="d in s.downloads_per_day" :key="d.day" :style="{ height: d.count / max * 100 + '%' }" :title="d.day + ': ' + d.count" /></div></div>
    <div class="card"><h2>{{ t('Top shares') }}</h2>
      <table><tr v-for="x in s.top_shares" :key="x.long_id"><td><a :href="'/share/' + x.long_id">{{ x.name }}</a></td><td>{{ x.downloads }}</td></tr></table></div>
    <div class="card"><h2>{{ t('System') }}</h2>
      <p class="muted">v{{ s.system.version }} · {{ s.system.go }} · {{ s.system.os }} · {{ t('uptime') }} {{ up(s.system.uptime_seconds) }} · {{ fmtBytes(s.system.mem_bytes) }}</p></div>
  </div>
</template>
