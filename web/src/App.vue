<script setup>
import { computed, ref, watch, watchEffect } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { session, logout, api } from './api'
import { app, pageTitle } from './state'
import { bundledUrl } from './backgrounds'
import { t, lang, languages, setLang } from './i18n'
import { onMounted, onBeforeUnmount, nextTick } from 'vue'
import { session as sess, refresh as refreshSession } from './api'
import { createIdleMonitor } from './idle.mjs'
import { isBusy } from './transfers.mjs'

const router = useRouter(), route = useRoute()
const ready = true
// A custom logo uploaded under Branding wins; otherwise the built-in Sendora mark.
const logoUrl = computed(() => app.boot?.has_logo ? "/api/branding/logo" : "/logo.svg")
// An uploaded custom image wins; otherwise the chosen bundled one ('none' = plain theme colour).
const bg = computed(() => app.backgrounds.length ? '/api/backgrounds/' + app.backgrounds[(new Date().getDate()) % app.backgrounds.length] : bundledUrl(app.boot?.background))

watchEffect(() => {
  const name = app.boot?.application_name || 'Sendora'
  document.title = pageTitle.value ? pageTitle.value + ' – ' + name : name
})

// The running version is shown in the footer to signed-in users only; public pages never expose it.
watch(() => session.user?.id, async id => {
  app.version = ''
  if (!id) return
  try { const v = (await api('/users/me')).version; if (session.user?.id === id) app.version = v || '' } catch {}
}, { immediate: true })
const versionLabel = computed(() => !session.user || !app.version ? '' : /^\d/.test(app.version) ? 'v' + app.version : app.version)

const languageBusy = ref(false), languageError = ref('')
watch(() => [session.user?.id, session.user?.ui_language], () => {
  if (session.user?.ui_language) setLang(session.user.ui_language)
}, { immediate: true })
async function chooseLanguage(value) {
  languageError.value = ''
  if (!session.user || session.user.is_guest) { setLang(value); return }
  languageBusy.value = true
  try {
    const saved = await api('/users/me', { method: 'PUT', body: { name: session.user.name, email: session.user.email, ui_language: value } })
    if (saved.access_token) session.token = saved.access_token
    session.user = saved.user
    setLang(saved.user.ui_language)
  } catch (e) { languageError.value = e.message }
  finally { languageBusy.value = false }
}
async function out() { await logout(); router.push('/login') }

// ---- automatic sign-out after inactivity (never while a transfer is running) ----
const idle = ref({ warning: false, seconds: 0 })
const stayBtn = ref(null)
let monitor = null, timer = null
const idleMs = () => {
  const m = Number(app.boot?.idle_logout_minutes)
  return Number.isFinite(m) && m > 0 ? m * 60 * 1000 : 0
}
function idleApplies() { return !!sess.user && !sess.user.is_guest }
async function idleTimedOut() {
  const minutes = Math.round(idleMs() / 60000)
  const next = route.fullPath
  idle.value = { warning: false, seconds: 0 }
  await logout()
  router.replace({ path: '/login', query: { next, idle: String(minutes) } })
}
function startIdle() {
  stopIdle()
  if (!idleApplies()) return
  monitor = createIdleMonitor({
    limitMs: idleMs(), isBusy, doRefresh: () => refreshSession(),
    onWarn: seconds => { const first = !idle.value.warning; idle.value = { warning: true, seconds }; if (first) nextTick(() => stayBtn.value?.focus()) },
    onClear: () => { idle.value = { warning: false, seconds: 0 } },
    onTimeout: idleTimedOut,
  })
  timer = setInterval(() => monitor?.tick(), 5000)
  // The warning countdown needs finer ticks than the 5 s inactivity check.
  countdown = setInterval(() => { if (monitor?.warning) monitor.tick() }, 1000)
}
let countdown = null
function stopIdle() {
  if (timer) clearInterval(timer)
  if (countdown) clearInterval(countdown)
  timer = countdown = monitor = null
  idle.value = { warning: false, seconds: 0 }
}
function stay() { monitor?.stay() }
const activity = () => monitor?.activity()
const ACTIVITY_EVENTS = ['pointerdown', 'pointermove', 'keydown', 'wheel', 'scroll', 'touchstart']
onMounted(() => { for (const e of ACTIVITY_EVENTS) window.addEventListener(e, activity, { passive: true, capture: true }); window.addEventListener('focus', activity) })
onBeforeUnmount(() => { for (const e of ACTIVITY_EVENTS) window.removeEventListener(e, activity, { capture: true }); window.removeEventListener('focus', activity); stopIdle() })
watch(() => [sess.user?.id, sess.user?.is_guest], startIdle, { immediate: true })
watch(() => app.boot?.idle_logout_minutes, () => { if (monitor) monitor.setLimit(idleMs()); else startIdle() })
</script>

<template>
  <div v-if="ready" class="shell" :class="{ hasbg: bg }" :style="bg ? { '--shell-bg': `url(${bg})` } : null">
    <header v-if="session.user && !(session.user.is_guest)" class="nav">
      <router-link to="/" class="brand">
        <img :src="logoUrl" alt="" class="logo" />
        <span>{{ app.boot?.application_name }}</span>
      </router-link>
      <nav>
        <router-link to="/">{{ t('Upload') }}</router-link>
        <router-link to="/shares">{{ t('My shares') }}</router-link>
        <router-link v-if="session.user.admin" to="/admin">{{ t('Admin') }}</router-link>
        <router-link to="/profile">{{ session.user.name || session.user.username }}</router-link>
        <select v-if="app.boot?.show_language_selector" :value="lang" @change="chooseLanguage($event.target.value)" :disabled="languageBusy" :aria-label="t('Language')">
          <option v-for="(n, k) in languages" :key="k" :value="k">{{ n }}</option>
        </select>
        <button class="link" @click="out">{{ t('Sign out') }}</button>
      </nav>
    </header>
    <header v-else class="nav">
      <router-link to="/" class="brand"><img :src="logoUrl" alt="" class="logo" /><span>{{ app.boot?.application_name || 'Sendora' }}</span></router-link>
      <nav v-if="!session.user && !app.boot?.setup_needed"><router-link to="/login">{{ t('Sign in') }}</router-link><router-link v-if="app.boot?.registration_available" to="/register">{{ t('Create account') }}</router-link></nav>
    </header>
    <div v-if="idle.warning" class="idle-modal" role="alertdialog" aria-modal="true" aria-labelledby="idle-title" aria-describedby="idle-desc">
      <div class="idle-box card">
        <h2 id="idle-title">{{ t('Still there?') }}</h2>
        <p id="idle-desc" aria-live="assertive">{{ t('You will be signed out in {n} seconds', { n: idle.seconds }) }}</p>
        <div class="actions"><button ref="stayBtn" type="button" @click="stay">{{ t('Stay signed in') }}</button></div>
      </div>
    </div>
    <main><p v-if="languageError" class="err" role="alert">{{ languageError }}</p><router-view :key="route.fullPath" /></main>
    <footer class="foot">
      <p>{{ t('© {year} Sendora contributors', { year: new Date().getFullYear() }) }} · {{ t('Open source') }} · MIT License<template v-if="versionLabel"> · {{ versionLabel }}</template></p>
      <select v-if="app.boot?.show_language_selector && !session.user" :value="lang" @change="chooseLanguage($event.target.value)" :disabled="languageBusy" :aria-label="t('Language')">
        <option v-for="(n, k) in languages" :key="k" :value="k">{{ n }}</option>
      </select>
    </footer>
  </div>
</template>
