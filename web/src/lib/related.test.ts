import { describe, expect, it } from 'vitest'

import type { ObjectRef } from '../api/types'
import { groupRelations, relatedHref } from './related'

function ref(relation: string, kind: string, name: string, extra: Partial<ObjectRef> = {}): ObjectRef {
  return { relation, kind, name, ...extra }
}

describe('groupRelations', () => {
  it('groups by relation and labels each group', () => {
    const groups = groupRelations([
      ref('child', 'Pod', 'web-1'),
      ref('owner', 'Deployment', 'web'),
    ])
    expect(groups.map((g) => [g.relation, g.label])).toEqual([
      ['owner', 'Owned by'],
      ['child', 'Owns'],
    ])
  })

  // The order is the order of the question: what made this, what did it make,
  // where does it run, what does it talk to, what does it read.
  it('orders groups by the walk, not by arrival', () => {
    const groups = groupRelations([
      ref('reference', 'ConfigMap', 'cfg'),
      ref('selected-by', 'Service', 'svc'),
      ref('node', 'Node', 'n1'),
      ref('descendant', 'Pod', 'p1'),
      ref('child', 'ReplicaSet', 'rs'),
      ref('owner', 'Deployment', 'dep'),
    ])
    expect(groups.map((g) => g.relation)).toEqual([
      'owner',
      'child',
      'descendant',
      'node',
      'selected-by',
      'reference',
    ])
  })

  // The server's vocabulary is allowed to grow. Hiding what this build has not
  // been taught about is worse than showing a heading nobody styled.
  it('keeps an unknown relation under its own heading', () => {
    const groups = groupRelations([ref('owner', 'Deployment', 'web'), ref('inspires', 'Poem', 'ode')])
    expect(groups.map((g) => g.relation)).toEqual(['owner', 'inspires'])
    expect(groups[1].label).toBe('inspires')
  })

  it('sorts shallower ownership hops first, then kind and name', () => {
    const groups = groupRelations([
      ref('descendant', 'Pod', 'b', { depth: 2 }),
      ref('descendant', 'Pod', 'a', { depth: 2 }),
      ref('descendant', 'ReplicaSet', 'rs', { depth: 1 }),
    ])
    expect(groups[0].refs.map((r) => r.name)).toEqual(['rs', 'a', 'b'])
  })

  // The list is re-rendered from a cache that need not preserve order; a group
  // that reshuffles under the reader is its own small bug.
  it('is stable however the input is ordered', () => {
    const refs = [
      ref('child', 'Pod', 'b', { depth: 1 }),
      ref('child', 'Pod', 'a', { depth: 1 }),
      ref('owner', 'Deployment', 'web', { depth: 1 }),
    ]
    const forward = groupRelations(refs)
    const backward = groupRelations([...refs].reverse())
    expect(JSON.stringify(forward)).toBe(JSON.stringify(backward))
  })

  it('handles nothing to show', () => {
    expect(groupRelations(undefined)).toEqual([])
    expect(groupRelations([])).toEqual([])
  })
})

describe('relatedHref', () => {
  it('builds a console route from the resolved fields', () => {
    expect(
      relatedHref('lens-a', ref('owner', 'Deployment', 'web', {
        group: 'apps', version: 'v1', resource: 'deployments', namespace: 'demo',
      })),
    ).toBe('/c/lens-a/r/apps/v1/deployments/demo/web')
  })

  it('spells the core group and cluster scope the way the routes expect', () => {
    expect(
      relatedHref('lens-a', ref('node', 'Node', 'n1', { group: '', version: 'v1', resource: 'nodes' })),
    ).toBe('/c/lens-a/r/core/v1/nodes/_/n1')
    // An absent group is the core group, not a missing segment.
    expect(
      relatedHref('lens-a', ref('node', 'Node', 'n1', { version: 'v1', resource: 'nodes' })),
    ).toBe('/c/lens-a/r/core/v1/nodes/_/n1')
  })

  // The reason to read the server's resolved resource instead of pluralising a
  // kind here: a CRD may spell its plural however it likes, and a guess is a
  // link that 404s for exactly the objects nobody else knows how to find.
  it('uses the resource the server resolved, not a guess from the kind', () => {
    expect(
      relatedHref('lens-a', ref('owner', 'Sprocket', 'sp-1', {
        group: 'acme.example', version: 'v1', resource: 'sprocketz', namespace: 'demo',
      })),
    ).toBe('/c/lens-a/r/acme.example/v1/sprocketz/demo/sp-1')
  })

  it('offers no link when the server could not resolve the resource', () => {
    // This is the shape of a reference to a kind the cluster does not serve:
    // named, with a note, and nowhere to go. A link built anyway would 404.
    expect(
      relatedHref('lens-a', ref('owner', 'Sprocket', 'sp-1', { note: 'not served by this cluster' })),
    ).toBeUndefined()
    expect(relatedHref('lens-a', ref('owner', 'X', 'x', { resource: 'xs' }))).toBeUndefined()
  })
})
