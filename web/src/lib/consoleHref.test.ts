import { describe, expect, it } from 'vitest'

import { consoleHref } from './consoleHref'

describe('consoleHref', () => {
  it('builds a namespaced route', () => {
    expect(
      consoleHref({
        cluster: 'lens-a', group: 'apps', version: 'v1',
        resource: 'deployments', namespace: 'demo', name: 'web',
      }),
    ).toBe('/c/lens-a/r/apps/v1/deployments/demo/web')
  })

  it('spells the core group and cluster scope the way the routes expect', () => {
    // An empty segment would not route at all; "core" and "_" are what the
    // resource routes accept.
    expect(
      consoleHref({ cluster: 'lens-a', group: '', version: 'v1', resource: 'nodes', name: 'node-1' }),
    ).toBe('/c/lens-a/r/core/v1/nodes/_/node-1')

    expect(
      consoleHref({ cluster: 'lens-a', version: 'v1', resource: 'nodes', name: 'node-1' }),
    ).toBe('/c/lens-a/r/core/v1/nodes/_/node-1')

    expect(
      consoleHref({ cluster: 'lens-a', version: 'v1', resource: 'pods', namespace: '', name: 'p' }),
    ).toBe('/c/lens-a/r/core/v1/pods/_/p')
  })

  // The reason this takes a resolved resource rather than a kind: a custom
  // resource may spell its plural however it likes, and a guess is a link that
  // 404s for precisely the objects nobody else knows how to find.
  it('uses the resource it was given, never a guess from the kind', () => {
    expect(
      consoleHref({
        cluster: 'lens-b', group: 'acme.example', version: 'v1alpha1',
        resource: 'sprocketz', namespace: 'demo', name: 'sp-1',
      }),
    ).toBe('/c/lens-b/r/acme.example/v1alpha1/sprocketz/demo/sp-1')
  })

  it('keeps a cross-cluster jump pointed at the other cluster', () => {
    // The palette is opened on one cluster and lists hits from all of them.
    expect(
      consoleHref({
        cluster: 'lens-b', group: 'apps', version: 'v1',
        resource: 'deployments', namespace: 'kube-system', name: 'coredns',
      }),
    ).toMatch(/^\/c\/lens-b\//)
  })
})
