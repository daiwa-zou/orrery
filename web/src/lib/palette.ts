import { isCustomGroup } from '../components/nav'

/**
 * The command palette's pure logic: how a resource is ranked against a query,
 * and where the cursor sits once the list has been rearranged underneath it.
 *
 * It lives here rather than beside the component so it can be exercised
 * directly, which is how every other piece of derived logic in this app is
 * tested.
 */

/** The subset of a resource the ranker needs, so it can be tested on its own. */
export interface RankableResource {
  kind: string
  name: string
  singularName?: string
  shortNames?: string[]
  group: string
}

/**
 * Scores a resource against a query. Lower is better; Infinity means no match.
 *
 * The ordering is deliberate. An exact short-name hit wins outright, because
 * short names are what people type when they know exactly what they want —
 * "cm" should land on ConfigMaps, not on whatever else happens to contain
 * those two letters. Exact full names come next, then prefixes, then
 * substrings, with the API group considered last so it never outranks a kind.
 */
export function scoreResource(r: RankableResource, query: string): number {
  const q = query.trim().toLowerCase()
  if (!q) return 0

  const kind = r.kind.toLowerCase()
  const name = r.name.toLowerCase()
  const singular = (r.singularName ?? '').toLowerCase()
  const shorts = (r.shortNames ?? []).map((s) => s.toLowerCase())
  const group = r.group.toLowerCase()

  if (shorts.includes(q)) return 0
  if (kind === q || name === q || singular === q) return 1
  if (shorts.some((s) => s.startsWith(q))) return 2
  if (kind.startsWith(q)) return 3
  if (name.startsWith(q) || (singular !== '' && singular.startsWith(q))) return 4
  if (group.startsWith(q)) return 5
  if (kind.includes(q)) return 6
  if (name.includes(q)) return 7
  if (group.includes(q)) return 8
  return Infinity
}

/** Ranks and filters resources for a query, best first. */
export function rankResources<T extends RankableResource>(items: T[], query: string): T[] {
  return items
    .map((item) => ({ item, score: scoreResource(item, query) }))
    .filter(({ score }) => score !== Infinity)
    .sort((a, b) => {
      if (a.score !== b.score) return a.score - b.score
      // Built-ins before custom resources at equal score, then alphabetical, so
      // an operator's CRD cannot bury the kind of the same name people meant.
      const aCustom = isCustomGroup(a.item.group)
      const bCustom = isCustomGroup(b.item.group)
      if (aCustom !== bCustom) return aCustom ? 1 : -1
      return a.item.kind.localeCompare(b.item.kind)
    })
    .map(({ item }) => item)
}

/**
 * Where the cursor sits, given the entry it was last on.
 *
 * The palette tracks a selected id rather than a selected row number because
 * four of its six sections arrive asynchronously — discovery, namespaces, the
 * cluster list and the fleet search — and they do not all append at the end.
 * Resources land in the middle, above Namespaces, Clusters and Objects, so a
 * row number chosen before discovery resolved points at a different entry
 * afterwards, and Enter opens something nobody picked.
 *
 * An entry that has gone entirely — the query narrowed past it — falls back to
 * the top, which is the best match rather than wherever the list happened to
 * end.
 */
export function selectedIndex(entries: { id: string }[], selectedId: string | undefined): number {
  if (selectedId === undefined) return 0
  const i = entries.findIndex((e) => e.id === selectedId)
  return i >= 0 ? i : 0
}
