import type { APIResource, DiscoveryResponse } from '../api/types'

/**
 * The navigation is curated rather than generated. A raw discovery dump lists
 * two hundred resources in alphabetical order, which is technically complete
 * and practically unusable — so the resources people reach for daily get
 * named sections in a deliberate order, and everything else is grouped by its
 * API group underneath.
 */
export interface NavSection {
  title: string
  items: NavItem[]
  /** Custom-resource sections start collapsed; curated ones do not. */
  collapsible?: boolean
}

export interface NavItem {
  label: string
  group: string
  version: string
  resource: string
  namespaced: boolean
  kind: string
}

const CURATED: { title: string; resources: string[] }[] = [
  {
    title: 'Workloads',
    resources: [
      'pods',
      'deployments',
      'statefulsets',
      'daemonsets',
      'replicasets',
      'jobs',
      'cronjobs',
      'replicationcontrollers',
    ],
  },
  {
    title: 'Networking',
    resources: ['services', 'ingresses', 'endpoints', 'networkpolicies', 'ingressclasses'],
  },
  {
    title: 'Storage',
    resources: ['persistentvolumeclaims', 'persistentvolumes', 'storageclasses'],
  },
  {
    title: 'Configuration',
    resources: [
      'configmaps',
      'secrets',
      'horizontalpodautoscalers',
      'poddisruptionbudgets',
      'resourcequotas',
      'limitranges',
      'priorityclasses',
    ],
  },
  {
    title: 'Access control',
    resources: [
      'serviceaccounts',
      'roles',
      'rolebindings',
      'clusterroles',
      'clusterrolebindings',
    ],
  },
  {
    title: 'Cluster',
    resources: ['nodes', 'namespaces', 'events', 'customresourcedefinitions'],
  },
]

/** API groups whose resources are Kubernetes' own, not a user's CRDs. */
const BUILTIN_GROUPS = new Set([
  '',
  'apps',
  'batch',
  'autoscaling',
  'networking.k8s.io',
  'policy',
  'rbac.authorization.k8s.io',
  'storage.k8s.io',
  'scheduling.k8s.io',
  'apiextensions.k8s.io',
  'coordination.k8s.io',
  'admissionregistration.k8s.io',
  'apiregistration.k8s.io',
  'authentication.k8s.io',
  'authorization.k8s.io',
  'certificates.k8s.io',
  'discovery.k8s.io',
  'events.k8s.io',
  'flowcontrol.apiserver.k8s.io',
  'node.k8s.io',
  'metrics.k8s.io',
])

function toItem(r: APIResource): NavItem {
  return {
    label: r.kind,
    group: r.group,
    version: r.version,
    resource: r.name,
    namespaced: r.namespaced,
    kind: r.kind,
  }
}

export function buildNav(discovery?: DiscoveryResponse): NavSection[] {
  if (!discovery) return []

  const all: APIResource[] = discovery.groups.flatMap((g) => g.resources)
  const byName = new Map<string, APIResource>()
  for (const r of all) {
    // Prefer the built-in owner of a name so a CRD called "jobs" cannot
    // displace batch/v1 Jobs in the Workloads section.
    const existing = byName.get(r.name)
    if (!existing || (BUILTIN_GROUPS.has(r.group) && !BUILTIN_GROUPS.has(existing.group))) {
      byName.set(r.name, r)
    }
  }

  const claimed = new Set<string>()
  const sections: NavSection[] = []

  for (const section of CURATED) {
    const items: NavItem[] = []
    for (const name of section.resources) {
      const r = byName.get(name)
      if (!r) continue
      items.push(toItem(r))
      claimed.add(`${r.group}/${r.name}`)
    }
    if (items.length > 0) sections.push({ title: section.title, items })
  }

  // Anything left over, grouped by API group. Custom resources are the point
  // of this section, but leftover built-ins land here too rather than being
  // silently unreachable.
  const leftovers = new Map<string, NavItem[]>()
  for (const r of all) {
    const key = `${r.group}/${r.name}`
    if (claimed.has(key)) continue
    if (r.group === 'metrics.k8s.io') continue
    const bucket = leftovers.get(r.group) ?? []
    bucket.push(toItem(r))
    leftovers.set(r.group, bucket)
  }

  const customGroups = [...leftovers.entries()].sort(([a], [b]) => {
    // User-defined groups first: they are why someone opens this section.
    const aBuiltin = BUILTIN_GROUPS.has(a)
    const bBuiltin = BUILTIN_GROUPS.has(b)
    if (aBuiltin !== bBuiltin) return aBuiltin ? 1 : -1
    return a.localeCompare(b)
  })

  for (const [group, items] of customGroups) {
    items.sort((a, b) => a.label.localeCompare(b.label))
    sections.push({
      title: group === '' ? 'Core (other)' : group,
      items,
      collapsible: true,
    })
  }

  return sections
}
