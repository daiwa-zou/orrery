/**
 * Typed localStorage access.
 *
 * Every call is guarded: Safari in private mode throws from `localStorage`
 * rather than returning null, and a UI preference is never worth taking the
 * page down for.
 */

import { splitSelector } from './labels'

/**
 * The stored string for a key, or null. This is the snapshot the reactive
 * hook reads: a primitive, so it is stable between renders and can be
 * compared cheaply.
 */
export function readRaw(key: string): string | null {
  try {
    return window.localStorage.getItem(key)
  } catch {
    return null
  }
}

/** Parses a stored array, treating anything unexpected as empty. */
export function parseArray<T>(raw: string | null): T[] {
  if (raw === null) return []
  try {
    const parsed: unknown = JSON.parse(raw)
    return Array.isArray(parsed) ? (parsed as T[]) : []
  } catch {
    return []
  }
}

/* ---- change notification ------------------------------------------------
 *
 * localStorage is an external store, and React has a primitive for reading
 * one — but the browser only fires `storage` in *other* tabs. A write in this
 * tab has to announce itself, or the view that just changed the value is the
 * one view that does not re-render.
 */

type Listener = () => void
const listeners = new Map<string, Set<Listener>>()

/** Subscribes to changes for one key, from this tab or any other. */
export function subscribeToKey(key: string, fn: Listener): () => void {
  let set = listeners.get(key)
  if (!set) {
    set = new Set()
    listeners.set(key, set)
  }
  set.add(fn)

  const onStorage = (e: StorageEvent) => {
    // A null key means the whole store was cleared.
    if (e.key === null || e.key === key) fn()
  }
  window.addEventListener('storage', onStorage)

  return () => {
    set?.delete(fn)
    if (set && set.size === 0) listeners.delete(key)
    window.removeEventListener('storage', onStorage)
  }
}

function notify(key: string): void {
  for (const fn of listeners.get(key) ?? []) fn()
}

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
  // Outside the try: subscribers should hear about it either way, since a
  // failed write still means the value they are showing may be wrong.
  notify(key)
}

/** Open nav sections are remembered per cluster, since their resources differ. */
export function navStateKey(cluster: string): string {
  return `orrery.nav.${cluster}`
}

/**
 * A stored list of strings, given the raw JSON. See isSavedIn for why these
 * take the string rather than reading the store.
 *
 * The fallback is for absent or unreadable, not for empty: an empty list is a
 * real answer — every section collapsed by hand — and replacing it with the
 * defaults would reopen them all on the next visit.
 */
export function stringArrayIn(raw: string | null, fallback: string[]): string[] {
  if (raw === null) return fallback
  try {
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return fallback
    return parsed.filter((v): v is string => typeof v === 'string')
  } catch {
    return fallback
  }
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
 * A starred view: the resource that was being listed and the whole search that
 * narrowed it. Teams operate the same handful of views every day — "my team's
 * failing pods", "everything in staging" — and re-typed them each time.
 */
export const SAVED_KEY = 'orrery.saved'
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
  /** Free text. Empty means "no words to match". */
  q: string
  /**
   * The label and field selectors, exactly as the list endpoint takes them.
   *
   * These are the whole point and they were missing: a view is starred
   * because of `app=web,tier!=cache`, and storing only the free text saved
   * the resource and threw the reason away.
   */
  labelSelector: string
  fieldSelector: string
  /** What the reader called it. Empty falls back to describeSaved. */
  name: string
}

/**
 * Fills in a stored view from before the selectors and the name existed, so
 * an upgrade keeps everyone's stars instead of quietly dropping them.
 */
function hydrate(v: SavedSearch): SavedSearch {
  return {
    ...v,
    q: v.q ?? '',
    labelSelector: v.labelSelector ?? '',
    fieldSelector: v.fieldSelector ?? '',
    name: v.name ?? '',
  }
}

/**
 * Identity of a saved view: the same resource narrowed the same way is the
 * same star. The name is not part of it — renaming a view must not turn it
 * into a second one.
 */
export function savedKey(v: SavedSearch): string {
  return [
    v.cluster,
    v.group,
    v.version,
    v.resource,
    v.namespace,
    v.q ?? '',
    v.labelSelector ?? '',
    v.fieldSelector ?? '',
  ].join('|')
}

/** A readable summary of what a view actually selects, for an unnamed star. */
export function describeSaved(v: SavedSearch): string {
  const terms = [
    ...splitSelector(v.labelSelector ?? ''),
    ...splitSelector(v.fieldSelector ?? ''),
    ...(v.q ? [v.q] : []),
  ]
  const where = v.namespace || (v.namespaced ? 'all namespaces' : '')
  if (terms.length === 0) return where ? `${v.kind}s in ${where}` : `All ${v.kind}s`
  return `${v.kind}s · ${terms.join(', ')}`
}

/** The name to show: what the reader called it, or what it selects. */
export function savedLabel(v: SavedSearch): string {
  return v.name?.trim() || describeSaved(v)
}

export function readSaved(cluster: string): SavedSearch[] {
  return readJSON<SavedSearch[]>(SAVED_KEY, [])
    .filter((v) => v && typeof v === 'object' && v.cluster === cluster)
    .map(hydrate)
}

/**
 * Stars a view, newest first. Re-saving one that is already stored replaces
 * it in place rather than being dropped, so a rename takes effect without
 * shuffling the list.
 */
export function addSaved(view: SavedSearch): void {
  const all = readJSON<SavedSearch[]>(SAVED_KEY, [])
  const at = all.findIndex((v) => savedKey(v) === savedKey(view))
  if (at >= 0) {
    const next = [...all]
    next[at] = view
    writeJSON(SAVED_KEY, next)
    return
  }
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
  return isSavedIn(readRaw(SAVED_KEY), view)
}

/**
 * Whether a view is starred, given the stored JSON rather than the store.
 *
 * Taking the raw string is what lets the caller read this during render: the
 * string is a stable value it can key a memo on, where an array parsed afresh
 * each time would be a new identity every render.
 */
export function isSavedIn(raw: string | null, view: SavedSearch): boolean {
  return savedIn(raw, view) !== undefined
}

/**
 * The stored view matching this one, which is where its name lives.
 *
 * A view built from the current page knows what it selects but not what the
 * reader called it, so anything that wants to *say* the name — a toast, a
 * tooltip — has to look it up rather than describe it afresh.
 */
export function savedIn(raw: string | null, view: SavedSearch): SavedSearch | undefined {
  return parseArray<SavedSearch>(raw)
    .map(hydrate)
    .find((v) => savedKey(v) === savedKey(view))
}

/* ---- custom columns ---------------------------------------------------- */

/**
 * Extra label columns, per cluster and resource.
 *
 * Well-known kinds get hand-tuned tables and CRDs get their own
 * additionalPrinterColumns, which matches what kubectl shows. What was missing
 * is the escape hatch: the label a team actually navigates by — owner,
 * version, the ticket that caused the rollout — lives in their own labels and
 * had nowhere to appear except the catch-all Labels column.
 *
 * Labels only, not annotations: list rows already carry labels, so a label
 * column costs nothing extra on the wire, while annotations would need the
 * server to project them.
 */
export const COLUMNS_KEY = 'orrery.columns'
const MAX_COLUMNS_PER_RESOURCE = 6

function columnsKeyFor(cluster: string, resource: string): string {
  return `${cluster}/${resource}`
}

type ColumnMap = Record<string, string[]>

export function readColumns(cluster: string, resource: string): string[] {
  return columnsIn(readRaw(COLUMNS_KEY), cluster, resource)
}

/** The chosen columns for one resource, given the stored JSON. See isSavedIn. */
export function columnsIn(raw: string | null, cluster: string, resource: string): string[] {
  let all: ColumnMap = {}
  try {
    const parsed: unknown = raw === null ? {} : JSON.parse(raw)
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      all = parsed as ColumnMap
    }
  } catch {
    // A corrupt store is an empty one, not a crashed page.
  }
  const keys = all[columnsKeyFor(cluster, resource)]
  return Array.isArray(keys) ? keys.filter((k) => typeof k === 'string') : []
}

export function addColumn(cluster: string, resource: string, label: string): void {
  const trimmed = label.trim()
  if (!trimmed) return
  const all = readJSON<ColumnMap>(COLUMNS_KEY, {})
  const k = columnsKeyFor(cluster, resource)
  const current = Array.isArray(all?.[k]) ? all[k] : []
  if (current.includes(trimmed)) return
  writeJSON(COLUMNS_KEY, {
    ...all,
    [k]: [...current, trimmed].slice(0, MAX_COLUMNS_PER_RESOURCE),
  })
}

export function removeColumn(cluster: string, resource: string, label: string): void {
  const all = readJSON<ColumnMap>(COLUMNS_KEY, {})
  const k = columnsKeyFor(cluster, resource)
  const current = Array.isArray(all?.[k]) ? all[k] : []
  writeJSON(COLUMNS_KEY, { ...all, [k]: current.filter((c) => c !== label) })
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
