import { describe, expect, it } from 'vitest'

import type { Overview } from '../api/types'
import { fleetHealth, fleetHealthLabel, needsAttention } from './fleetHealth'

function overview(partial: Partial<Overview>): Overview {
  return {
    cluster: { name: 'c', displayName: 'C', authMode: 'impersonation', health: { status: 'healthy' }, available: true },
    nodes: { total: 1 },
    namespaces: { total: 3 },
    pods: { total: 0 },
    workloads: {},
    warnings: [],
    ...partial,
  } as Overview
}

const warning = { namespace: 'demo', reason: 'BackOff', message: 'x', object: 'Pod/a', count: 1, lastSeen: '' }

describe('fleetHealth', () => {
  it('counts pods that are neither running nor finished', () => {
    const h = fleetHealth(overview({
      pods: { total: 6, byStatus: { Running: 3, Succeeded: 1, ImagePullBackOff: 1, Pending: 1 } },
    }))
    expect(h.podsNeedingAttention).toBe(2)
    expect(needsAttention(h)).toBe(true)
  })

  it('treats Completed the same as Succeeded', () => {
    const h = fleetHealth(overview({ pods: { total: 2, byStatus: { Running: 1, Completed: 1 } } }))
    expect(h.podsNeedingAttention).toBe(0)
    expect(needsAttention(h)).toBe(false)
  })

  it('counts degraded and unscheduled workloads, not progressing ones', () => {
    // Progressing is a rollout in flight, which is the normal state during a
    // deploy and not something to call anyone about.
    const h = fleetHealth(overview({
      workloads: {
        deployments: { total: 3, byStatus: { Healthy: 1, Degraded: 1, Progressing: 1 } },
        daemonsets: { total: 1, byStatus: { 'Not scheduled': 1 } },
        jobs: { total: 1, byStatus: { Healthy: 1 } },
      },
    }))
    expect(h.workloadsUnhealthy).toBe(2)
  })

  it('reports warnings without treating them as attention on their own', () => {
    // A warning that already resolved should not keep a cluster flagged red.
    const h = fleetHealth(overview({ pods: { total: 1, byStatus: { Running: 1 } }, warnings: [warning] }))
    expect(h.warnings).toBe(1)
    expect(needsAttention(h)).toBe(false)
  })

  // The point of the whole module. A zero that came from not looking must
  // never be presented as a zero that came from looking.
  it('marks the answer partial when a count was refused or unavailable', () => {
    expect(fleetHealth(overview({ pods: { total: 0, forbidden: true } })).partial).toBe(true)
    expect(fleetHealth(overview({ pods: { total: 0, unavailable: true } })).partial).toBe(true)
    expect(fleetHealth(overview({ workloads: { deployments: { total: 0, forbidden: true } } })).partial).toBe(true)
    expect(fleetHealth(overview({ warningsForbidden: true })).partial).toBe(true)
    expect(fleetHealth(overview({ warningsUnavailable: true })).partial).toBe(true)
  })

  it('is not partial when everything was readable', () => {
    const h = fleetHealth(overview({
      pods: { total: 2, byStatus: { Running: 2 } },
      workloads: { deployments: { total: 1, byStatus: { Healthy: 1 } } },
    }))
    expect(h.partial).toBe(false)
    expect(h.unknown).toBe(false)
  })

  it('is unknown with no overview at all', () => {
    const h = fleetHealth(undefined)
    expect(h.unknown).toBe(true)
    expect(needsAttention(h)).toBe(false)
  })
})

describe('fleetHealthLabel', () => {
  it('says what is wrong, singular and plural', () => {
    expect(fleetHealthLabel(fleetHealth(overview({
      pods: { total: 2, byStatus: { Running: 1, ImagePullBackOff: 1 } },
    })))).toBe('1 pod not running')

    expect(fleetHealthLabel(fleetHealth(overview({
      pods: { total: 3, byStatus: { Pending: 2 } },
      workloads: { deployments: { total: 2, byStatus: { Degraded: 2 } } },
    })))).toBe('2 pods not running · 2 workloads degraded')
  })

  it('reports a clean cluster, mentioning warnings when there are any', () => {
    expect(fleetHealthLabel(fleetHealth(overview({ pods: { total: 1, byStatus: { Running: 1 } } }))))
      .toBe('Workloads healthy')
    expect(fleetHealthLabel(fleetHealth(overview({
      pods: { total: 1, byStatus: { Running: 1 } }, warnings: [warning],
    })))).toBe('Workloads healthy · 1 recent warning')
  })

  // Never "healthy" on an answer that was not fully read — that is the exact
  // claim this page was making and could not support.
  it('refuses to call a partial answer healthy', () => {
    const label = fleetHealthLabel(fleetHealth(overview({ pods: { total: 0, forbidden: true } })))
    expect(label).not.toMatch(/healthy/i)
    expect(label).toBe('Nothing to report from what could be read')
  })

  it('flags the gap even when it did find something', () => {
    const label = fleetHealthLabel(fleetHealth(overview({
      pods: { total: 2, byStatus: { Pending: 1 } },
      workloads: { deployments: { total: 0, forbidden: true } },
    })))
    expect(label).toContain('1 pod not running')
    expect(label).toContain('unavailable')
  })

  it('says so when nothing could be read', () => {
    expect(fleetHealthLabel(fleetHealth(undefined))).toBe('Workload health unavailable')
  })
})
