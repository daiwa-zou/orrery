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

/* ---- saved searches ---------------------------------------------------- */

/**
 * A starred query: the resource that was being listed and the search text that
 * narrowed it. Teams operate the same handful of views every day — "my team's
 * failing pods", "everything in staging" — and re-typed them each time.
 */
const SAVED_KEY = 'orrery.saved'
const MAX_SAVED = 24

export interface SavedSearch {
  cluster: string
  group: string
  version: string
  resource: string
  kind: string
  namespaced: boolean
  /** Namespace the view was scoped to, empty for all namespaces. */
  namespace: string
  /** The search-bar text. Empty means "the unfiltered list". */
  q: string
}

/** Identity of a saved view: same resource, namespace and query is the same star. */
export function savedKey(v: SavedSearch): string {
  return [v.cluster, v.group, v.version, v.resource, v.namespace, v.q].join('|')
}

export function readSaved(cluster: string): SavedSearch[] {
  return readJSON<SavedSearch[]>(SAVED_KEY, []).filter(
    (v) => v && typeof v === 'object' && v.cluster === cluster && typeof v.q === 'string',
  )
}

/** Stars a view, newest first. Re-starring an identical view is a no-op. */
export function addSaved(view: SavedSearch): void {
  const all = readJSON<SavedSearch[]>(SAVED_KEY, [])
  if (all.some((v) => savedKey(v) === savedKey(view))) return
  writeJSON(SAVED_KEY, [view, ...all].slice(0, MAX_SAVED))
}

export function removeSaved(view: SavedSearch): void {
  const all = readJSON<SavedSearch[]>(SAVED_KEY, [])
  writeJSON(
    SAVED_KEY,
    all.filter((v) => savedKey(v) !== savedKey(view)),
  )
}

export function isSaved(view: SavedSearch): boolean {
  return readJSON<SavedSearch[]>(SAVED_KEY, []).some((v) => savedKey(v) === savedKey(view))
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
