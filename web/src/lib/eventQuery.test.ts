import { describe, expect, it } from 'vitest'
import {
  addWhereTerm,
  columnValues,
  eventTermProblem,
  freeTextOf,
  removeWhereTerm,
  summarizeEvents,
  tokenizeEvents,
  valueTerm,
} from './eventQuery'

const columns = [
  { key: 'type', type: 'status' },
  { key: 'reason', type: 'text' },
  { key: 'object', type: 'text' },
  { key: 'message', type: 'text' },
  { key: 'count', type: 'number' },
  { key: 'lastSeen', type: 'age' },
]

describe('tokenizeEvents', () => {
  it('splits on whitespace and keeps a quoted phrase whole', () => {
    expect(tokenizeEvents('back-off web')).toEqual(['back-off', 'web'])
    expect(tokenizeEvents('web "failed to mount"')).toEqual(['web', 'failed to mount'])
  })

  it('runs an unterminated quote to the end, because it is being typed', () => {
    expect(tokenizeEvents('"failed to')).toEqual(['failed to'])
  })

  it('has nothing to say about empty input', () => {
    expect(tokenizeEvents('   ')).toEqual([])
  })
})

describe('freeTextOf', () => {
  it('leaves predicates out of the words to search for', () => {
    expect(freeTextOf('back-off count>3 web')).toBe('back-off web')
  })

  it('excludes a predicate from the moment it has an operator', () => {
    // `count>` on its way to `count>3` must not be searched for as text: the
    // list would empty on the keystroke that starts the filter.
    expect(freeTextOf('count>')).toBe('')
  })

  it('re-quotes a phrase, which is what tells the server it is one thing', () => {
    expect(freeTextOf('"failed to mount" web')).toBe('"failed to mount" web')
  })
})

describe('eventTermProblem', () => {
  it('accepts predicates over the columns the feed has', () => {
    expect(eventTermProblem('count>3', columns)).toBeUndefined()
    expect(eventTermProblem('lastSeen<15m', columns)).toBeUndefined()
    expect(eventTermProblem('reason=~^Failed', columns)).toBeUndefined()
  })

  it('names a column that is not here', () => {
    expect(eventTermProblem('restarts>3', columns)).toMatch(/no restarts column/)
  })

  it('refuses to order text rather than answering lexicographically', () => {
    expect(eventTermProblem('reason>a', columns)).toMatch(/text/)
  })

  it('teaches the operator that works instead of searching for the literal', () => {
    // The first thing anyone who has used the resource search will type.
    expect(eventTermProblem('reason=BackOff', columns)).toMatch(/reason=~BackOff/)
  })

  it('leaves ordinary words, and words containing =, alone', () => {
    expect(eventTermProblem('back-off', columns)).toBeUndefined()
    expect(eventTermProblem('app=web', columns)).toBeUndefined()
  })

  it('checks nothing until the first page has arrived', () => {
    expect(eventTermProblem('restarts>3', undefined)).toBeUndefined()
  })
})

describe('where terms', () => {
  it('anchors a value picked from the feed and escapes it', () => {
    expect(valueTerm('reason', 'BackOff')).toBe('reason=~^BackOff$')
    expect(valueTerm('object', 'Pod/web.1')).toBe('object=~^Pod/web\\.1$')
  })

  it('drops an exact repeat rather than filtering twice by the same thing', () => {
    const once = addWhereTerm({ q: '', where: [] }, 'count>3')
    expect(addWhereTerm(once, 'count>3')).toEqual(once)
  })

  it('removes by value, so a re-render cannot drop the wrong term', () => {
    const q = { q: '', where: ['count>3', 'lastSeen<1h'] }
    expect(removeWhereTerm(q, 'count>3').where).toEqual(['lastSeen<1h'])
  })
})

describe('columnValues', () => {
  it('offers what the feed actually holds, most common first', () => {
    const rows = [
      { reason: 'BackOff' },
      { reason: 'Pulled' },
      { reason: 'BackOff' },
      { reason: '' },
      { count: 3 },
    ]
    expect(columnValues(rows, 'reason')).toEqual(['BackOff', 'Pulled'])
  })
})

describe('summarizeEvents', () => {
  const rows = [
    { type: 'Warning', reason: 'BackOff' },
    { type: 'Warning', reason: 'BackOff' },
    { type: 'Normal', reason: 'Pulled' },
    { type: 'Normal', reason: 'Started' },
  ]

  it('counts the types and the reasons behind them', () => {
    const summary = summarizeEvents(rows)
    expect(summary).toMatchObject({ warnings: 2, normal: 2 })
    expect(summary.reasons[0]).toEqual({ reason: 'BackOff', count: 2, warnings: 2 })
    expect(summary.reasons.map((r) => r.reason)).toEqual(['BackOff', 'Pulled', 'Started'])
  })

  it('counts rows rather than repeats, which are a different number', () => {
    const summary = summarizeEvents([{ type: 'Warning', reason: 'BackOff', count: 97 }])
    expect(summary.reasons[0].count).toBe(1)
  })

  it('says nothing about an empty feed', () => {
    expect(summarizeEvents([])).toEqual({ warnings: 0, normal: 0, reasons: [] })
  })
})
