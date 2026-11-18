import { reactive } from 'vue'
import { translateMessage } from './i18n/mark.mjs'
import { filenameFromDisposition } from './content-disposition.mjs'

// endReason: why the server ended the session at the last refresh (for example a
// disabled account or a provider sign-in that must be renewed), shown on the login page.
export const session = reactive({ token: null, user: null, ready: false, endReason: '' })
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
      if (!r.ok) {
        const e = await r.json().catch(() => null)
        session.endReason = e?.error || ''
        throw 0
      }
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

// One request with a single refresh-and-retry when the access token has expired.
async function request(path, opts = {}) {
  let r = await raw(path, opts)
  if (r.status === 401 && session.token && !opts.noRetry) {
    if (await refresh()) r = await raw(path, opts)
  }
  return r
}

async function errorData(r) {
  const ct = r.headers.get('content-type') || ''
  return ct.includes('json') ? await r.json().catch(() => null) : null
}

export async function api(path, opts = {}) {
  const r = await request(path, opts)
  const data = await errorData(r)
  if (!r.ok) throw new ApiError(r.status, data)
  return data
}

// Fetches a file with the same authentication, retry and (translated) errors as api().
// Returns the blob and the file name from Content-Disposition ('' when absent).
export async function fetchBlob(path, opts = {}) {
  const r = await request(path, opts)
  if (!r.ok) throw new ApiError(r.status, await errorData(r))
  return { blob: await r.blob(), filename: filenameFromDisposition(r.headers.get('content-disposition')) }
}

// Downloads a file through fetchBlob and hands it to the browser as a save; returns the file name used.
export async function downloadBlob(path, fallbackName, opts = {}) {
  const { blob, filename } = await fetchBlob(path, opts)
  const name = filename || fallbackName
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob); a.download = name
  document.body.appendChild(a); a.click(); a.remove()
  setTimeout(() => URL.revokeObjectURL(a.href), 10000)
  return name
}

export function setSession(d) { session.token = d.access_token; session.user = d.user }

export async function logout() {
  const headers = session.token ? { Authorization: 'Bearer ' + session.token } : {}
  await fetch('/api/auth/logout', { method: 'POST', headers }).catch(() => {})
  session.token = null; session.user = null
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
