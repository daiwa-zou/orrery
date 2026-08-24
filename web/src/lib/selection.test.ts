import { describe, expect, it } from 'vitest'
import { rowKey, toggleAll, toggleRow } from './selection'
import type { Row } from '../api/types'

const row = (partial: Partial<Row>): Row =>
  ({ name: 'x', uid: '', age: '', ...partial }) as Row

describe('rowKey', () => {
  it('prefers the uid', () => {
    expect(rowKey(row({ uid: 'u1', namespace: 'ns', name: 'a' }))).toBe('u1')
  })

  it('falls back to namespace/name when the server sends no uid', () => {
    expect(rowKey(row({ namespace: 'ns', name: 'a' }))).toBe('ns/a')
  })
})

describe('toggleRow', () => {
  it('adds a missing key and removes a present one', () => {
    const one = toggleRow(new Set(), 'a')
    expect(one).toEqual(new Set(['a']))
    expect(toggleRow(one, 'a')).toEqual(new Set())
  })

  it('does not mutate the input set', () => {
    const before = new Set(['a'])
    toggleRow(before, 'b')
    expect(before).toEqual(new Set(['a']))
  })
})

describe('toggleAll', () => {
  it('selects every visible key when any is unselected', () => {
    expect(toggleAll(new Set(['a']), ['a', 'b'])).toEqual(new Set(['a', 'b']))
  })

  it('deselects every visible key when all are selected', () => {
    expect(toggleAll(new Set(['a', 'b']), ['a', 'b'])).toEqual(new Set())
  })

  it('leaves keys from other pages alone in both directions', () => {
    expect(toggleAll(new Set(['other']), ['a'])).toEqual(new Set(['other', 'a']))
    expect(toggleAll(new Set(['other', 'a']), ['a'])).toEqual(new Set(['other']))
  })

  it('is a no-op for an empty page', () => {
    expect(toggleAll(new Set(['a']), [])).toEqual(new Set(['a']))
  })
})
