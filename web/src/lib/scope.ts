/**
 * The namespace scope, as it travels in the URL.
 *
 * One namespace was never the shape of the question. An incident spans two, a
 * team owns four, and answering that by picking one at a time and holding the
 * results in your head is the console making the reader do the join. So the
 * scope is a *set*, carried as a repeated `namespace` parameter — the same
 * spelling the API takes, and the same spelling `where` already uses on the
 * resource lists.
 *
 * An absent parameter keeps the meaning it has always had: everywhere the
 * caller may look. That is what makes this backward compatible with every link
 * ever shared — `?namespace=demo` is a set of one, and no link is a set of
 * none by accident.
 *
 * Which leaves the one state the repeated parameter cannot spell. With every
 * namespace ticked by default, unticking them all is a thing a reader can do,
 * and it means *nothing selected*, not *everywhere* — the two would otherwise
 * collapse into the same empty list of names and the picker would spring back
 * to all under their hands. It travels as `scope=none`: one state, one
 * spelling, and a link that carries it.
 */

/** The marker for a scope with no namespaces in it at all. */
export const NO_NAMESPACES = 'none'

/** Whether the scope selects nothing, which is not the same as everything. */
export function scopeIsEmpty(params: URLSearchParams): boolean {
  return params.get('scope') === NO_NAMESPACES
}

/** The namespaces in a URL, in the order they appear, without repeats. */
export function namespacesIn(params: URLSearchParams): string[] {
  const out: string[] = []
  for (const value of params.getAll('namespace')) {
    const ns = value.trim()
    // A repeat asks for the same objects twice, and an empty value is how a
    // cleared filter arrives; the server drops both, and so does this.
    if (ns !== '' && !out.includes(ns)) out.push(ns)
  }
  return out
}

/**
 * The same params with the scope replaced.
 *
 * `empty` is the deliberate nothing — every box unticked — as against an empty
 * `namespaces` with `empty` false, which is the default everywhere.
 */
export function withNamespaces(
  params: URLSearchParams,
  namespaces: string[],
  empty = false,
): URLSearchParams {
  const next = new URLSearchParams(params)
  next.delete('namespace')
  next.delete('scope')
  if (empty) next.set('scope', NO_NAMESPACES)
  for (const ns of namespaces) next.append('namespace', ns)
  // Changing scope invalidates the page you were on: page 4 of the old scope
  // is not page 4 of the new one.
  next.delete('page')
  return next
}

/**
 * The scope as a query string for a link — `?namespace=a&namespace=b`, or
 * nothing at all when the scope is everywhere.
 */
export function namespaceSearch(namespaces: string[], empty = false): string {
  if (empty) return `?scope=${NO_NAMESPACES}`
  if (namespaces.length === 0) return ''
  const params = new URLSearchParams()
  for (const ns of namespaces) params.append('namespace', ns)
  return `?${params.toString()}`
}

/**
 * What the picker says when it is closed.
 *
 * A count rather than a list past the first: three namespace names do not fit
 * a 250px column, and truncating them mid-name says less than saying how many
 * there are. The first is kept because a scope of one is the common case and
 * naming it is the whole point.
 */
export function scopeLabel(namespaces: string[], empty = false): string {
  if (empty) return 'None'
  if (namespaces.length === 0) return 'All'
  if (namespaces.length === 1) return namespaces[0]
  return `${namespaces[0]} +${namespaces.length - 1}`
}

/** The scope in words, for a tooltip or a screen reader. */
export function scopeDescription(namespaces: string[], empty = false): string {
  if (empty) return 'No namespaces selected, so there is nothing to show'
  if (namespaces.length === 0) return 'Every namespace you may read'
  if (namespaces.length === 1) return `Only ${namespaces[0]}`
  return `${namespaces.length} namespaces: ${namespaces.join(', ')}`
}

/** The set with one namespace added or removed. */
export function toggleNamespace(namespaces: string[], ns: string): string[] {
  return namespaces.includes(ns) ? namespaces.filter((n) => n !== ns) : [...namespaces, ns]
}

/**
 * The single namespace a scope names, or undefined.
 *
 * Some things take exactly one — creating an object has to put it somewhere,
 * and the pod metrics endpoint answers for one namespace. Guessing which of
 * four the reader meant would be worse than asking, so this answers only when
 * there is no guess to make.
 */
export function onlyNamespace(namespaces: string[]): string | undefined {
  return namespaces.length === 1 ? namespaces[0] : undefined
}

/**
 * Why the namespace list is empty, when it is empty for a reason.
 *
 * An empty picker has three quite different causes and used to look the same
 * for all of them: this cluster really has no namespaces, you may not list
 * them, or the request did not come back. The last two are the common ones —
 * a narrowly bound user is precisely who cannot list namespaces — and a
 * control that shows nothing without saying why reads as the first.
 *
 * Returns undefined for the one case that needs no explaining: the request
 * answered, and the answer was none. That is the only state the bare empty
 * list was ever telling the truth about.
 */
export function namespaceListReason(isLoading: boolean, error: unknown): string | undefined {
  if (isLoading) return 'Loading namespaces…'
  if (!error) return undefined
  const err = error as { status?: number }
  if (err?.status === 403) {
    return 'You may not list namespaces on this cluster, so none are offered here. ' +
      'Resources you are allowed to read are still shown.'
  }
  return 'Namespaces could not be listed, so none are offered here. ' +
    'This is not a permission problem; try again.'
}
