/**
 * Client-side validation for label and annotation keys/values, mirroring
 * apimachinery's IsQualifiedName / IsValidLabelValue. Catching a bad key
 * before the PATCH gives an inline message instead of a server error toast.
 */

const NAME_RE = /^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$/
const DNS_LABEL_RE = /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/

/**
 * Validates a qualified name — the key syntax shared by labels and
 * annotations: an optional DNS-subdomain prefix, "/", then a short name.
 * Returns a message, or undefined when valid.
 */
export function validateMetaKey(key: string): string | undefined {
  if (key === '') return 'A key is required.'

  const parts = key.split('/')
  if (parts.length > 2) return 'A key may contain at most one "/".'

  if (parts.length === 2) {
    const prefix = parts[0]
    if (prefix === '') return 'The prefix before "/" must not be empty.'
    if (prefix.length > 253) return 'The key prefix must be at most 253 characters.'
    if (!prefix.split('.').every((p) => DNS_LABEL_RE.test(p))) {
      return 'The key prefix must be a lowercase DNS subdomain (e.g. example.com).'
    }
  }

  const name = parts[parts.length - 1]
  if (name === '') return 'The key name after "/" must not be empty.'
  if (name.length > 63) return 'The key name must be at most 63 characters.'
  if (!NAME_RE.test(name)) {
    return 'The key name must start and end with a letter or digit, with only ".", "-" or "_" between.'
  }
  return undefined
}

/**
 * Validates a label value. Annotations skip this — their values are
 * unrestricted. An empty label value is legal.
 */
export function validateLabelValue(value: string): string | undefined {
  if (value === '') return undefined
  if (value.length > 63) return 'A label value must be at most 63 characters.'
  if (!NAME_RE.test(value)) {
    return 'A label value must start and end with a letter or digit, with only ".", "-" or "_" between.'
  }
  return undefined
}

/**
 * Splits a selector into its top-level comma-separated terms, leaving commas
 * inside `in (a,b)` set expressions alone.
 */
export function splitSelector(selector: string): string[] {
  const out: string[] = []
  let depth = 0
  let cur = ''
  for (const ch of selector) {
    if (ch === '(') depth++
    else if (ch === ')') depth = Math.max(0, depth - 1)
    if (ch === ',' && depth === 0) {
      out.push(cur)
      cur = ''
      continue
    }
    cur += ch
  }
  out.push(cur)
  return out.map((p) => p.trim()).filter(Boolean)
}

/**
 * Toggles a `key=value` equality term in a comma-separated label selector,
 * which is what clicking a label chip does. Clicking a chip already in the
 * selector removes it; clicking a different value for a key that already has
 * an equality term replaces that term, because requiring two values for one
 * key can never match.
 */
export function toggleSelectorTerm(selector: string, key: string, value: string): string {
  const term = `${key}=${value}`
  const parts = splitSelector(selector)

  const had = parts.includes(term)
  const kept = parts.filter((p) => {
    if (p === term) return false
    const eq = p.match(/^([^!=<>()\s]+)\s*==?\s*/)
    return !(eq && eq[1] === key)
  })

  return (had ? kept : [...kept, term]).join(',')
}

/**
 * The merge patch that turns `before` into `after`: changed and added keys
 * carry their new value, removed keys carry null (which deletes the key under
 * RFC 7386 merge-patch semantics). Empty result means nothing changed.
 */
export function metaChanges(
  before: Record<string, string>,
  after: Record<string, string>,
): Record<string, string | null> {
  const changes: Record<string, string | null> = {}
  for (const [k, v] of Object.entries(after)) {
    if (before[k] !== v) changes[k] = v
  }
  for (const k of Object.keys(before)) {
    if (!(k in after)) changes[k] = null
  }
  return changes
}
