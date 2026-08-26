import { describe, expect, it } from 'vitest'

import type { KubeObject } from '../api/types'
import { containerNamesOf, defaultContainerOf, podSpecOf } from './podTemplate'

function obj(kind: string, body: Record<string, unknown>): KubeObject {
  return { kind, metadata: { name: 'x' }, ...body } as KubeObject
}

const podSpec = {
  initContainers: [{ name: 'wait-for-db' }],
  containers: [{ name: 'app' }, { name: 'sidecar' }],
}

describe('podSpecOf', () => {
  it('reads a Pod straight off spec', () => {
    expect(podSpecOf(obj('Pod', { spec: podSpec }))).toEqual(podSpec)
  })

  it('reaches through the template of every workload kind', () => {
    for (const kind of ['Deployment', 'StatefulSet', 'DaemonSet', 'ReplicaSet', 'ReplicationController', 'Job']) {
      expect(podSpecOf(obj(kind, { spec: { template: { spec: podSpec } } }))).toEqual(podSpec)
    }
  })

  // A CronJob templates a Job which templates a Pod. Getting this wrong is
  // invisible — it looks like a CronJob with no containers.
  it('reaches through a CronJob\'s two levels of template', () => {
    const cj = obj('CronJob', { spec: { jobTemplate: { spec: { template: { spec: podSpec } } } } })
    expect(podSpecOf(cj)).toEqual(podSpec)
  })

  it('returns undefined for kinds that have no pod spec', () => {
    for (const kind of ['Service', 'ConfigMap', 'Node', 'CustomThing']) {
      expect(podSpecOf(obj(kind, { spec: podSpec }))).toBeUndefined()
    }
    expect(podSpecOf(undefined)).toBeUndefined()
    expect(podSpecOf(obj('Deployment', {}))).toBeUndefined()
    expect(podSpecOf(obj('Deployment', { spec: { template: 'nonsense' } }))).toBeUndefined()
  })
})

describe('containerNamesOf', () => {
  it('lists init containers before app containers', () => {
    expect(containerNamesOf(obj('Pod', { spec: podSpec }))).toEqual(['wait-for-db', 'app', 'sidecar'])
  })

  it('works through a workload template', () => {
    const dep = obj('Deployment', { spec: { template: { spec: podSpec } } })
    expect(containerNamesOf(dep)).toEqual(['wait-for-db', 'app', 'sidecar'])
  })

  it('survives malformed container lists', () => {
    expect(containerNamesOf(obj('Pod', { spec: { containers: 'nope' } }))).toEqual([])
    expect(containerNamesOf(obj('Pod', { spec: { containers: [{}, { name: 42 }, { name: 'ok' }] } }))).toEqual(['ok'])
    expect(containerNamesOf(obj('Pod', {}))).toEqual([])
  })
})

describe('defaultContainerOf', () => {
  it('picks the first app container, never an init container', () => {
    expect(defaultContainerOf(obj('Pod', { spec: podSpec }))).toBe('app')
  })

  it('honours the default-container annotation on a Pod', () => {
    const pod = {
      kind: 'Pod',
      metadata: {
        name: 'x',
        annotations: { 'kubectl.kubernetes.io/default-container': 'sidecar' },
      },
      spec: podSpec,
    } as KubeObject
    expect(defaultContainerOf(pod)).toBe('sidecar')
  })

  // The annotation is inherited by the pods, so on a workload it lives on the
  // template. Reading the workload's own metadata would miss it every time.
  it('reads the annotation from a workload\'s pod template, not the workload', () => {
    const dep = {
      kind: 'Deployment',
      metadata: { name: 'x', annotations: { 'kubectl.kubernetes.io/default-container': 'ignored' } },
      spec: {
        template: {
          metadata: { annotations: { 'kubectl.kubernetes.io/default-container': 'sidecar' } },
          spec: podSpec,
        },
      },
    } as unknown as KubeObject
    expect(defaultContainerOf(dep)).toBe('sidecar')
  })

  it('ignores an annotation naming a container that does not exist', () => {
    const pod = {
      kind: 'Pod',
      metadata: { name: 'x', annotations: { 'kubectl.kubernetes.io/default-container': 'ghost' } },
      spec: podSpec,
    } as KubeObject
    expect(defaultContainerOf(pod)).toBe('app')
  })

  it('returns empty when there is nothing to default to', () => {
    expect(defaultContainerOf(obj('Pod', { spec: { initContainers: [{ name: 'only-init' }] } }))).toBe('')
    expect(defaultContainerOf(obj('Service', { spec: podSpec }))).toBe('')
    expect(defaultContainerOf(undefined)).toBe('')
  })
})
