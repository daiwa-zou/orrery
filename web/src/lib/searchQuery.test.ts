import { describe, expect, it } from 'vitest'
import {
  addQueryTerm,
  composeSearchInput,
  freeTextOf,
  isFilterTerm,
  parseSearchInput,
  queryTerms,
  removeQueryTerm,
  tokenizeSearch,
  trailingToken,
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

  it('names the term it rejected, and why', () => {
    const { problems } = parseSearchInput('web app=We!b tier=cache')
    expect(problems).toHaveLength(1)
    expect(problems[0].term).toBe('app=We!b')
    expect(problems[0].reason).toMatch(/label value/i)
  })

  it('reports a bad key in an exists-negation and in a set expression', () => {
    expect(parseSearchInput('!Not/A/Key').problems[0]).toMatchObject({
      term: '!Not/A/Key',
    })
    expect(parseSearchInput('Not/A/Key in (a,b)').problems[0]).toMatchObject({
      term: 'Not/A/Key in (a,b)',
    })
  })

  it('reports every rejected term, not just the first', () => {
    const { problems } = parseSearchInput('app=We!b tier=Ca!che')
    expect(problems.map((p) => p.term)).toEqual(['app=We!b', 'tier=Ca!che'])
  })

  it('leaves problems empty for anything committable', () => {
    for (const input of ['', 'web', 'app=web', '!canary', 'app in (a,b)', 'status.phase=Running']) {
      expect(parseSearchInput(input).problems).toEqual([])
    }
  })

  it('keeps the valid terms of a partly-invalid input, so the bar can still show them', () => {
    expect(parseSearchInput('web app=We!b tier=cache')).toMatchObject({
      q: 'web',
      labelSelector: 'tier=cache',
      committable: false,
    })
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

describe('queryTerms', () => {
  const query = {
    q: 'web frontend',
    labelSelector: 'app=web,tier!=cache,role in (a,b)',
    fieldSelector: 'status.phase=Running',
  }

  it('lists selectors before free text, each tagged with where it came from', () => {
    expect(queryTerms(query)).toEqual([
      { kind: 'label', term: 'app=web' },
      { kind: 'label', term: 'tier!=cache' },
      { kind: 'label', term: 'role in (a,b)' },
      { kind: 'field', term: 'status.phase=Running' },
      { kind: 'text', term: 'web' },
      { kind: 'text', term: 'frontend' },
    ])
  })

  it('is empty for an empty query', () => {
    expect(queryTerms({ q: '', labelSelector: '', fieldSelector: '' })).toEqual([])
  })

  it('keeps a set expression whole rather than splitting it at its comma', () => {
    const terms = queryTerms({ q: '', labelSelector: 'role in (a,b)', fieldSelector: '' })
    expect(terms).toEqual([{ kind: 'label', term: 'role in (a,b)' }])
  })

  it('round-trips: every term reappears in the composed input', () => {
    const text = composeSearchInput(query)
    for (const { term } of queryTerms(query)) expect(text).toContain(term)
  })
})

describe('removeQueryTerm', () => {
  const query = {
    q: 'web frontend',
    labelSelector: 'app=web,tier!=cache',
    fieldSelector: 'status.phase=Running',
  }

  it('drops a label term and leaves the rest alone', () => {
    expect(removeQueryTerm(query, { kind: 'label', term: 'app=web' })).toEqual({
      q: 'web frontend',
      labelSelector: 'tier!=cache',
      fieldSelector: 'status.phase=Running',
    })
  })

  it('drops a field term', () => {
    expect(
      removeQueryTerm(query, { kind: 'field', term: 'status.phase=Running' }).fieldSelector,
    ).toBe('')
  })

  it('drops one free-text word and keeps the other', () => {
    expect(removeQueryTerm(query, { kind: 'text', term: 'web' }).q).toBe('frontend')
  })

  it('does not remove a matching string from the wrong kind', () => {
    const ambiguous = { q: 'app=web', labelSelector: 'app=web', fieldSelector: '' }
    expect(removeQueryTerm(ambiguous, { kind: 'label', term: 'app=web' })).toEqual({
      q: 'app=web',
      labelSelector: '',
      fieldSelector: '',
    })
  })

  it('removing every term leaves a query that composes to an empty string', () => {
    let next: typeof query = query
    for (const term of queryTerms(query)) next = removeQueryTerm(next, term)
    expect(composeSearchInput(next)).toBe('')
  })
})

describe('isFilterTerm', () => {
  it('is true for label, field, negation and set terms', () => {
    for (const t of ['app=web', 'tier!=cache', '!canary', 'app in (a,b)', 'status.phase=Running']) {
      expect(isFilterTerm(t)).toBe(true)
    }
  })

  it('is false for free text and for something the parser refuses', () => {
    for (const t of ['web', 'app', '', 'app=We!']) {
      expect(isFilterTerm(t)).toBe(false)
    }
  })
})

describe('freeTextOf', () => {
  it('keeps the words and drops the terms', () => {
    expect(freeTextOf('nginx app=web error status.phase=Running')).toBe('nginx error')
  })

  it('drops a term the moment it reads as one, so it is never searched as text', () => {
    expect(freeTextOf('app=')).toBe('')
    expect(freeTextOf('app=w')).toBe('')
  })

  it('keeps a half-written term that is not yet a term', () => {
    expect(freeTextOf('app')).toBe('app')
  })

  it('keeps text the parser refused, since it is not applied as a filter', () => {
    expect(freeTextOf('app=We!')).toBe('app=We!')
  })
})

describe('trailingToken', () => {
  it('is the token under the cursor at the end of the input', () => {
    expect(trailingToken('nginx app=we')).toBe('app=we')
    expect(trailingToken('nginx ')).toBe('')
    expect(trailingToken('')).toBe('')
    expect(trailingToken('one')).toBe('one')
  })
})

describe('addQueryTerm', () => {
  const empty = { q: '', labelSelector: '', fieldSelector: '' }

  it('routes a label term to the label selector', () => {
    expect(addQueryTerm(empty, 'app=web')).toEqual({
      q: '',
      labelSelector: 'app=web',
      fieldSelector: '',
    })
  })

  it('routes a field term to the field selector', () => {
    expect(addQueryTerm(empty, 'status.phase=Running')).toEqual({
      q: '',
      labelSelector: '',
      fieldSelector: 'status.phase=Running',
    })
  })

  it('appends rather than replacing, and leaves free text alone', () => {
    const start = { q: 'nginx', labelSelector: 'app=web', fieldSelector: '' }
    expect(addQueryTerm(start, 'tier!=cache')).toEqual({
      q: 'nginx',
      labelSelector: 'app=web,tier!=cache',
      fieldSelector: '',
    })
  })

  it('adding then removing a term is a round trip', () => {
    const start = { q: '', labelSelector: 'app=web', fieldSelector: '' }
    const added = addQueryTerm(start, 'status.phase=Running')
    expect(removeQueryTerm(added, { kind: 'field', term: 'status.phase=Running' })).toEqual(start)
  })

  it('every added term shows up as its own chip', () => {
    let q = empty
    for (const t of ['app=web', 'tier!=cache', 'status.phase=Running']) q = addQueryTerm(q, t)
    expect(queryTerms(q).map((t) => t.term)).toEqual([
      'app=web',
      'tier!=cache',
      'status.phase=Running',
    ])
  })
})
