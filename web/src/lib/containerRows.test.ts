import { describe, expect, it } from 'vitest'

import type { KubeObject } from '../api/types'
import { containerRows } from './containerRows'

function pod(status: unknown): KubeObject {
  return { kind: 'Pod', metadata: { name: 'p' }, status } as KubeObject
}

describe('containerRows', () => {
  it('reads a healthy running container', () => {
    const [row] = containerRows(pod({
      containerStatuses: [{
        name: 'app', image: 'web:2', ready: true, restartCount: 0,
        state: { running: { startedAt: '2026-01-01T00:00:00Z' } },
      }],
    }))
    expect(row).toMatchObject({ name: 'app', image: 'web:2', ready: true, restarts: 0, state: 'Running', init: false })
    expect(row.lastExit).toBeUndefined()
  })

  // The case the table exists for. Waiting carries the reason people are
  // actually looking for, and it is not the pod phase.
  it('surfaces the waiting reason rather than the word Waiting', () => {
    const [row] = containerRows(pod({
      containerStatuses: [{
        name: 'app', ready: false, restartCount: 7,
        state: { waiting: { reason: 'ImagePullBackOff', message: 'Back-off pulling image' } },
      }],
    }))
    expect(row.state).toBe('ImagePullBackOff')
    expect(row.stateDetail).toBe('Back-off pulling image')
    expect(row.restarts).toBe(7)
  })

  it('falls back to a generic state when no reason is given', () => {
    const [waiting] = containerRows(pod({ containerStatuses: [{ name: 'a', state: { waiting: {} } }] }))
    expect(waiting.state).toBe('Waiting')
    const [term] = containerRows(pod({ containerStatuses: [{ name: 'a', state: { terminated: {} } }] }))
    expect(term.state).toBe('Terminated')
    const [none] = containerRows(pod({ containerStatuses: [{ name: 'a' }] }))
    expect(none.state).toBe('Unknown')
  })

  // How the previous instance died is the other half of "why is it
  // restarting", and it lives in lastState, not state.
  it('reports the previous exit with its reason', () => {
    const [row] = containerRows(pod({
      containerStatuses: [{
        name: 'app', state: { running: {} },
        lastState: { terminated: { exitCode: 137, reason: 'OOMKilled' } },
      }],
    }))
    expect(row.lastExit).toBe('exit 137 (OOMKilled)')
  })

  it('reports a bare exit code when the reason is missing', () => {
    const [row] = containerRows(pod({
      containerStatuses: [{ name: 'app', state: { running: {} }, lastState: { terminated: { exitCode: 1 } } }],
    }))
    expect(row.lastExit).toBe('exit 1')
  })

  it('marks init containers so the table can separate them', () => {
    const rows = containerRows(pod({
      initContainerStatuses: [{ name: 'wait-for-db', state: { terminated: { reason: 'Completed', exitCode: 0 } } }],
      containerStatuses: [{ name: 'app', state: { running: {} } }],
    }))
    const init = rows.find((r) => r.name === 'wait-for-db')
    const app = rows.find((r) => r.name === 'app')
    expect(init?.init).toBe(true)
    expect(app?.init).toBe(false)
  })

  it('survives a pod with nothing to report', () => {
    expect(containerRows(undefined)).toEqual([])
    expect(containerRows(pod(undefined))).toEqual([])
    expect(containerRows(pod({}))).toEqual([])
  })

  // The status block comes off the wire; a field of the wrong type must not
  // take the page down with it.
  it('coerces rather than trusting the shape', () => {
    const [row] = containerRows(pod({
      containerStatuses: [{ name: 123, image: null, ready: 'yes', restartCount: '4' }],
    }))
    expect(row.name).toBe('123')
    expect(row.ready).toBe(false) // only a real true counts as ready
    expect(row.restarts).toBe(4)
  })
})
