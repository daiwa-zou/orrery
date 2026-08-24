import type { Row } from '../api/types'

/* Multi-select state for DataTable, kept as pure functions so the toggle
   semantics are testable without rendering. The selection is a set of row
   keys, not row objects: rows are replaced wholesale on every refetch, and
   keys survive that. */

/** Stable identity for a row — the same key DataTable uses for rendering. */
export function rowKey(row: Row): string {
  return row.uid || `${row.namespace}/${row.name}`
}

export function toggleRow(selected: ReadonlySet<string>, key: string): Set<string> {
  const next = new Set(selected)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  return next
}

/**
 * Header-checkbox semantics: if every visible row is selected, deselect them
 * all; otherwise select them all. Keys not on this page are left alone, so
 * select-all on page 2 does not silently discard a selection made on page 1.
 */
export function toggleAll(selected: ReadonlySet<string>, keys: string[]): Set<string> {
  const next = new Set(selected)
  const all = keys.length > 0 && keys.every((k) => next.has(k))
  for (const k of keys) {
    if (all) next.delete(k)
    else next.add(k)
  }
  return next
}
