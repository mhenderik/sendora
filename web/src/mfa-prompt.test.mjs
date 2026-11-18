import test from 'node:test'
import assert from 'node:assert/strict'
import { shouldPromptMFA, mfaPromptKey } from './mfa-prompt.mjs'

const boot = { mfa_totp_enabled: true, mfa_webauthn_enabled: false }
const user = { id: 3, mfa_enabled: false }
const base = { user, hasPassword: true, boot, dismissed: false }

test('asks a local account without a second factor', () => {
  assert.equal(shouldPromptMFA(base), true)
})

test('does not ask accounts that sign in through an identity provider only', () => {
  assert.equal(shouldPromptMFA({ ...base, hasPassword: false }), false)
})

test('does not ask when a factor exists, when enrolment is already forced, or after dismissal', () => {
  assert.equal(shouldPromptMFA({ ...base, user: { ...user, mfa_enabled: true } }), false)
  assert.equal(shouldPromptMFA({ ...base, user: { ...user, mfa_enroll_required: true } }), false)
  assert.equal(shouldPromptMFA({ ...base, dismissed: true }), false)
})

test('does not ask guests or auditors, or when no method can be enrolled', () => {
  assert.equal(shouldPromptMFA({ ...base, user: { ...user, is_guest: true } }), false)
  assert.equal(shouldPromptMFA({ ...base, user: { ...user, auditor: true } }), false)
  assert.equal(shouldPromptMFA({ ...base, boot: { mfa_totp_enabled: false, mfa_webauthn_enabled: false } }), false)
  assert.equal(shouldPromptMFA({ ...base, user: null }), false)
})

test('dismissal is remembered per user', () => {
  assert.notEqual(mfaPromptKey(1), mfaPromptKey(2))
})
