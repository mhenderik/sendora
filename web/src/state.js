import { reactive, ref } from 'vue'
import { api } from './api'
import { setLang, languages, loadLanguageList } from './i18n'

export const emailLanguages = reactive({ ...languages })

// Optional per-page title prefix (upload progress, share name); App.vue combines it with the application name.
export const pageTitle = ref('')

export const app = reactive({ boot: null, theme: null, backgrounds: [] })

// Pick the higher-contrast foreground for both bright and dark custom accents.
export function contrastText(color) {
  if (!/^#(?:[\da-f]{3}|[\da-f]{6})$/i.test(color || '')) return '#ffffff'
  let hex = color.slice(1)
  if (hex.length === 3) hex = [...hex].map(c => c + c).join('')
  const linear = [0, 2, 4].map(i => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  const luminance = linear[0] * 0.2126 + linear[1] * 0.7152 + linear[2] * 0.0722
  return (luminance + 0.05) / 0.05 >= 1.05 / (luminance + 0.05) ? '#000000' : '#ffffff'
}

export function applyTheme(th) {
  app.theme = th
  const root = document.documentElement.style
  const map = { primary: '--primary', bg: '--bg', surface: '--surface', text: '--text', muted: '--muted', border: '--border', danger: '--danger' }
  for (const k in map) th?.data?.[k] ? root.setProperty(map[k], th.data[k]) : root.removeProperty(map[k])
  for (const k of ['primary', 'danger']) {
    if (th?.data?.[k]) root.setProperty(`--${k}-text`, contrastText(th.data[k]))
    else root.removeProperty(`--${k}-text`)
  }
}

export async function loadBoot() {
  const [boot, theme, bgs, mailLanguages] = await Promise.all([
    api('/bootstrap'), api('/themes/active').catch(() => null), api('/backgrounds').catch(() => []), api('/auth/email-languages')
  ])
  for (const key of Object.keys(emailLanguages)) delete emailLanguages[key]
  for (const item of mailLanguages) emailLanguages[item.code] = item.name
  app.boot = boot
  await loadLanguageList()
  app.backgrounds = bgs || []
  applyTheme(theme)
  document.title = boot.application_name
  // A custom favicon (Branding) replaces the built-in icons from index.html; without
  // one, the built-in Sendora icon stays or comes back.
  const icons = [...document.querySelectorAll('link[rel=icon]')]
  const custom = icons.filter(l => (l.getAttribute('href') || '').startsWith('/api/branding/'))
  const add = href => { const l = document.createElement('link'); l.rel = 'icon'; l.href = href; document.head.appendChild(l) }
  if (boot.has_favicon) {
    icons.forEach(l => l.remove())
    add('/api/branding/favicon?v=' + Date.now())
  } else {
    custom.forEach(l => l.remove())
    if (icons.length === custom.length) add('/favicon.svg')
  }
  let saved = null
  try { saved = localStorage.getItem('lang') } catch {}
  if (!saved && boot.default_language) setLang(boot.default_language)
}
