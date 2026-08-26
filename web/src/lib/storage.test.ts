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
  writeJSON,
  type SavedSearch,
} from './storage'
import type { RecentResource } from './storage'

/**
 * The suite runs in a node environment, so `window` is stubbed with just the
 * piece storage.ts touches. `throwing` simulates Safari private mode, where
 * localStorage throws instead of degrading.
 */
function stubStorage(opts: { throwing?: boolean } = {}) {
  const store = new Map<string, string>()
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
  vi.stubGlobal('window', { localStorage })
  return store
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
