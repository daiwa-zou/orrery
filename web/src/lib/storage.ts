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
    const parsed = JSON.parse(raw) as T
    // The shape check matters: every current caller stores an array, and a
    // stray non-array (schema change, another tool on the same origin) would
    // throw at .filter()/.includes() in the middle of a render.
    if (Array.isArray(fallback) && !Array.isArray(parsed)) return fallback
    return parsed
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
  return `orrery.nav.${cluster}`
}

/** Recently opened resources, used to fill the palette before anything is typed. */
const RECENTS_KEY = 'orrery.recents'
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
  return readJSON<RecentResource[]>(RECENTS_KEY, []).filter(
    (r) => r && typeof r === 'object' && r.cluster === cluster,
  )
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

/* ---- theme ------------------------------------------------------------- */

export type Theme = 'dark' | 'light'

const THEME_KEY = 'orrery.theme'

/**
 * The theme currently applied to the document.
 *
 * Read from the attribute rather than from storage, because index.html has
 * already resolved "no stored preference" against the system setting before
 * first paint. Storage alone cannot answer what is on screen.
 */
export function currentTheme(): Theme {
  return document.documentElement.getAttribute('data-theme') === 'light' ? 'light' : 'dark'
}

/** Applies a theme and remembers it, overriding the system preference. */
export function setTheme(theme: Theme): void {
  document.documentElement.setAttribute('data-theme', theme)
  try {
    window.localStorage.setItem(THEME_KEY, theme)
  } catch {
    // Preference does not stick; the page is still themed for this session.
  }
}
