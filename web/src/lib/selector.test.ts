import { describe, expect, it } from 'vitest'

import { POD_OWNERS, podSelectorOf, renderSelector } from './selector'

describe('renderSelector', () => {
  it('renders matchLabels as equality terms', () => {
    expect(renderSelector({ matchLabels: { app: 'web' } })).toBe('app=web')
  })

  // The string reaches a react-query key and a WebSocket URL. If key order
  // decided the output, an object arriving with its keys in a different order
  // would refetch the list and reconnect the log stream for no reason.
  it('orders labels deterministically', () => {
    const a = renderSelector({ matchLabels: { tier: 'front', app: 'web' } })
    const b = renderSelector({ matchLabels: { app: 'web', tier: 'front' } })
    expect(a).toBe(b)
    expect(a).toBe('app=web,tier=front')
  })

  it('reads a bare map, as ReplicationController and Service carry it', () => {
    expect(renderSelector({ app: 'legacy', tier: 'db' })).toBe('app=legacy,tier=db')
  })

  it('renders every matchExpressions operator', () => {
    const cases: [Record<string, unknown>, string][] = [
      [{ key: 'tier', operator: 'In', values: ['front', 'back'] }, 'tier in (front,back)'],
      [{ key: 'tier', operator: 'NotIn', values: ['cache'] }, 'tier notin (cache)'],
      [{ key: 'live', operator: 'Exists' }, 'live'],
      [{ key: 'legacy', operator: 'DoesNotExist' }, '!legacy'],
    ]
    for (const [expr, want] of cases) {
      expect(renderSelector({ matchExpressions: [expr] })).toBe(want)
    }
  })

  it('combines matchLabels and matchExpressions', () => {
    expect(
      renderSelector({
        matchLabels: { app: 'web' },
        matchExpressions: [{ key: 'tier', operator: 'In', values: ['front'] }],
      }),
    ).toBe('app=web,tier in (front)')
  })

  // The failure that matters. An empty labelSelector is dropped from the query
  // string, so returning a partial selector would silently widen the question
  // from "this workload's pods" to "every pod in the namespace" — and the
  // caller would render that as the workload's own.
  it('refuses the whole selector when one requirement cannot be expressed', () => {
    expect(
      renderSelector({
        matchLabels: { app: 'web' },
        matchExpressions: [{ key: 'tier', operator: 'In', values: [] }],
      }),
    ).toBe('')
    expect(
      renderSelector({
        matchLabels: { app: 'web' },
        matchExpressions: [{ key: 'tier', operator: 'Sideways', values: ['x'] }],
      }),
    ).toBe('')
    expect(renderSelector({ matchExpressions: [{ operator: 'Exists' }] })).toBe('')
  })

  it('ignores non-string label values rather than stringifying them', () => {
    // A selector is a map of strings; anything else came from a malformed
    // object, and "app=[object Object]" would match nothing while looking
    // like it should match something.
    expect(renderSelector({ matchLabels: { app: 'web', count: 3 } })).toBe('app=web')
  })

  it('returns empty for anything that is not a selector', () => {
    for (const input of [undefined, null, 'app=web', 42, [], {}, { matchLabels: {} }]) {
      expect(renderSelector(input)).toBe('')
    }
  })
})

describe('podSelectorOf', () => {
  it('reads the selector of every kind that owns pods', () => {
    for (const kind of POD_OWNERS) {
      expect(podSelectorOf(kind, { selector: { matchLabels: { app: 'web' } } })).toBe('app=web')
    }
  })

  it('declines kinds that do not own pods', () => {
    // A Service has a selector and it does select pods, but it does not *own*
    // them: its endpoints are not a workload's replicas, and offering "the
    // logs of this Service" would merge unrelated workloads into one feed.
    for (const kind of ['Service', 'Pod', 'ConfigMap', 'CronJob', 'Node', '']) {
      expect(podSelectorOf(kind, { selector: { matchLabels: { app: 'web' } } })).toBe('')
    }
  })

  it('survives a spec that is missing or the wrong shape', () => {
    expect(podSelectorOf('Deployment', undefined)).toBe('')
    expect(podSelectorOf('Deployment', {})).toBe('')
    expect(podSelectorOf('Deployment', { selector: null })).toBe('')
    expect(podSelectorOf('Deployment', 'nonsense')).toBe('')
  })

  it('reads a ReplicationController bare-map selector', () => {
    expect(podSelectorOf('ReplicationController', { selector: { app: 'legacy' } })).toBe('app=legacy')
  })
})
