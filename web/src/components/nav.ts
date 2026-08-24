import type { APIResource, DiscoveryResponse } from '../api/types'

/**
 * The navigation is curated, and deliberately short.
 *
 * A cluster serves upwards of sixty listable resources across twenty API
 * groups before anyone installs an operator. Showing all of them gives every
 * resource equal weight, which in practice means `flowcontrol.apiserver.k8s.io`
 * competes for attention with Pods. So the nav is split into three tiers: the
 * handful people open daily, the custom resources that are usually the reason
 * to go looking, and everything else behind one disclosure.
 *
 * Nothing is unreachable — the command palette searches all of it.
 */
export interface Nav {
  /** Always visible, grouped into named sections. */
  primary: NavSection[]
  /** Resources from API groups Kubernetes does not ship: real CRDs. */
  custom: NavItem[]
  /** Everything else, flat and alphabetical. */
  rest: NavItem[]
}

export interface NavSection {
  title: string
  items: NavItem[]
}

export interface NavItem {
  label: string
  group: string
  version: string
  resource: string
  namespaced: boolean
  kind: string
}

/**
 * The daily set. Anything not named here still appears under "All resources"
 * and in the palette, so the bar for inclusion is "most people, most days"
 * rather than "someone, sometime".
 */
const CURATED: { title: string; resources: string[] }[] = [
  {
    title: 'Workloads',
    resources: ['pods', 'deployments', 'statefulsets', 'daemonsets', 'jobs', 'cronjobs'],
  },
  { title: 'Networking', resources: ['services', 'ingresses'] },
  { title: 'Storage', resources: ['persistentvolumeclaims'] },
  { title: 'Configuration', resources: ['configmaps', 'secrets'] },
  { title: 'Cluster', resources: ['nodes', 'namespaces', 'events'] },
]

/**
 * Kubernetes' own API groups that predate the `*.k8s.io` convention and so
 * cannot be recognised by suffix.
 */
const LEGACY_BUILTIN_GROUPS = new Set(['', 'apps', 'batch', 'autoscaling', 'policy', 'extensions'])

/**
 * Whether a group belongs to a user rather than to Kubernetes.
 *
 * This is a rule rather than a list on purpose. An enumerated set of built-in
 * groups silently rots: `resource.k8s.io` shipped and immediately started
 * masquerading as somebody's CRD, which is exactly the noise the Custom
 * resources section exists to avoid. Every upstream group is either a legacy
 * bare name or lives under `.k8s.io`, while third-party CRDs use their own
 * domains — cert-manager.io, argoproj.io, monitoring.coreos.com.
 *
 * Projects on `x-k8s.io` (Cluster API and friends) deliberately fall on the
 * custom side: they really are installed as CRDs.
 */
export function isCustomGroup(group: string): boolean {
  if (LEGACY_BUILTIN_GROUPS.has(group)) return false
  return group !== 'k8s.io' && !group.endsWith('.k8s.io')
}

/**
 * Whether a resource can be opened as a list at all.
 *
 * metrics.k8s.io advertises `list` but is an aggregated API with no watch
 * support, so the cache backing every list view cannot be built for it —
 * navigating there produces an error page. Usage is surfaced through the
 * dedicated metrics endpoints instead. The palette and the nav must agree on
 * this, or search offers a link that is guaranteed to break.
 */
export function isBrowsable(group: string): boolean {
  return group !== 'metrics.k8s.io'
}

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

const byKind = (a: NavItem, b: NavItem) => a.kind.localeCompare(b.kind)

export function buildNav(discovery?: DiscoveryResponse): Nav {
  const empty: Nav = { primary: [], custom: [], rest: [] }
  if (!discovery) return empty

  const all: APIResource[] = discovery.groups.flatMap((g) => g.resources)

  const byName = new Map<string, APIResource>()
  for (const r of all) {
    // Prefer the built-in owner of a name so a CRD called "jobs" cannot
    // displace batch/v1 Jobs from the Workloads section.
    const existing = byName.get(r.name)
    if (!existing || (!isCustomGroup(r.group) && isCustomGroup(existing.group))) {
      byName.set(r.name, r)
    }
  }

  const claimed = new Set<string>()
  const primary: NavSection[] = []

  for (const section of CURATED) {
    const items: NavItem[] = []
    for (const name of section.resources) {
      const r = byName.get(name)
      if (!r) continue
      items.push(toItem(r))
      claimed.add(`${r.group}/${r.name}`)
    }
    if (items.length > 0) primary.push({ title: section.title, items })
  }

  const custom: NavItem[] = []
  const rest: NavItem[] = []

  for (const r of all) {
    if (claimed.has(`${r.group}/${r.name}`)) continue
    if (!isBrowsable(r.group)) continue

    if (isCustomGroup(r.group)) custom.push(toItem(r))
    else rest.push(toItem(r))
  }

  custom.sort(byKind)
  rest.sort(byKind)

  return { primary, custom, rest }
}

/** Total resources reachable from the nav, for the "All resources" count. */
export function navItemCount(nav: Nav): number {
  return (
    nav.primary.reduce((n, s) => n + s.items.length, 0) + nav.custom.length + nav.rest.length
  )
}
