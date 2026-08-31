import { describe, expect, it } from 'vitest'

import { metricsState } from './metricsState'

describe('metricsState', () => {
  it('is ready when the cluster answered with metrics', () => {
    expect(metricsState({ data: { available: true, nodes: [] }, isLoading: false }))
      .toEqual({ kind: 'ready' })
  })

  // The cluster was asked and said no. This is the only case that may claim
  // anything about the cluster, and it repeats the server's own wording.
  it('reports absence only when the cluster said so', () => {
    const state = metricsState({
      data: { available: false, reason: 'metrics-server is not installed or not responding on this cluster' },
      isLoading: false,
    })
    expect(state.kind).toBe('absent')
    expect(state).toMatchObject({ reason: expect.stringContaining('metrics-server') })
  })

  it('still says something when the cluster gives no reason', () => {
    const state = metricsState({ data: { available: false }, isLoading: false })
    expect(state.kind).toBe('absent')
    expect((state as { reason: string }).reason).not.toBe('')
  })

  it('is loading while the request is in flight', () => {
    expect(metricsState({ isLoading: true })).toEqual({ kind: 'loading' })
  })

  // The bug this exists to prevent: a failed or parked request rendered as a
  // statement about the cluster. "I could not ask" is not "there are none".
  it('reports a failure as unreachable, never as absence', () => {
    const failed = metricsState({ isLoading: false, error: new Error('network down') })
    expect(failed.kind).toBe('unreachable')
    expect(failed).toMatchObject({ reason: 'network down' })

    const parked = metricsState({ isLoading: false, isPaused: true, failureReason: new Error('offline') })
    expect(parked.kind).toBe('unreachable')
    expect(parked).toMatchObject({ reason: 'offline' })
  })

  it('has something to say when a request parked before failing', () => {
    const state = metricsState({ isLoading: false, isPaused: true })
    expect(state.kind).toBe('unreachable')
    expect((state as { reason: string }).reason).toContain('offline')
  })

  // A drawn chart is worth more than a refresh that failed behind it. Thirty
  // seconds stale beats blanking the panel someone is reading.
  it('keeps showing data when a later refetch fails', () => {
    expect(
      metricsState({ data: { available: true, nodes: [] }, isLoading: false, error: new Error('blip') }),
    ).toEqual({ kind: 'ready' })
  })

  it('never returns loading for a query that will never resolve', () => {
    // Nothing pending, nothing failed, nothing there — the state that used to
    // fall through to "Metrics are unavailable."
    const state = metricsState({ isLoading: false })
    expect(state.kind).toBe('unreachable')
  })

  // A refusal is an answer, and it is a permanent one. Landing it in
  // 'unreachable' put a "Try again" link under a permission the viewer will
  // never hold — on the cluster used for local development, where the console
  // says "you are not allowed to list nodes cluster-wide · Try again" and
  // trying again does exactly the same thing forever.
  it('reports a refusal as forbidden, not as something to retry', () => {
    const err = Object.assign(new Error('you are not allowed to list nodes cluster-wide'), {
      status: 403,
    })
    const state = metricsState({ isLoading: false, error: err })
    expect(state.kind).toBe('forbidden')
    expect(state).toMatchObject({ reason: expect.stringContaining('not allowed') })
  })

  // And it must not swallow the failures that *are* worth retrying.
  it('keeps other HTTP failures retryable', () => {
    const err = Object.assign(new Error('the server hit an unexpected error'), { status: 500 })
    expect(metricsState({ isLoading: false, error: err }).kind).toBe('unreachable')
  })

  // A refusal still loses to data already on screen, like any other error.
  it('keeps showing data when a later refetch is refused', () => {
    const err = Object.assign(new Error('forbidden'), { status: 403 })
    expect(
      metricsState({ data: { available: true, nodes: [] }, isLoading: false, error: err }),
    ).toEqual({ kind: 'ready' })
  })
})
