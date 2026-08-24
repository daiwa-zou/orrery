import { describe, expect, it } from 'vitest'
import { metaChanges, validateLabelValue, validateMetaKey } from './labels'

describe('validateMetaKey', () => {
  it('accepts plain names and prefixed keys', () => {
    expect(validateMetaKey('app')).toBeUndefined()
    expect(validateMetaKey('app.kubernetes.io/name')).toBeUndefined()
    expect(validateMetaKey('a-b_c.d')).toBeUndefined()
    expect(validateMetaKey('A1')).toBeUndefined()
  })

  it('rejects empty keys and empty halves', () => {
    expect(validateMetaKey('')).toBeDefined()
    expect(validateMetaKey('/name')).toBeDefined()
    expect(validateMetaKey('example.com/')).toBeDefined()
  })

  it('rejects more than one slash', () => {
    expect(validateMetaKey('a/b/c')).toBeDefined()
  })

  it('rejects bad name characters and edges', () => {
    expect(validateMetaKey('-app')).toBeDefined()
    expect(validateMetaKey('app-')).toBeDefined()
    expect(validateMetaKey('a b')).toBeDefined()
  })

  it('enforces the 63-character name limit', () => {
    expect(validateMetaKey('a'.repeat(63))).toBeUndefined()
    expect(validateMetaKey('a'.repeat(64))).toBeDefined()
  })

  it('requires a lowercase DNS-subdomain prefix', () => {
    expect(validateMetaKey('Example.com/name')).toBeDefined()
    expect(validateMetaKey('example..com/name')).toBeDefined()
    expect(validateMetaKey(`${'a'.repeat(254)}/name`)).toBeDefined()
  })
})

describe('validateLabelValue', () => {
  it('accepts empty and regular values', () => {
    expect(validateLabelValue('')).toBeUndefined()
    expect(validateLabelValue('v1.2-beta_3')).toBeUndefined()
  })

  it('rejects bad edges and over-long values', () => {
    expect(validateLabelValue('-x')).toBeDefined()
    expect(validateLabelValue('x-')).toBeDefined()
    expect(validateLabelValue('a'.repeat(64))).toBeDefined()
  })
})

describe('metaChanges', () => {
  it('returns an empty patch when nothing changed', () => {
    expect(metaChanges({ a: '1' }, { a: '1' })).toEqual({})
  })

  it('carries additions and modifications, and nulls removals', () => {
    expect(metaChanges({ a: '1', b: '2' }, { a: '1', b: '3', c: '4' })).toEqual({
      b: '3',
      c: '4',
    })
    expect(metaChanges({ a: '1', b: '2' }, { a: '1' })).toEqual({ b: null })
  })
})
