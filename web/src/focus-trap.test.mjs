import test from 'node:test'
import assert from 'node:assert/strict'
import { wrapFocusIndex } from './focus-trap.mjs'

test('Tab on the last control wraps to the first, Shift+Tab on the first to the last', () => {
  assert.equal(wrapFocusIndex(2, 3, false), 0)
  assert.equal(wrapFocusIndex(0, 3, true), 2)
})

test('moves inside the dialog are left to the browser', () => {
  assert.equal(wrapFocusIndex(0, 3, false), -1)
  assert.equal(wrapFocusIndex(2, 3, true), -1)
})

test('focus outside the dialog is pulled back in', () => {
  assert.equal(wrapFocusIndex(-1, 2, false), 0)
  assert.equal(wrapFocusIndex(-1, 2, true), 1)
  assert.equal(wrapFocusIndex(-1, 0, false), -1)
})

test('a single control keeps focus', () => {
  assert.equal(wrapFocusIndex(0, 1, false), 0)
  assert.equal(wrapFocusIndex(0, 1, true), 0)
})
