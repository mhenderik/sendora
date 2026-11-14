import { reactive } from 'vue'
import { translateMessage } from './i18n/mark.mjs'

export const session = reactive({ token: null, user: null, ready: false })
let refreshing = null

async function raw(path, opts = {}) {
  const headers = { ...(opts.headers || {}) }
  if (session.token) headers.Authorization = 'Bearer ' + session.token
  let body = opts.body
  if (body && !(body instanceof FormData) && typeof body !== 'string' && !(body instanceof Blob) && !(body instanceof ArrayBuffer)) {
    body = JSON.stringify(body)
    headers['Content-Type'] = 'application/json'
  }
  return fetch('/api' + path, { ...opts, headers, body, credentials: 'same-origin' })
}

export async function refresh() {
  refreshing ??= fetch('/api/auth/refresh', { method: 'POST' })
    .then(async r => {
      if (!r.ok) throw 0
      const d = await r.json()
      session.token = d.access_token
      session.user = d.user
      return true
    })
    .catch(() => { session.token = null; session.user = null; return false })
    .finally(() => { refreshing = null })
  return refreshing
}

// The message is translated centrally when a translation exists; `raw` keeps the
// original server text for logic that must not depend on the UI language.
export class ApiError extends Error {
  constructor(status, data) {
    const raw = data?.error || 'Request failed'
    super(translateMessage(raw)); this.status = status; this.data = data; this.raw = raw
  }
}

export async function api(path, opts = {}) {
  let r = await raw(path, opts)
  if (r.status === 401 && session.token && !opts.noRetry) {
    if (await refresh()) r = await raw(path, opts)
  }
  const ct = r.headers.get('content-type') || ''
  const data = ct.includes('json') ? await r.json().catch(() => null) : null
  if (!r.ok) throw new ApiError(r.status, data)
  return data
}

export function setSession(d) { session.token = d.access_token; session.user = d.user }

export async function logout() {
  const headers = session.token ? { Authorization: 'Bearer ' + session.token } : {}
  await fetch('/api/auth/logout', { method: 'POST', headers }).catch(() => {})
  session.token = null; session.user = null
}

// download via fetch so the Authorization/share headers are kept; returns blob url trigger
export async function saveBlob(path, filename, headers = {}) {
  const r = await fetch('/api' + path, { headers })
  if (!r.ok) throw new ApiError(r.status, await r.json().catch(() => null))
  const blob = await r.blob()
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob); a.download = filename; a.click()
  setTimeout(() => URL.revokeObjectURL(a.href), 10000)
}

export function fmtBytes(n) {
  if (n == null) return ''
  const u = ['B', 'KB', 'MB', 'GB', 'TB']; let i = 0
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++ }
  return (i ? n.toFixed(1) : n) + ' ' + u[i]
}
let localeGetter = () => undefined
export const setLocaleGetter = fn => { localeGetter = typeof fn === 'function' ? fn : () => undefined }
export const fmtDate = d => d ? new Date(d).toLocaleString(localeGetter()) : '—'
