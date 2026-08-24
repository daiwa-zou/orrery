/**
 * Typed localStorage access.
 *
 * Every call is guarded: Safari in private mode throws from `localStorage`
 * rather than returning null, and a UI preference is never worth taking the
 * page down for.
 */

export function readJSON<T>(key: string, fallback: T): T {
  try {
    const raw = window.localStorage.getItem(key)
    if (raw === null) return fallback
    return JSON.parse(raw) as T
  } catch {
    return fallback
  }
}

export function writeJSON(key: string, value: unknown): void {
  try {
    window.localStorage.setItem(key, JSON.stringify(value))
  } catch {
    // Quota exceeded or storage disabled — the preference simply does not stick.
  }
}

/** Open nav sections are remembered per cluster, since their resources differ. */
export function navStateKey(cluster: string): string {
  return `clusterlens.nav.${cluster}`
}

/** Recently opened resources, used to fill the palette before anything is typed. */
const RECENTS_KEY = 'clusterlens.recents'
const MAX_RECENTS = 8

export interface RecentResource {
  cluster: string
  group: string
  version: string
  resource: string
  kind: string
  namespaced: boolean
}

export function readRecents(cluster: string): RecentResource[] {
  return readJSON<RecentResource[]>(RECENTS_KEY, []).filter((r) => r.cluster === cluster)
}

/**
 * Records a visit, most recent first. Entries are keyed by cluster and
 * resource so revisiting something promotes it rather than duplicating it.
 */
export function recordRecent(entry: RecentResource): void {
  const all = readJSON<RecentResource[]>(RECENTS_KEY, [])
  const key = (r: RecentResource) => `${r.cluster}/${r.group}/${r.resource}`
  const next = [entry, ...all.filter((r) => key(r) !== key(entry))].slice(0, MAX_RECENTS)
  writeJSON(RECENTS_KEY, next)
}
