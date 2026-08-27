import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  addColumn,
  addSaved,
  isSaved,
  navStateKey,
  readJSON,
  readRecents,
  readColumns,
  readSaved,
  recordRecent,
  removeColumn,
  removeSaved,
  savedKey,
  subscribeToKey,
  writeJSON,
  columnsIn,
  isSavedIn,
  COLUMNS_KEY,
  SAVED_KEY,
  type SavedSearch,
} from './storage'
import type { RecentResource } from './storage'

/**
 * The suite runs in a node environment, so `window` is stubbed with just the
 * pieces storage.ts touches. `throwing` simulates Safari private mode, where
 * localStorage throws instead of degrading.
 *
 * The event listeners are here because subscribeToKey listens for the
 * browser's `storage` event, which is how a write in another tab arrives.
 * `emitStorage` plays that other tab.
 */
function stubStorage(opts: { throwing?: boolean } = {}) {
  const store = new Map<string, string>()
  const handlers = new Set<(e: { key: string | null }) => void>()
  const localStorage = {
    getItem: (key: string): string | null => {
      if (opts.throwing) throw new Error('denied')
      return store.get(key) ?? null
    },
    setItem: (key: string, value: string): void => {
      if (opts.throwing) throw new Error('denied')
      store.set(key, value)
    },
  }
  vi.stubGlobal('window', {
    localStorage,
    addEventListener: (name: string, fn: (e: { key: string | null }) => void) => {
      if (name === 'storage') handlers.add(fn)
    },
    removeEventListener: (name: string, fn: (e: { key: string | null }) => void) => {
      if (name === 'storage') handlers.delete(fn)
    },
  })
  const emitStorage = (key: string | null) => {
    for (const fn of [...handlers]) fn({ key })
  }
  return Object.assign(store, { emitStorage })
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('readJSON / writeJSON', () => {
  it('round-trips a value', () => {
    stubStorage()
    writeJSON('k', { a: 1 })
    expect(readJSON('k', {})).toEqual({ a: 1 })
  })

  it('returns the fallback for missing keys and broken JSON', () => {
    const store = stubStorage()
    expect(readJSON('absent', 'fb')).toBe('fb')
    store.set('bad', '{not json')
    expect(readJSON('bad', 'fb')).toBe('fb')
  })

  it('rejects a stored non-array when the caller expects an array', () => {
    // A schema change or another tool on the same origin must not make
    // .filter() blow up mid-render.
    const store = stubStorage()
    store.set('k', '{"oops": true}')
    expect(readJSON<string[]>('k', [])).toEqual([])
  })

  it('degrades to the fallback when storage throws', () => {
    stubStorage({ throwing: true })
    expect(readJSON('k', 'fb')).toBe('fb')
    expect(() => writeJSON('k', 1)).not.toThrow()
  })
})

describe('navStateKey', () => {
  it('is scoped per cluster', () => {
    stubStorage()
    expect(navStateKey('prod')).not.toBe(navStateKey('staging'))
  })
})

describe('recents', () => {
  const entry = (cluster: string, resource: string): RecentResource => ({
    cluster,
    group: 'apps',
    version: 'v1',
    resource,
    kind: 'Deployment',
    namespaced: true,
  })

  beforeEach(() => {
    stubStorage()
  })

  it('records most recent first and filters by cluster', () => {
    recordRecent(entry('prod', 'deployments'))
    recordRecent(entry('staging', 'deployments'))
    recordRecent(entry('prod', 'statefulsets'))

    expect(readRecents('prod').map((r) => r.resource)).toEqual(['statefulsets', 'deployments'])
    expect(readRecents('staging').map((r) => r.resource)).toEqual(['deployments'])
  })

  it('promotes a revisit instead of duplicating it', () => {
    recordRecent(entry('prod', 'deployments'))
    recordRecent(entry('prod', 'statefulsets'))
    recordRecent(entry('prod', 'deployments'))

    expect(readRecents('prod').map((r) => r.resource)).toEqual(['deployments', 'statefulsets'])
  })

  it('caps the list at eight entries', () => {
    for (let i = 0; i < 10; i++) recordRecent(entry('prod', `r${i}`))
    expect(readRecents('prod')).toHaveLength(8)
    expect(readRecents('prod')[0].resource).toBe('r9')
  })
})

describe('saved searches', () => {
  const view = (over: Partial<SavedSearch> = {}): SavedSearch => ({
    cluster: 'prod',
    group: 'core',
    version: 'v1',
    resource: 'pods',
    kind: 'Pod',
    namespaced: true,
    namespace: '',
    q: 'status.phase=Running',
    ...over,
  })

  beforeEach(() => {
    stubStorage()
  })

  it('stars a view and reads it back for that cluster only', () => {
    addSaved(view())
    addSaved(view({ cluster: 'staging' }))

    expect(readSaved('prod')).toHaveLength(1)
    expect(readSaved('staging')).toHaveLength(1)
    expect(readSaved('other')).toEqual([])
  })

  it('treats an identical view as already starred rather than duplicating it', () => {
    addSaved(view())
    addSaved(view())

    expect(readSaved('prod')).toHaveLength(1)
    expect(isSaved(view())).toBe(true)
  })

  it('separates views that differ only by query or namespace', () => {
    addSaved(view({ q: 'a' }))
    addSaved(view({ q: 'b' }))
    addSaved(view({ q: 'a', namespace: 'demo' }))

    expect(readSaved('prod')).toHaveLength(3)
    expect(savedKey(view({ q: 'a' }))).not.toBe(savedKey(view({ q: 'b' })))
  })

  it('unstars only the matching view', () => {
    addSaved(view({ q: 'a' }))
    addSaved(view({ q: 'b' }))
    removeSaved(view({ q: 'a' }))

    expect(readSaved('prod').map((v) => v.q)).toEqual(['b'])
    expect(isSaved(view({ q: 'a' }))).toBe(false)
  })
})

describe('custom label columns', () => {
  beforeEach(() => {
    stubStorage()
  })

  it('keeps chosen columns per cluster and resource', () => {
    addColumn('prod', 'pods', 'team')
    addColumn('prod', 'deployments', 'version')
    addColumn('staging', 'pods', 'owner')

    expect(readColumns('prod', 'pods')).toEqual(['team'])
    expect(readColumns('prod', 'deployments')).toEqual(['version'])
    expect(readColumns('staging', 'pods')).toEqual(['owner'])
    expect(readColumns('prod', 'services')).toEqual([])
  })

  it('ignores a repeat and trims whitespace', () => {
    addColumn('prod', 'pods', 'team')
    addColumn('prod', 'pods', '  team  ')
    addColumn('prod', 'pods', '   ')

    expect(readColumns('prod', 'pods')).toEqual(['team'])
  })

  it('caps how many columns one resource can carry', () => {
    for (let i = 0; i < 10; i++) addColumn('prod', 'pods', `k${i}`)
    expect(readColumns('prod', 'pods')).toHaveLength(6)
  })

  it('removes only the named column', () => {
    addColumn('prod', 'pods', 'team')
    addColumn('prod', 'pods', 'owner')
    removeColumn('prod', 'pods', 'team')

    expect(readColumns('prod', 'pods')).toEqual(['owner'])
  })
})


/**
 * The reactive read path. These take the stored string rather than reading the
 * store, which is what lets a component parse them during render: the string
 * is a stable memo key, where an array parsed afresh each time would be a new
 * identity on every render.
 */
describe('reading from a raw stored string', () => {
  const view: SavedSearch = {
    cluster: 'lens-a',
    group: 'core',
    version: 'v1',
    resource: 'pods',
    kind: 'Pod',
    namespaced: true,
    namespace: 'demo',
    q: 'status.phase=Running',
  }

  it('finds a starred view and misses one that differs', () => {
    const raw = JSON.stringify([view])
    expect(isSavedIn(raw, view)).toBe(true)
    expect(isSavedIn(raw, { ...view, q: '' })).toBe(false)
  })

  it('reads the columns for one resource only', () => {
    const raw = JSON.stringify({ 'lens-a/pods': ['team'], 'lens-a/services': ['owner'] })
    expect(columnsIn(raw, 'lens-a', 'pods')).toEqual(['team'])
    expect(columnsIn(raw, 'lens-a', 'services')).toEqual(['owner'])
    expect(columnsIn(raw, 'lens-b', 'pods')).toEqual([])
  })

  // These run during render, so a store another tool on the same origin has
  // scribbled on must not take the page down with it.
  it('treats anything unparseable as empty rather than throwing', () => {
    for (const raw of [null, '', 'not json', '"a string"', '42']) {
      expect(isSavedIn(raw, view)).toBe(false)
      expect(columnsIn(raw, 'lens-a', 'pods')).toEqual([])
    }
    // Right type at the top level, wrong type underneath.
    expect(columnsIn(JSON.stringify({ 'lens-a/pods': 'team' }), 'lens-a', 'pods')).toEqual([])
    expect(columnsIn(JSON.stringify(['team']), 'lens-a', 'pods')).toEqual([])
    expect(isSavedIn(JSON.stringify({ not: 'an array' }), view)).toBe(false)
  })

  it('drops non-string column names', () => {
    const raw = JSON.stringify({ 'lens-a/pods': ['team', 7, null, 'owner'] })
    expect(columnsIn(raw, 'lens-a', 'pods')).toEqual(['team', 'owner'])
  })
})

describe('subscribeToKey', () => {
  // Subscriptions are module state, so they have to be released or they leak
  // into the next test and it passes for the wrong reason.
  const release: Array<() => void> = []
  const listen = (key: string, fn: () => void) => {
    release.push(subscribeToKey(key, fn))
  }

  beforeEach(() => {
    stubStorage()
  })
  afterEach(() => {
    while (release.length > 0) release.pop()!()
  })

  // The browser fires `storage` in other tabs only, so a write here has to
  // announce itself — otherwise the view that just changed the value is the
  // one view that does not re-render.
  it('notifies on a write in this tab', () => {
    const seen = vi.fn()
    listen(COLUMNS_KEY, seen)
    writeJSON(COLUMNS_KEY, { 'lens-a/pods': ['team'] })
    expect(seen).toHaveBeenCalledTimes(1)
  })

  it('does not notify for an unrelated key', () => {
    const seen = vi.fn()
    listen(COLUMNS_KEY, seen)
    writeJSON(SAVED_KEY, [])
    expect(seen).not.toHaveBeenCalled()
  })

  it('stops notifying once unsubscribed', () => {
    const seen = vi.fn()
    subscribeToKey(COLUMNS_KEY, seen)()
    writeJSON(COLUMNS_KEY, {})
    expect(seen).not.toHaveBeenCalled()
  })

  it('notifies when another tab writes the key', () => {
    const store = stubStorage()
    const seen = vi.fn()
    listen(SAVED_KEY, seen)
    store.emitStorage(SAVED_KEY)
    expect(seen).toHaveBeenCalledTimes(1)
    store.emitStorage('orrery.something-else')
    expect(seen).toHaveBeenCalledTimes(1)
    // A null key is the whole store being cleared, which affects every key.
    store.emitStorage(null)
    expect(seen).toHaveBeenCalledTimes(2)
  })

  // A failed write still means what is on screen may be wrong, so the
  // notification is outside the try.
  it('notifies even when the write could not be persisted', () => {
    stubStorage({ throwing: true })
    const seen = vi.fn()
    listen(COLUMNS_KEY, seen)
    writeJSON(COLUMNS_KEY, {})
    expect(seen).toHaveBeenCalledTimes(1)
  })
})
