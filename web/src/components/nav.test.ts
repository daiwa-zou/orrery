import { describe, expect, it } from 'vitest'
import type { APIResource, DiscoveryResponse } from '../api/types'
import { buildNav, isBrowsable, isCustomGroup, objectPath, resourcesByKind } from './nav'
import { rankResources, scoreResource } from '../lib/palette'

function resource(partial: Partial<APIResource> & { kind: string; name: string }): APIResource {
  return {
    group: '',
    version: 'v1',
    singularName: partial.kind.toLowerCase(),
    namespaced: true,
    verbs: ['get', 'list', 'watch'],
    preferred: true,
    ...partial,
  }
}

function discovery(resources: APIResource[]): DiscoveryResponse {
  const groups = new Map<string, APIResource[]>()
  for (const r of resources) {
    groups.set(r.group, [...(groups.get(r.group) ?? []), r])
  }
  return {
    serverVersion: 'v1.35.1',
    groups: [...groups.entries()].map(([group, rs]) => ({ group, resources: rs })),
  }
}

/** A realistic slice of a cluster: built-ins, a demoted built-in, and a CRD. */
const CLUSTER = discovery([
  resource({ kind: 'Pod', name: 'pods', shortNames: ['po'] }),
  resource({ kind: 'ConfigMap', name: 'configmaps', shortNames: ['cm'] }),
  resource({ kind: 'ComponentStatus', name: 'componentstatuses', shortNames: ['cs'], namespaced: false }),
  resource({ kind: 'Service', name: 'services', shortNames: ['svc'] }),
  resource({ kind: 'Deployment', name: 'deployments', group: 'apps', shortNames: ['deploy'] }),
  resource({ kind: 'Job', name: 'jobs', group: 'batch' }),
  resource({ kind: 'ReplicaSet', name: 'replicasets', group: 'apps', shortNames: ['rs'] }),
  resource({ kind: 'Role', name: 'roles', group: 'rbac.authorization.k8s.io' }),
  resource({
    kind: 'PodDisruptionBudget',
    name: 'poddisruptionbudgets',
    group: 'policy',
    shortNames: ['pdb'],
  }),
  resource({
    kind: 'NetworkPolicy',
    name: 'networkpolicies',
    group: 'networking.k8s.io',
    shortNames: ['netpol'],
  }),
  // Dynamic Resource Allocation: a built-in group that a hardcoded list missed.
  resource({ kind: 'ResourceClaim', name: 'resourceclaims', group: 'resource.k8s.io' }),
  resource({ kind: 'PodMetrics', name: 'pods', group: 'metrics.k8s.io' }),
  resource({ kind: 'Widget', name: 'widgets', group: 'demo.orrery.io', shortNames: ['wg'] }),
])

describe('isCustomGroup', () => {
  it('treats Kubernetes groups as built in, by rule rather than by list', () => {
    for (const group of ['', 'apps', 'batch', 'policy', 'rbac.authorization.k8s.io']) {
      expect(isCustomGroup(group), group).toBe(false)
    }
  })

  it('recognises upstream groups it has never seen before', () => {
    // The regression that motivated the rule: resource.k8s.io shipped and
    // started showing up as somebody's CRD.
    expect(isCustomGroup('resource.k8s.io')).toBe(false)
    expect(isCustomGroup('some.future.k8s.io')).toBe(false)
  })

  it('treats third-party domains as custom', () => {
    for (const group of ['demo.orrery.io', 'cert-manager.io', 'monitoring.coreos.com']) {
      expect(isCustomGroup(group), group).toBe(true)
    }
  })
})

describe('buildNav', () => {
  const nav = buildNav(CLUSTER)

  it('returns nothing before discovery has loaded', () => {
    const empty = buildNav(undefined)
    expect(empty.primary).toEqual([])
    expect(empty.custom).toEqual([])
    expect(empty.rest).toEqual([])
  })

  it('puts the everyday resources in named sections, in the curated order', () => {
    const workloads = nav.primary.find((s) => s.title === 'Workloads')
    expect(workloads?.items.map((i) => i.kind)).toEqual(['Pod', 'Deployment', 'Job'])

    const config = nav.primary.find((s) => s.title === 'Configuration')
    expect(config?.items.map((i) => i.kind)).toEqual(['ConfigMap'])
  })

  it('demotes rarely used built-ins to the flat tier', () => {
    const restKinds = nav.rest.map((i) => i.kind)
    expect(restKinds).toContain('ReplicaSet')
    expect(restKinds).toContain('Role')
    expect(restKinds).toContain('PodDisruptionBudget')
    expect(restKinds).toContain('ResourceClaim')
  })

  it('surfaces only genuine CRDs as custom resources', () => {
    expect(nav.custom.map((i) => i.kind)).toEqual(['Widget'])
  })

  it('excludes resources that cannot be listed', () => {
    const everything = [...nav.primary.flatMap((s) => s.items), ...nav.custom, ...nav.rest]
    expect(everything.some((i) => i.group === 'metrics.k8s.io')).toBe(false)
  })

  it('never shows the same resource in two tiers', () => {
    const keys = [...nav.primary.flatMap((s) => s.items), ...nav.custom, ...nav.rest].map(
      (i) => `${i.group}/${i.resource}`,
    )
    expect(new Set(keys).size).toBe(keys.length)
  })

  it('does not let a CRD displace the built-in kind of the same name', () => {
    // An operator that defines its own "jobs" must not take over Workloads.
    const withImposter = buildNav(
      discovery([
        resource({ kind: 'Job', name: 'jobs', group: 'batch' }),
        resource({ kind: 'Job', name: 'jobs', group: 'rogue.example.com' }),
      ]),
    )
    const workloads = withImposter.primary.find((s) => s.title === 'Workloads')
    expect(workloads?.items[0].group).toBe('batch')
    // The imposter is still reachable, just not in the curated slot.
    expect(withImposter.custom.map((i) => i.group)).toEqual(['rogue.example.com'])
  })
})

describe('isBrowsable', () => {
  it('excludes the metrics API, which cannot back a list view', () => {
    expect(isBrowsable('metrics.k8s.io')).toBe(false)
    expect(isBrowsable('apps')).toBe(true)
  })
})

describe('scoreResource', () => {
  const configMap = { kind: 'ConfigMap', name: 'configmaps', shortNames: ['cm'], group: '' }

  it('ranks an exact short name above everything else', () => {
    expect(scoreResource(configMap, 'cm')).toBe(0)
  })

  it('excludes resources that do not match at all', () => {
    expect(scoreResource(configMap, 'zzzz')).toBe(Infinity)
  })

  it('ignores surrounding whitespace and case', () => {
    expect(scoreResource(configMap, '  CM  ')).toBe(0)
  })
})

describe('rankResources', () => {
  const resources = CLUSTER.groups.flatMap((g) => g.resources)
  const top = (query: string) => rankResources(resources, query).map((r) => r.kind)

  it('sends a short name straight to its resource', () => {
    expect(top('cm')[0]).toBe('ConfigMap')
    expect(top('netpol')[0]).toBe('NetworkPolicy')
  })

  it('finds a custom resource by its short name', () => {
    expect(top('wg')[0]).toBe('Widget')
  })

  it('prefers an exact kind over one that merely starts with the query', () => {
    const ranked = top('pod')
    expect(ranked[0]).toBe('Pod')
    expect(ranked.indexOf('PodDisruptionBudget')).toBeGreaterThan(0)
  })

  it('does not let a two-letter query match unrelated kinds', () => {
    // "cm" must not drag in ComponentStatus just because both start with C.
    expect(top('cm')).not.toContain('ComponentStatus')
  })

  it('finds demoted resources, which is what makes hiding them safe', () => {
    expect(top('role')[0]).toBe('Role')
    expect(top('replicaset')[0]).toBe('ReplicaSet')
  })

  it('returns everything when the query is empty', () => {
    expect(rankResources(resources, '')).toHaveLength(resources.length)
  })
})

describe('resourcesByKind', () => {
  it('lets a built-in kind win over a CRD that shadows it', () => {
    // An operator shipping its own Service must not capture the events about
    // core/v1 Services: the shadowed kind is the one almost everybody means.
    const d = discovery([
      resource({ kind: 'Service', name: 'services', group: '' }),
      resource({ kind: 'Service', name: 'services', group: 'acme.example.com' }),
    ])
    expect(resourcesByKind(d).get('service')?.group).toBe('')
  })

  it('lets a preferred version win over a superseded one', () => {
    const d = discovery([
      resource({ kind: 'Widget', name: 'widgets', group: 'acme.io', version: 'v1beta1', preferred: false }),
      resource({ kind: 'Widget', name: 'widgets', group: 'acme.io', version: 'v1', preferred: true }),
    ])
    expect(resourcesByKind(d).get('widget')?.version).toBe('v1')
  })

  it('has nothing to say before discovery arrives', () => {
    expect(resourcesByKind(undefined).size).toBe(0)
  })
})

describe('objectPath', () => {
  const byKind = resourcesByKind(
    discovery([
      resource({ kind: 'Pod', name: 'pods', group: '' }),
      resource({ kind: 'Node', name: 'nodes', group: '', namespaced: false }),
      resource({ kind: 'Deployment', name: 'deployments', group: 'apps', version: 'v1' }),
    ]),
  )

  it('addresses a namespaced object through its namespace', () => {
    expect(objectPath('lens-a', byKind, 'Pod/web-1', 'demo')).toBe(
      '/c/lens-a/r/core/v1/pods/demo/web-1',
    )
  })

  it('encodes the core group the way the routes spell it', () => {
    expect(objectPath('lens-a', byKind, 'Deployment/web', 'demo')).toBe(
      '/c/lens-a/r/apps/v1/deployments/demo/web',
    )
  })

  it('puts _ where a cluster-scoped object has no namespace', () => {
    expect(objectPath('lens-a', byKind, 'Node/node-1', 'demo')).toBe(
      '/c/lens-a/r/core/v1/nodes/_/node-1',
    )
    expect(objectPath('lens-a', byKind, 'Pod/web-1')).toBe('/c/lens-a/r/core/v1/pods/_/web-1')
  })

  it('refuses what it cannot resolve rather than guessing a URL', () => {
    // A row dressed as a link that leads to a 404 is indistinguishable, to the
    // reader, from the console being broken.
    expect(objectPath('lens-a', byKind, 'Widget/thing', 'demo')).toBeUndefined()
    expect(objectPath('lens-a', byKind, 'justaname', 'demo')).toBeUndefined()
    expect(objectPath('lens-a', byKind, 'Pod/', 'demo')).toBeUndefined()
    expect(objectPath('lens-a', new Map(), 'Pod/web-1', 'demo')).toBeUndefined()
  })
})
