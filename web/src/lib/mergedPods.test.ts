import { describe, expect, it } from 'vitest'

import { chooseMergedPods, MAX_MERGED_PODS } from './mergedPods'

const names = (n: number, prefix = 'pod') =>
  Array.from({ length: n }, (_, i) => `${prefix}-${String(i).padStart(3, '0')}`)

describe('chooseMergedPods', () => {
  it('keeps every pod when the set fits', () => {
    const all = names(MAX_MERGED_PODS)
    const { pods, dropped } = chooseMergedPods(all)
    expect(pods).toEqual(all)
    expect(dropped).toBe(0)
  })

  it('truncates to the ceiling and reports the remainder', () => {
    const { pods, dropped } = chooseMergedPods(names(MAX_MERGED_PODS + 7))
    expect(pods).toHaveLength(MAX_MERGED_PODS)
    expect(dropped).toBe(7)
  })

  // The one that matters. Arrival order comes from a shared cache, so taking
  // the first twenty as they land would show a different twenty replicas after
  // the next update — a feed quietly swapping its subject mid-incident.
  it('picks the same pods however the input is ordered', () => {
    const all = names(MAX_MERGED_PODS + 5)
    const shuffled = [...all].reverse()
    expect(chooseMergedPods(shuffled).pods).toEqual(chooseMergedPods(all).pods)
  })

  it('does not mutate the caller\'s array', () => {
    const all = ['c', 'a', 'b']
    chooseMergedPods(all)
    expect(all).toEqual(['c', 'a', 'b'])
  })

  it('handles an empty set', () => {
    expect(chooseMergedPods([])).toEqual({ pods: [], dropped: 0 })
  })
})
