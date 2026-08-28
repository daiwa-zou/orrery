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
  parseWhereTerm,
  sameQuery,
  whereProblem,
  type SearchQuery,
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
    where: [],
    }
    const text = composeSearchInput(query)
    expect(text).toBe('app=web tier!=cache status.phase=Running web')
    expect(parseSearchInput(text)).toMatchObject(query)
  })

  it('keeps set expressions intact', () => {
    const query = { q: '', labelSelector: 'app in (a,b)', fieldSelector: '', where: [] }
    expect(parseSearchInput(composeSearchInput(query))).toMatchObject(query)
  })

  it('composes an empty query to an empty string', () => {
    expect(composeSearchInput({ q: '', labelSelector: '', fieldSelector: '', where: [] })).toBe('')
  })
})

describe('queryTerms', () => {
  const query = {
    q: 'web frontend',
    labelSelector: 'app=web,tier!=cache,role in (a,b)',
    fieldSelector: 'status.phase=Running',
    where: [],
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
    expect(queryTerms({ q: '', labelSelector: '', fieldSelector: '', where: [] })).toEqual([])
  })

  it('keeps a set expression whole rather than splitting it at its comma', () => {
    const terms = queryTerms({ q: '', labelSelector: 'role in (a,b)', fieldSelector: '', where: [] })
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
    where: [],
  }

  it('drops a label term and leaves the rest alone', () => {
    expect(removeQueryTerm(query, { kind: 'label', term: 'app=web' })).toEqual({
      q: 'web frontend',
      labelSelector: 'tier!=cache',
      fieldSelector: 'status.phase=Running',
    where: [],
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
    const ambiguous = { q: 'app=web', labelSelector: 'app=web', fieldSelector: '', where: [] }
    expect(removeQueryTerm(ambiguous, { kind: 'label', term: 'app=web' })).toEqual({
      q: 'app=web',
      labelSelector: '',
      fieldSelector: '',
    where: [],
    })
  })

  it('removing every term leaves a query that composes to an empty string', () => {
    let next: SearchQuery = query
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
  const empty = { q: '', labelSelector: '', fieldSelector: '', where: [] }

  it('routes a label term to the label selector', () => {
    expect(addQueryTerm(empty, 'app=web')).toEqual({
      q: '',
      labelSelector: 'app=web',
      fieldSelector: '',
    where: [],
    })
  })

  it('routes a field term to the field selector', () => {
    expect(addQueryTerm(empty, 'status.phase=Running')).toEqual({
      q: '',
      labelSelector: '',
      fieldSelector: 'status.phase=Running',
    where: [],
    })
  })

  it('appends rather than replacing, and leaves free text alone', () => {
    const start = { q: 'nginx', labelSelector: 'app=web', fieldSelector: '', where: [] }
    expect(addQueryTerm(start, 'tier!=cache')).toEqual({
      q: 'nginx',
      labelSelector: 'app=web,tier!=cache',
      fieldSelector: '',
    where: [],
    })
  })

  it('adding then removing a term is a round trip', () => {
    const start = { q: '', labelSelector: 'app=web', fieldSelector: '', where: [] }
    const added = addQueryTerm(start, 'status.phase=Running')
    expect(removeQueryTerm(added, { kind: 'field', term: 'status.phase=Running' })).toEqual(start)
  })

  it('every added term shows up as its own chip', () => {
    let q: SearchQuery = empty
    for (const t of ['app=web', 'tier!=cache', 'status.phase=Running']) q = addQueryTerm(q, t)
    expect(queryTerms(q).map((t) => t.term)).toEqual([
      'app=web',
      'tier!=cache',
      'status.phase=Running',
    ])
  })
})

describe('column predicates', () => {
  it('reads every operator, longest first', () => {
    expect(parseWhereTerm('restarts>=3')).toEqual({ column: 'restarts', op: '>=', value: '3' })
    expect(parseWhereTerm('restarts>3')).toEqual({ column: 'restarts', op: '>', value: '3' })
    expect(parseWhereTerm('age<=1h')).toEqual({ column: 'age', op: '<=', value: '1h' })
    expect(parseWhereTerm('name=~^web')).toEqual({ column: 'name', op: '=~', value: '^web' })
    expect(parseWhereTerm('name!~canary')).toEqual({ column: 'name', op: '!~', value: 'canary' })
  })

  // The whole reason the two languages can share one box: a selector's
  // operators must not be read as a predicate's.
  it('leaves selector terms alone', () => {
    for (const t of ['app=web', 'tier!=cache', '!canary', 'app in (a,b)', 'status.phase=Running']) {
      expect(parseWhereTerm(t)).toBeUndefined()
    }
  })

  it('is not fooled by an operator inside a label value', () => {
    // `app=a>b` is a label term with an illegal value, not a predicate on a
    // column called "app=a" — saying so would send the reader after the
    // wrong mistake.
    expect(parseWhereTerm('app=a>b')).toBeUndefined()
  })

  it('needs both a column and a value', () => {
    expect(parseWhereTerm('>3')).toBeUndefined()
    expect(parseWhereTerm('restarts>')).toBeUndefined()
  })

  it('routes predicates to where, leaving the selectors untouched', () => {
    expect(parseSearchInput('app=web restarts>3 nginx')).toMatchObject({
      labelSelector: 'app=web',
      where: ['restarts>3'],
      q: 'nginx',
    })
  })

  it('gives each predicate its own chip and removes it by value', () => {
    const query = parseSearchInput('restarts>3 name=~^web-')
    expect(queryTerms(query).filter((t) => t.kind === 'where').map((t) => t.term)).toEqual([
      'restarts>3',
      'name=~^web-',
    ])
    expect(removeQueryTerm(query, { kind: 'where', term: 'restarts>3' }).where).toEqual([
      'name=~^web-',
    ])
  })

  it('does not search for a predicate that is still being written', () => {
    // Picking `age>` from the dropdown must not filter the list down to the
    // objects whose name contains the text "age>", which is none of them.
    expect(freeTextOf('age>')).toBe('')
    expect(freeTextOf('nginx restarts>')).toBe('nginx')
    expect(parseWhereTerm('age>')).toBeUndefined()
  })

  it('counts as a filter term, so it is promoted to a chip like any other', () => {
    expect(isFilterTerm('restarts>3')).toBe(true)
    expect(isFilterTerm('name=~^web-')).toBe(true)
    expect(freeTextOf('nginx restarts>3')).toBe('nginx')
  })

  it('round-trips through the composed input', () => {
    const query = parseSearchInput('app=web restarts>3 name=~^web- nginx')
    expect(parseSearchInput(composeSearchInput(query))).toMatchObject({
      labelSelector: 'app=web',
      where: ['restarts>3', 'name=~^web-'],
      q: 'nginx',
    })
  })

  it('two queries differing only by a predicate are not the same query', () => {
    const a = parseSearchInput('restarts>3')
    const b = parseSearchInput('restarts>4')
    expect(sameQuery(a, b)).toBe(false)
    expect(sameQuery(a, parseSearchInput('restarts>3'))).toBe(true)
  })
})

describe('whereProblem', () => {
  const columns = [
    { key: 'name', type: 'text' },
    { key: 'restarts', type: 'number' },
    { key: 'age', type: 'age' },
  ]

  it('accepts what the server would accept', () => {
    for (const t of ['restarts>3', 'age<1h', 'name=~^web-', 'name!~canary', 'age>=30s']) {
      expect(whereProblem(t, columns)).toBeUndefined()
    }
  })

  it('names the columns that do exist when one does not', () => {
    const msg = whereProblem('restart>1', columns)
    expect(msg).toMatch(/no restart column/)
    expect(msg).toMatch(/restarts/)
  })

  it('refuses ordering a text column, and says what would work', () => {
    expect(whereProblem('name>abc', columns)).toMatch(/=~/)
  })

  it('refuses a value the column cannot hold', () => {
    expect(whereProblem('restarts>abc', columns)).toMatch(/is a number/)
    expect(whereProblem('age>banana', columns)).toMatch(/not a duration/)
  })

  it('refuses a pattern that will not compile', () => {
    expect(whereProblem('name=~[unclosed', columns)).toMatch(/not a valid pattern/)
  })

  // Before the first page arrives there is nothing to check against, and
  // guessing would reject terms that are perfectly good.
  it('withholds judgement until the columns are known', () => {
    expect(whereProblem('restarts>3', undefined)).toBeUndefined()
    expect(whereProblem('anything>3', [])).toBeUndefined()
  })

  it('has no opinion about a term that is not a predicate', () => {
    expect(whereProblem('app=web', columns)).toBeUndefined()
  })
})
