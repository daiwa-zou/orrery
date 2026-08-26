import { groupSegment } from '../api/client'
import type { ObjectRef } from '../api/types'

/**
 * Presenting one object's neighbourhood.
 *
 * The server names each edge — owner, child, node, reference — and this decides
 * what those words look like to a reader and what order they arrive in. The
 * order is the order of the question being asked: what made this, what did it
 * make, where does it run, what does it talk to, what does it read.
 */

/** The headings, in the order they should appear. */
const RELATION_ORDER: [relation: string, label: string][] = [
  ['owner', 'Owned by'],
  ['child', 'Owns'],
  ['descendant', 'Descendants'],
  ['node', 'Scheduled on'],
  ['hosts', 'Running here'],
  ['selects', 'Selects'],
  ['selected-by', 'Selected by'],
  ['reference', 'References'],
]

const LABELS = new Map(RELATION_ORDER)

export interface RelationGroup {
  relation: string
  label: string
  refs: ObjectRef[]
}

/**
 * Turns a flat list of references into ordered, labelled groups.
 *
 * An unrecognised relation gets its own group rather than being dropped: the
 * server's vocabulary is allowed to grow, and a console that silently hides
 * whatever it has not been taught about is worse than one showing a heading
 * it did not choose the wording for.
 */
export function groupRelations(refs: ObjectRef[] | undefined): RelationGroup[] {
  if (!refs || refs.length === 0) return []

  const byRelation = new Map<string, ObjectRef[]>()
  for (const ref of refs) {
    const list = byRelation.get(ref.relation)
    if (list) list.push(ref)
    else byRelation.set(ref.relation, [ref])
  }

  const out: RelationGroup[] = []
  for (const [relation, label] of RELATION_ORDER) {
    const group = byRelation.get(relation)
    if (group) {
      out.push({ relation, label, refs: sortRefs(group) })
      byRelation.delete(relation)
    }
  }
  // Whatever is left is a relation this build has not been taught; show it
  // under its own name rather than losing it.
  for (const [relation, group] of [...byRelation].sort(([a], [b]) => a.localeCompare(b))) {
    out.push({ relation, label: LABELS.get(relation) ?? relation, refs: sortRefs(group) })
  }
  return out
}

/**
 * Orders one group: shallower ownership hops first, then by kind and name, so
 * a Deployment's ReplicaSet precedes the pods beneath it and the list does not
 * reshuffle when the server returns the same set in a different order.
 */
function sortRefs(refs: ObjectRef[]): ObjectRef[] {
  return [...refs].sort((a, b) => {
    const depth = (a.depth ?? 0) - (b.depth ?? 0)
    if (depth !== 0) return depth
    if (a.kind !== b.kind) return a.kind.localeCompare(b.kind)
    return a.name.localeCompare(b.name)
  })
}

/**
 * The console route for a reference, or undefined when there is nowhere to go.
 *
 * Built from the fields the server resolved through discovery rather than from
 * the kind. Guessing the plural of a kind is what the owner links used to do,
 * and a CRD may spell its plural however it likes — so a guess is a link that
 * 404s for exactly the objects nobody else knows how to find.
 */
export function relatedHref(cluster: string, ref: ObjectRef): string | undefined {
  if (!ref.resource || !ref.version || !ref.name) return undefined
  const namespace = ref.namespace || '_'
  return `/c/${cluster}/r/${groupSegment(ref.group ?? '')}/${ref.version}/${ref.resource}/${namespace}/${ref.name}`
}
