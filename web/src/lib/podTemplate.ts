/**
 * Finding the pod spec, wherever the kind happens to keep it.
 *
 * A Pod carries its spec at `spec`. Every workload carries a *template* of one,
 * and each nests it a little differently — one level down for the apps kinds
 * and Jobs, three for a CronJob, which templates a Job which templates a Pod.
 * Anything that wants "the containers of this thing" has to know all three, and
 * knowing them in one place is the difference between a Logs tab that works on
 * Deployments and one that quietly shows no containers.
 */

import type { KubeObject } from '../api/types'

/** The container spec fields these helpers read. */
interface ContainerLike {
  name?: unknown
}

interface PodSpecLike {
  containers?: unknown
  initContainers?: unknown
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function dig(from: unknown, path: string[]): unknown {
  let cur = from
  for (const step of path) {
    if (!isRecord(cur)) return undefined
    cur = cur[step]
  }
  return cur
}

/** Where each kind keeps its pod spec, relative to the object. */
const POD_SPEC_PATHS: Record<string, string[]> = {
  Pod: ['spec'],
  Deployment: ['spec', 'template', 'spec'],
  StatefulSet: ['spec', 'template', 'spec'],
  DaemonSet: ['spec', 'template', 'spec'],
  ReplicaSet: ['spec', 'template', 'spec'],
  ReplicationController: ['spec', 'template', 'spec'],
  Job: ['spec', 'template', 'spec'],
  CronJob: ['spec', 'jobTemplate', 'spec', 'template', 'spec'],
}

/** Where each kind keeps the pod template's *metadata*. */
const POD_META_PATHS: Record<string, string[]> = {
  Pod: ['metadata'],
  Deployment: ['spec', 'template', 'metadata'],
  StatefulSet: ['spec', 'template', 'metadata'],
  DaemonSet: ['spec', 'template', 'metadata'],
  ReplicaSet: ['spec', 'template', 'metadata'],
  ReplicationController: ['spec', 'template', 'metadata'],
  Job: ['spec', 'template', 'metadata'],
  CronJob: ['spec', 'jobTemplate', 'spec', 'template', 'metadata'],
}

/** The pod spec this object is or describes, or undefined for other kinds. */
export function podSpecOf(obj?: KubeObject): PodSpecLike | undefined {
  if (!obj?.kind) return undefined
  const path = POD_SPEC_PATHS[obj.kind]
  if (!path) return undefined
  const spec = dig(obj, path)
  return isRecord(spec) ? spec : undefined
}

function namesOf(list: unknown): string[] {
  if (!Array.isArray(list)) return []
  return list
    .map((c) => (isRecord(c) ? (c as ContainerLike).name : undefined))
    .filter((n): n is string => typeof n === 'string' && n !== '')
}

/**
 * Every container name, init containers first — the order the containers table
 * shows and the order a picker should offer.
 */
export function containerNamesOf(obj?: KubeObject): string[] {
  const spec = podSpecOf(obj)
  if (!spec) return []
  return [...namesOf(spec.initContainers), ...namesOf(spec.containers)]
}

/**
 * The container a fresh Logs or Terminal tab should open on: the
 * `kubectl.kubernetes.io/default-container` annotation when it names a real
 * container, else the first app container — never an init container, whose
 * stream is usually long-finished silence, and never a trailing sidecar.
 *
 * The annotation is read from the pod template's own metadata, which is where
 * a workload carries it. Reading the workload's metadata instead is the bug
 * this exists to avoid: the annotation is inherited by the pods, so it sits on
 * the template and a Deployment's own annotations usually do not have it.
 */
export function defaultContainerOf(obj?: KubeObject): string {
  const spec = podSpecOf(obj)
  if (!spec) return ''
  const appNames = namesOf(spec.containers)
  if (appNames.length === 0) return ''

  const meta = obj?.kind ? dig(obj, POD_META_PATHS[obj.kind] ?? []) : undefined
  const annotations = isRecord(meta) && isRecord(meta.annotations) ? meta.annotations : undefined
  const annotated = annotations?.['kubectl.kubernetes.io/default-container']
  if (typeof annotated === 'string' && appNames.includes(annotated)) return annotated

  return appNames[0]
}
