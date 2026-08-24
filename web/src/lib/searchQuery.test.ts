import { describe, expect, it } from 'vitest'
import {
  composeSearchInput,
  parseSearchInput,
  tokenizeSearch,
} from './searchQuery'

describe('tokenizeSearch', () => {
  it('splits on whitespace', () => {
    expect(tokenizeSearch('  web app=x  ')).toEqual(['web', 'app=x'])
  })

  it('keeps set expressions together, including spaced values', () => {
    expect(tokenizeSearch('app in (a, b) web')).toEqual(['app in (a,b)', 'web'])
    expect(tokenizeSearch('tier notin (cache)')).toEqual(['tier notin (cache)'])
  })
})

describe('parseSearchInput', () => {
  it('routes bare words to q', () => {
    expect(parseSearchInput('web frontend')).toMatchObject({
      q: 'web frontend',
      labelSelector: '',
      fieldSelector: '',
      committable: true,
    })
  })

  it('routes dotless equality terms to the label selector', () => {
    expect(parseSearchInput('app=web tier!=cache')).toMatchObject({
      labelSelector: 'app=web,tier!=cache',
      q: '',
    })
  })

  it('routes supported dotted keys to the field selector', () => {
    expect(parseSearchInput('status.phase=Running spec.nodeName!=node-1')).toMatchObject({
      fieldSelector: 'status.phase=Running,spec.nodeName!=node-1',
      labelSelector: '',
    })
  })

  it('normalizes == to = for field terms', () => {
    expect(parseSearchInput('status.phase==Running').fieldSelector).toBe(
      'status.phase=Running',
    )
  })

  it('treats unsupported dotted keys as free text instead of 400ing', () => {
    expect(parseSearchInput('spec.serviceAccountName=x')).toMatchObject({
      q: 'spec.serviceAccountName=x',
      fieldSelector: '',
      committable: true,
    })
  })

  it('routes slash-prefixed keys to the label selector', () => {
    expect(parseSearchInput('app.kubernetes.io/name=web').labelSelector).toBe(
      'app.kubernetes.io/name=web',
    )
  })

  it('supports exists-negation and set expressions', () => {
    expect(parseSearchInput('!canary app in (web, api)').labelSelector).toBe(
      '!canary,app in (web,api)',
    )
  })

  it('holds commit while a label value is invalid mid-edit', () => {
    expect(parseSearchInput('app=We!b').committable).toBe(false)
  })

  it('allows an empty label value (matches empty-valued labels)', () => {
    expect(parseSearchInput('app=')).toMatchObject({
      labelSelector: 'app=',
      committable: true,
    })
  })

  it('bare `type=x` is a label term, not the field selector', () => {
    expect(parseSearchInput('type=kubernetes.io')).toMatchObject({
      labelSelector: 'type=kubernetes.io',
      fieldSelector: '',
    })
  })
})

describe('composeSearchInput', () => {
  it('round-trips through parse', () => {
    const query = {
      q: 'web',
      labelSelector: 'app=web,tier!=cache',
      fieldSelector: 'status.phase=Running',
    }
    const text = composeSearchInput(query)
    expect(text).toBe('app=web tier!=cache status.phase=Running web')
    expect(parseSearchInput(text)).toMatchObject(query)
  })

  it('keeps set expressions intact', () => {
    const query = { q: '', labelSelector: 'app in (a,b)', fieldSelector: '' }
    expect(parseSearchInput(composeSearchInput(query))).toMatchObject(query)
  })

  it('composes an empty query to an empty string', () => {
    expect(composeSearchInput({ q: '', labelSelector: '', fieldSelector: '' })).toBe('')
  })
})
