import { groupSegment } from '../api/client'
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
  /**
   * The cluster's own structure: the resources the namespace picker does not
   * apply to. They sit above the sections rather than inside one, because the
   * sections are all namespace-filtered and these are not.
   */
  clusterScoped: NavItem[]
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
  // Events are deliberately absent: the dedicated Events page above the
  // sections replaces the raw core/v1 list.
]

/**
 * The cluster-scoped views, in the order they are read in.
 *
 * These used to be a section called "Cluster", which was a tautology in a
 * console that shows one cluster at a time — every item in this sidebar is
 * that cluster's. Worse, it put them among the namespace-filtered sections:
 * pick a namespace and every list below moves except these two, because a
 * node is not in a namespace and neither is a namespace. Sitting them beside
 * Overview and Events, above the sections, gives the sidebar a rule it can
 * keep: above the fold is the cluster, below it is what runs inside it.
 */
const CLUSTER_SCOPED = ['nodes', 'namespaces']

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
  const empty: Nav = { clusterScoped: [], primary: [], custom: [], rest: [] }
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

  const clusterScoped: NavItem[] = []
  for (const name of CLUSTER_SCOPED) {
    const r = byName.get(name)
    if (!r) continue
    clusterScoped.push(toItem(r))
    claimed.add(`${r.group}/${r.name}`)
  }

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

  return { clusterScoped, primary, custom, rest }
}

/**
 * Which resource serves each kind, for the places that hold a reference like
 * "Pod/web-1" and have to turn it into a link.
 *
 * An event and an overview warning both name their subject by kind, which is
 * not enough to address it: the console's URLs are keyed by group, version and
 * resource. Discovery has all three, but a kind can appear more than once in
 * it, so this picks between the candidates by two rules.
 *
 * Built-in groups beat custom ones, because an operator that ships its own
 * `Service` CRD must not capture the events about core/v1 Services — the
 * shadowed kind is the one almost everybody means. Within a tier, a preferred
 * version beats a superseded one, for the same reason discovery marks it.
 */
export function resourcesByKind(discovery?: DiscoveryResponse): Map<string, APIResource> {
  const map = new Map<string, APIResource>()
  for (const group of discovery?.groups ?? []) {
    for (const res of group.resources) {
      const key = res.kind.toLowerCase()
      const existing = map.get(key)
      const better =
        !existing ||
        (isCustomGroup(existing.group) && !isCustomGroup(res.group)) ||
        (isCustomGroup(existing.group) === isCustomGroup(res.group) &&
          res.preferred &&
          !existing.preferred)
      if (better) map.set(key, res)
    }
  }
  return map
}

/**
 * The console path for a "Kind/name" reference, or undefined when it cannot be
 * resolved — an unknown kind, a malformed reference, or discovery that has not
 * arrived yet.
 *
 * Undefined is the load-bearing case. A row that cannot be resolved must not
 * be dressed as a link: sending someone to a 404 is worse than leaving the
 * reference as text, and it is indistinguishable to them from the console
 * being broken.
 */
export function objectPath(
  cluster: string,
  byKind: Map<string, APIResource>,
  ref: string,
  namespace?: string,
): string | undefined {
  const [kind, name] = ref.split('/')
  if (!kind || !name) return undefined
  const res = byKind.get(kind.toLowerCase())
  if (!res) return undefined
  // Cluster-scoped objects take "_" in the namespace position, and so does a
  // namespaced one whose namespace we were not told.
  const ns = res.namespaced ? namespace || '_' : '_'
  return `/c/${cluster}/r/${groupSegment(res.group)}/${res.version}/${res.name}/${ns}/${name}`
}
