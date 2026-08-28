import { describe, expect, it } from 'vitest'

import { selectedIndex } from './palette'

// scoreResource and rankResources are already exercised in
// components/nav.test.ts, which is where the palette's ranking has always been
// tested from; this file covers what that one does not.

/**
 * The palette assembles six sections in a fixed order — Pages, Saved,
 * Resources, Namespaces, Clusters, Objects — and four of them are filled in
 * asynchronously. Resources land in the middle, so anything selected below
 * them moves once discovery resolves.
 */
describe('selectedIndex', () => {
  // What the list is while discovery is still in flight: no Resources yet.
  const cold = [
    { id: 'page:events' },
    { id: 'namespace:default' },
    { id: 'cluster:lens-b' },
  ]
  // The same list a moment later, with the Resources section filled in.
  const warm = [
    { id: 'page:events' },
    { id: 'resource:apps/deployments' },
    { id: 'resource:apps/daemonsets' },
    { id: 'namespace:default' },
    { id: 'cluster:lens-b' },
  ]

  it('keeps the cursor on the entry when a section loads in above it', () => {
    // The user arrowed onto "default" while it sat at row 1.
    expect(selectedIndex(cold, 'namespace:default')).toBe(1)
    // Discovery resolves and two Resources are inserted above it. A row number
    // would now be pointing at a Deployment, and Enter would open it.
    expect(warm[1].id).toBe('resource:apps/deployments')
    expect(selectedIndex(warm, 'namespace:default')).toBe(3)
  })

  it('starts at the top match before anything is chosen', () => {
    expect(selectedIndex(warm, undefined)).toBe(0)
    expect(selectedIndex([], undefined)).toBe(0)
  })

  it('returns to the top match when the chosen entry is filtered away', () => {
    // Narrowing the query past the selected entry should land on the best
    // remaining match, not on wherever the list happens to end.
    expect(selectedIndex(warm, 'namespace:kube-system')).toBe(0)
  })

  it('is in range for an empty list', () => {
    expect(selectedIndex([], 'namespace:default')).toBe(0)
  })
})
