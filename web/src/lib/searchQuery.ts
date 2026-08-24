import { splitSelector, validateLabelValue, validateMetaKey } from './labels'

/**
 * The unified search bar's model. One text input holds free text, label
 * selector terms and field selector terms together; this module translates
 * between that text and the three server query params.
 *
 * The grammar is whitespace-separated terms:
 *   - `key=value`, `key!=value`, `!key`, `key in (a,b)` — a label term when
 *     the key has no dot, since label keys with dots also carry a `/` prefix
 *     in practice and the supported field keys are a fixed dotted set.
 *   - `status.phase=Running` and friends — a field term when the key is one
 *     the server projects (SUPPORTED_FIELD_KEYS).
 *   - anything else — free text, matched against name/namespace/labels.
 */
export interface SearchQuery {
  q: string
  labelSelector: string
  fieldSelector: string
}

/** Mirrors the server's supportedFieldKeys, minus the dotless `type` — a bare
 *  `type=x` reads as (and is treated as) a label term. */
export const SUPPORTED_FIELD_KEYS = [
  'metadata.name',
  'metadata.namespace',
  'status.phase',
  'spec.nodeName',
  'involvedObject.name',
  'involvedObject.kind',
  'involvedObject.namespace',
  'involvedObject.uid',
]

const SET_EXPR_RE = /^(\S+)\s+(in|notin)\s+\(.*\)$/i
const TERM_RE = /^([^=!\s]+)(!=|==?)(.*)$/

/** Splits input into terms, keeping `key in (a, b)` together. */
export function tokenizeSearch(text: string): string[] {
  const rough = text.trim().split(/\s+/).filter(Boolean)
  const out: string[] = []
  for (let i = 0; i < rough.length; i++) {
    const op = rough[i + 1]?.toLowerCase()
    if ((op === 'in' || op === 'notin') && rough[i + 2]?.startsWith('(')) {
      let j = i + 2
      let joined = `${rough[i]} ${rough[i + 1]} ${rough[j]}`
      while (!rough[j].includes(')') && j + 1 < rough.length) {
        j++
        joined += rough[j]
      }
      out.push(joined)
      i = j
      continue
    }
    out.push(rough[i])
  }
  return out
}

export interface ParsedSearch extends SearchQuery {
  /**
   * False while a term is recognizably a selector mid-edit but not yet valid
   * (e.g. `app=We!` — bad label value). The bar holds the previous committed
   * query instead of sending a request that can only 400.
   */
  committable: boolean
}

export function parseSearchInput(text: string): ParsedSearch {
  const labels: string[] = []
  const fields: string[] = []
  const words: string[] = []
  let committable = true

  for (const token of tokenizeSearch(text)) {
    const set = SET_EXPR_RE.exec(token)
    if (set) {
      if (validateMetaKey(set[1]) === undefined) labels.push(token)
      else committable = false
      continue
    }
    if (token.startsWith('!') && token.length > 1 && !token.includes('=')) {
      if (validateMetaKey(token.slice(1)) === undefined) labels.push(token)
      else committable = false
      continue
    }
    const term = TERM_RE.exec(token)
    if (term) {
      const [, key, op, value] = term
      if (key.includes('.') || key.includes('/')) {
        // Dotted keys are field terms; a prefixed label key (a.b/c=x) also
        // lands here and is routed by the slash.
        if (!key.includes('/') && SUPPORTED_FIELD_KEYS.includes(key)) {
          fields.push(`${key}${op === '==' ? '=' : op}${value}`)
        } else if (key.includes('/') && validateMetaKey(key) === undefined) {
          if (validateLabelValue(value) === undefined) labels.push(token)
          else committable = false
        } else {
          words.push(token)
        }
        continue
      }
      if (validateMetaKey(key) === undefined) {
        if (validateLabelValue(value) === undefined) labels.push(token)
        else committable = false
        continue
      }
    }
    words.push(token)
  }

  return {
    q: words.join(' '),
    labelSelector: labels.join(','),
    fieldSelector: fields.join(','),
    committable,
  }
}

/** The inverse of parseSearchInput, used to seed the bar from the URL. */
export function composeSearchInput(query: SearchQuery): string {
  return [
    ...splitSelector(query.labelSelector),
    ...splitSelector(query.fieldSelector),
    query.q.trim(),
  ]
    .filter(Boolean)
    .join(' ')
}

export function sameQuery(a: SearchQuery, b: SearchQuery): boolean {
  return (
    a.q === b.q && a.labelSelector === b.labelSelector && a.fieldSelector === b.fieldSelector
  )
}
