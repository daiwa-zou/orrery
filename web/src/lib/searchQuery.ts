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
const SUPPORTED_FIELD_KEYS = [
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

/** A term the parser recognised as a selector but cannot send. */
export interface SearchProblem {
  /** The term exactly as typed, so a message can point at it. */
  term: string
  /** Why it was rejected, in the words of the validator that rejected it. */
  reason: string
}

export interface ParsedSearch extends SearchQuery {
  /**
   * False while a term is recognizably a selector mid-edit but not yet valid
   * (e.g. `app=We!` — bad label value). The bar holds the previous committed
   * query instead of sending a request that can only 400.
   */
  committable: boolean
  /**
   * Which terms were rejected and why. Holding the previous query is the
   * right behaviour, but doing it silently leaves the reader looking at an
   * unfiltered list believing it is filtered — so the bar needs something to
   * show them.
   */
  problems: SearchProblem[]
}

export function parseSearchInput(text: string): ParsedSearch {
  const labels: string[] = []
  const fields: string[] = []
  const words: string[] = []
  const problems: SearchProblem[] = []

  for (const token of tokenizeSearch(text)) {
    const set = SET_EXPR_RE.exec(token)
    if (set) {
      const bad = validateMetaKey(set[1])
      if (bad === undefined) labels.push(token)
      else problems.push({ term: token, reason: bad })
      continue
    }
    if (token.startsWith('!') && token.length > 1 && !token.includes('=')) {
      const bad = validateMetaKey(token.slice(1))
      if (bad === undefined) labels.push(token)
      else problems.push({ term: token, reason: bad })
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
          const bad = validateLabelValue(value)
          if (bad === undefined) labels.push(token)
          else problems.push({ term: token, reason: bad })
        } else {
          words.push(token)
        }
        continue
      }
      if (validateMetaKey(key) === undefined) {
        const bad = validateLabelValue(value)
        if (bad === undefined) labels.push(token)
        else problems.push({ term: token, reason: bad })
        continue
      }
    }
    words.push(token)
  }

  return {
    q: words.join(' '),
    labelSelector: labels.join(','),
    fieldSelector: fields.join(','),
    committable: problems.length === 0,
    problems,
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

/** One removable part of a committed query. */
export interface QueryTerm {
  kind: 'text' | 'label' | 'field'
  /** As it reads in the search bar, which is how it should read on a chip. */
  term: string
}

/**
 * The committed query broken into the parts a reader can drop one at a time.
 *
 * The bar itself is capped at max-w-md, so `app=web tier!=cache
 * status.phase=Running` is committed and then largely invisible — scrolled
 * out of a box the reader cannot see the end of. Listing the terms outside
 * the field is what makes an active filter legible, and giving each one a
 * cross is what makes it undoable without retyping the rest.
 */
export function queryTerms(query: SearchQuery): QueryTerm[] {
  return [
    ...splitSelector(query.labelSelector).map((term): QueryTerm => ({ kind: 'label', term })),
    ...splitSelector(query.fieldSelector).map((term): QueryTerm => ({ kind: 'field', term })),
    ...tokenizeSearch(query.q).map((term): QueryTerm => ({ kind: 'text', term })),
  ]
}

/**
 * The query with one term dropped. Removing by value rather than by index,
 * so a list that re-rendered between the click and the handler cannot drop
 * the wrong term; a duplicate term would be removed twice, which is the same
 * answer either way.
 */
export function removeQueryTerm(query: SearchQuery, target: QueryTerm): SearchQuery {
  const without = (selector: string) =>
    splitSelector(selector)
      .filter((t) => t !== target.term)
      .join(',')

  return {
    q:
      target.kind === 'text'
        ? tokenizeSearch(query.q)
            .filter((t) => t !== target.term)
            .join(' ')
        : query.q,
    labelSelector: target.kind === 'label' ? without(query.labelSelector) : query.labelSelector,
    fieldSelector: target.kind === 'field' ? without(query.fieldSelector) : query.fieldSelector,
  }
}
