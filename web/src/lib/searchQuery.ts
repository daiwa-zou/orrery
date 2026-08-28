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
  /**
   * Column predicates — the comparisons a Kubernetes selector cannot express.
   * A list rather than a joined string because a pattern may contain a comma,
   * and each one travels as its own repeated `where` parameter.
   */
  where: string[]
}

/**
 * The operators that mark a term as a column predicate rather than a
 * selector. Longest first, so `>=` is never read as `>` before a value that
 * begins with `=`.
 *
 * Every one of these is a syntax error to a Kubernetes selector, which is
 * what makes the two languages safe to mix in one box: a term using any of
 * them can only be a predicate, and `=`, `!=`, `in` and `!key` keep exactly
 * the meanings they had.
 */
export const WHERE_OPS = ['>=', '<=', '=~', '!~', '>', '<'] as const
export type WhereOp = (typeof WHERE_OPS)[number]

/** A column name, as far as the client can tell without asking the server. */
const COLUMN_RE = /^[A-Za-z_][A-Za-z0-9_.-]*$/

export interface WhereTerm {
  column: string
  op: WhereOp
  value: string
}

/**
 * Reads a token as a column predicate, or returns undefined if it is not one.
 *
 * The part before the operator has to look like a column name. Without that,
 * `app=a>b` — a label term with an illegal value — would be read as a
 * predicate on a column called `app=a`, and the reader would be told about a
 * missing column instead of about the character that is actually wrong.
 */
export function parseWhereTerm(token: string): WhereTerm | undefined {
  const split = splitWhereTerm(token)
  return split && split.value !== '' ? split : undefined
}

/**
 * The same split, but accepting a term whose value has not been typed yet.
 *
 * `age>` is not a filter — there is nothing to compare against — but it is
 * plainly on its way to being one, and that matters for what happens to it in
 * the meantime: searching object names for the literal text "age>" empties
 * the list the instant a column is picked from the dropdown.
 */
export function splitWhereTerm(token: string): WhereTerm | undefined {
  let best = -1
  let op: WhereOp | undefined
  for (const candidate of WHERE_OPS) {
    const at = token.indexOf(candidate)
    if (at < 0) continue
    // `!=` belongs to label selectors; its `=` must not start an `=~`.
    if (candidate === '=~' && at > 0 && '!<>'.includes(token[at - 1])) continue
    if (best < 0 || at < best || (at === best && candidate.length > op!.length)) {
      best = at
      op = candidate
    }
  }
  if (best < 0 || !op) return undefined

  const column = token.slice(0, best)
  if (!COLUMN_RE.test(column)) return undefined
  return { column, op, value: token.slice(best + op.length) }
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
  const where: string[] = []
  const problems: SearchProblem[] = []

  for (const token of tokenizeSearch(text)) {
    // Predicates are tested first because their operators cannot appear in a
    // selector at all: anything carrying one is unambiguously a predicate,
    // and nothing below would have read it correctly.
    const predicate = parseWhereTerm(token)
    if (predicate) {
      where.push(token)
      continue
    }

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
    where,
    committable: problems.length === 0,
    problems,
  }
}

/** The inverse of parseSearchInput, used to seed the bar from the URL. */
export function composeSearchInput(query: SearchQuery): string {
  return [
    ...splitSelector(query.labelSelector),
    ...splitSelector(query.fieldSelector),
    ...(query.where ?? []),
    query.q.trim(),
  ]
    .filter(Boolean)
    .join(' ')
}

export function sameQuery(a: SearchQuery, b: SearchQuery): boolean {
  return (
    a.q === b.q &&
    a.labelSelector === b.labelSelector &&
    a.fieldSelector === b.fieldSelector &&
    sameTerms(a.where, b.where)
  )
}

function sameTerms(a: string[] = [], b: string[] = []): boolean {
  return a.length === b.length && a.every((t, i) => t === b[i])
}

/** One removable part of a committed query. */
export interface QueryTerm {
  kind: 'text' | 'label' | 'field' | 'where'
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
    ...(query.where ?? []).map((term): QueryTerm => ({ kind: 'where', term })),
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
    where:
      target.kind === 'where'
        ? (query.where ?? []).filter((t) => t !== target.term)
        : (query.where ?? []),
  }
}

/** Whether a token is a selector the server will actually apply. */
export function isFilterTerm(token: string): boolean {
  const parsed = parseSearchInput(token)
  return parsed.labelSelector !== '' || parsed.fieldSelector !== '' || parsed.where.length > 0
}

/**
 * The free-text half of what is in the box.
 *
 * The input holds two different things at once — words to search for, and a
 * filter term part-way through being written — and only the words belong in
 * `q`. Sending `app=we` as free text while it is still being typed would
 * search object names for the string "app=we" and find nothing, so a term is
 * excluded from the moment it reads as one.
 */
export function freeTextOf(input: string): string {
  return tokenizeSearch(input)
    .filter((t) => !isFilterTerm(t) && !splitWhereTerm(t))
    .join(' ')
}

/** The last whitespace-separated token, which is the one being typed. */
export function trailingToken(input: string): string {
  const m = /(\S*)$/.exec(input)
  return m ? m[1] : ''
}

/**
 * The query with one more term applied.
 *
 * Where it lands is decided by the parser rather than by the caller, so a
 * chip can never end up in the selector the server would reject it from.
 */
export function addQueryTerm(query: SearchQuery, term: string): SearchQuery {
  const parsed = parseSearchInput(term)
  const join = (existing: string, added: string) =>
    !added ? existing : existing ? `${existing},${added}` : added

  return {
    q: query.q,
    labelSelector: join(query.labelSelector, parsed.labelSelector),
    fieldSelector: join(query.fieldSelector, parsed.fieldSelector),
    where: [...(query.where ?? []), ...parsed.where],
  }
}

/** What the table is showing, which is what a predicate may talk about. */
export interface PredicateColumn {
  key: string
  type?: string
}

const ORDERING_OPS: WhereOp[] = ['>', '>=', '<', '<=']
const DURATION_RE = /^\d+(\.\d+)?(s|m|h|d|w)$/

/**
 * Why a predicate cannot be applied, or undefined if it can.
 *
 * The server refuses these too, and its refusal is the authority — this is
 * the same judgement made early enough to answer in the search bar instead
 * of replacing the page with an error. Checking here is what keeps a typo a
 * correction rather than a broken screen.
 */
export function whereProblem(
  token: string,
  columns: PredicateColumn[] | undefined,
): string | undefined {
  const parsed = parseWhereTerm(token)
  if (!parsed) return undefined
  // Nothing to check against until the first page of results has arrived.
  if (!columns || columns.length === 0) return undefined

  const col = columns.find((c) => c.key === parsed.column)
  if (!col) {
    const names = columns
      .map((c) => c.key)
      .filter((k) => !k.startsWith('_'))
      .sort()
    return `There is no ${parsed.column} column here. Try: ${names.join(', ')}.`
  }

  if (!ORDERING_OPS.includes(parsed.op)) {
    try {
      new RegExp(parsed.value)
    } catch {
      return `${parsed.value} is not a valid pattern.`
    }
    return undefined
  }

  if (col.type === 'number') {
    return Number.isFinite(Number(parsed.value)) || /^\d+(\.\d+)?[a-zA-Z]+$/.test(parsed.value)
      ? undefined
      : `${parsed.column} is a number, and ${parsed.value} is not one.`
  }
  if (col.type === 'age') {
    return DURATION_RE.test(parsed.value)
      ? undefined
      : `${parsed.value} is not a duration. Try 30s, 5m, 2h, 3d or 1w.`
  }
  return `${parsed.op} cannot order ${parsed.column}, which is text. Use =~ to match a pattern.`
}
