<script setup>
import { computed } from 'vue'
import { t } from '../i18n'
import { accountPolicy, passwordProblems } from '../sharepolicy.mjs'
import { app } from '../state'

// Live hints for the local account password policy; the server stays authoritative.
const props = defineProps({ password: { type: String, default: '' }, optional: { type: Boolean, default: false } })
const policy = computed(() => accountPolicy(app.boot))
const problems = computed(() => (props.optional && !props.password ? [] : passwordProblems(props.password, policy.value)))
defineExpose({ problems })
</script>
<template>
  <p v-if="password || !optional" class="muted" :class="{ err: password && problems.length }">{{ problems.length ? t('Needs:') + ' ' + problems.map(p => t(p.key, { n: p.n })).join(', ') : '\u2713 ' + t('Password meets the requirements') }}</p>
</template>
