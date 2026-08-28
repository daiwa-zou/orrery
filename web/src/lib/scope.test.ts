import { describe, expect, it } from 'vitest'
import {
  namespaceListReason,
  namespaceSearch,
  namespacesIn,
  scopeIsEmpty,
  onlyNamespace,
  scopeDescription,
  scopeLabel,
  toggleNamespace,
  withNamespaces,
} from './scope'

describe('namespacesIn', () => {
  it('reads a set from the repeated parameter', () => {
    expect(namespacesIn(new URLSearchParams('namespace=demo&namespace=payments'))).toEqual([
      'demo',
      'payments',
    ])
  })

  it('still reads the single-namespace links already in the wild', () => {
    expect(namespacesIn(new URLSearchParams('namespace=demo'))).toEqual(['demo'])
  })

  it('is empty when the parameter is absent, which means everywhere', () => {
    expect(namespacesIn(new URLSearchParams('q=web'))).toEqual([])
  })

  it('drops the empty value a cleared filter leaves behind', () => {
    expect(namespacesIn(new URLSearchParams('namespace=&namespace=demo'))).toEqual(['demo'])
  })

  it('drops a repeat, which would ask for the same objects twice', () => {
    expect(namespacesIn(new URLSearchParams('namespace=demo&namespace=demo'))).toEqual(['demo'])
  })
})

describe('withNamespaces', () => {
  it('replaces the scope rather than adding to it', () => {
    const next = withNamespaces(new URLSearchParams('namespace=old&q=web'), ['a', 'b'])
    expect(next.getAll('namespace')).toEqual(['a', 'b'])
    expect(next.get('q')).toBe('web')
  })

  it('drops the parameter entirely for the empty set', () => {
    const next = withNamespaces(new URLSearchParams('namespace=demo'), [])
    expect(next.has('namespace')).toBe(false)
  })

  it('resets the page, since page 4 of the old scope is not page 4 of the new', () => {
    const next = withNamespaces(new URLSearchParams('namespace=a&page=4'), ['b'])
    expect(next.has('page')).toBe(false)
  })
})

describe('namespaceSearch', () => {
  it('repeats the key, the way the API takes it', () => {
    expect(namespaceSearch(['a', 'b'])).toBe('?namespace=a&namespace=b')
  })

  it('says nothing at all for the empty set', () => {
    // A link to "everywhere" carries no scope, which is what makes every
    // cluster-scoped link in the nav unchanged by this.
    expect(namespaceSearch([])).toBe('')
  })
})

describe('scopeLabel', () => {
  it('names one namespace and counts the rest', () => {
    expect(scopeLabel([])).toBe('All')
    expect(scopeLabel(['demo'])).toBe('demo')
    expect(scopeLabel(['demo', 'payments'])).toBe('demo +1')
    expect(scopeLabel(['demo', 'payments', 'billing'])).toBe('demo +2')
  })

  it('says the whole of it in words, where there is room', () => {
    expect(scopeDescription([])).toMatch(/every namespace/i)
    expect(scopeDescription(['demo'])).toBe('Only demo')
    expect(scopeDescription(['a', 'b'])).toBe('2 namespaces: a, b')
  })
})

describe('toggleNamespace', () => {
  it('adds one that is not there and removes one that is', () => {
    expect(toggleNamespace(['a'], 'b')).toEqual(['a', 'b'])
    expect(toggleNamespace(['a', 'b'], 'a')).toEqual(['b'])
  })
})

describe('onlyNamespace', () => {
  it('answers only when there is no guess to make', () => {
    // Creating an object has to put it somewhere; picking one of four for the
    // reader would be worse than asking them.
    expect(onlyNamespace(['demo'])).toBe('demo')
    expect(onlyNamespace([])).toBeUndefined()
    expect(onlyNamespace(['a', 'b'])).toBeUndefined()
  })
})

/**
 * Nothing selected, which is not the same as everything.
 *
 * With every box ticked by default, unticking them all is a state a reader can
 * reach, and it shows nothing. The repeated parameter cannot spell it — an
 * empty list of names is what "everywhere" already looks like — so it gets its
 * own marker, or the picker would spring back to all under their hands.
 */
describe('the empty scope', () => {
  it('is not what an absent parameter means', () => {
    expect(scopeIsEmpty(new URLSearchParams(''))).toBe(false)
    expect(scopeIsEmpty(new URLSearchParams('namespace=demo'))).toBe(false)
    expect(scopeIsEmpty(new URLSearchParams('scope=none'))).toBe(true)
  })

  it('is written and cleared through the same call', () => {
    const none = withNamespaces(new URLSearchParams('namespace=demo'), [], true)
    expect(scopeIsEmpty(none)).toBe(true)
    expect(none.getAll('namespace')).toEqual([])

    // And naming a namespace again clears it, so the two can never both be set.
    const some = withNamespaces(none, ['demo'])
    expect(scopeIsEmpty(some)).toBe(false)
    expect(some.getAll('namespace')).toEqual(['demo'])
  })

  it('travels on a link, so the state survives navigation', () => {
    expect(namespaceSearch([], true)).toBe('?scope=none')
  })

  it('says so rather than reading as everywhere', () => {
    expect(scopeLabel([], true)).toBe('None')
    expect(scopeLabel([], false)).toBe('All')
    expect(scopeDescription([], true)).toMatch(/nothing to show/)
  })
})

/**
 * An empty namespace picker had one appearance and three causes. Two of them
 * — you may not list namespaces, the request did not come back — are the
 * common ones on exactly the deployments this dashboard is for, and both used
 * to render as the third: a cluster with no namespaces in it.
 */
describe('why the namespace list is empty', () => {
  it('says nothing when the answer really is none', () => {
    expect(namespaceListReason(false, undefined)).toBeUndefined()
  })

  it('does not present a list still loading as a list that came back empty', () => {
    expect(namespaceListReason(true, undefined)).toMatch(/Loading/)
  })

  it('names a denial as a denial', () => {
    const reason = namespaceListReason(false, { status: 403 })
    expect(reason).toMatch(/may not list namespaces/)
    // And says the rest of the console still works, since it does.
    expect(reason).toMatch(/still shown/)
  })

  it('does not blame RBAC for a request that failed', () => {
    const reason = namespaceListReason(false, { status: 500 })
    expect(reason).not.toMatch(/may not/)
    expect(reason).toMatch(/not a permission problem/)
  })
})
