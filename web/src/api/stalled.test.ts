import { describe, expect, it } from 'vitest'

import { stalledReason } from './hooks'

/**
 * These pin the three-way distinction the pages depend on: a fetch in flight,
 * a fetch that failed, and a fetch that is parked and will not resume. Only
 * the third one used to render as nothing at all.
 */
describe('stalledReason', () => {
  const failure = new Error('pods "web-1" not found')

  it('reports the reason a parked retry stopped', () => {
    expect(stalledReason({ isPaused: true, failureReason: failure, data: undefined })).toBe(failure)
  })

  it('is silent while a fetch is actually in flight', () => {
    // Not paused: something is happening, and the loading state covers it.
    expect(stalledReason({ isPaused: false, failureReason: failure, data: undefined })).toBeUndefined()
  })

  // The list keeps the previous page on screen while it refetches. Replacing a
  // perfectly good table with an error because the *next* fetch parked would
  // be a worse answer than the slightly stale one already there.
  it('stays silent when there is still data to show', () => {
    expect(stalledReason({ isPaused: true, failureReason: failure, data: { items: [] } })).toBeUndefined()
    expect(stalledReason({ isPaused: true, failureReason: failure, data: null })).toBeUndefined()
  })

  // A retry can be parked before anything has failed — the browser went
  // offline between mount and first request. There is no failureReason then,
  // and "nothing to say" would put us straight back to a blank page.
  it('always has something to say when parked with no data', () => {
    const reason = stalledReason({ isPaused: true, failureReason: null, data: undefined })
    expect(reason).toBeInstanceOf(Error)
    expect(String(reason)).toContain('offline')

    expect(stalledReason({ isPaused: true, failureReason: undefined, data: undefined })).toBeInstanceOf(Error)
  })
})
