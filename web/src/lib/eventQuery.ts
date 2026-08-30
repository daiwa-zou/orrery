import { splitWhereTerm, whereProblem, type PredicateColumn } from './searchQuery'

/**
 * The event feed's search box, which is a different question from the resource
 * lists' one.
 *
 * A list is searched by *identity* — this label, that field, that name — and
 * its bar is built around Kubernetes selectors. An event is searched by what it
 * *says*: words from a message, a reason, the object it is about. So this box
 * holds free text and the column predicates (`count>3`, `lastSeen<15m`,
 * `reason=~^Failed`) and nothing else; a label selector would be accepted by
 * the server's list endpoints and silently ignored by this one, which is the
 * worst outcome available — a filtered-looking page that is not filtered.
 */
export interface EventQuery {
  /** Free text: words, phrases and exclusions, applied server-side. */
  q: string
  /** Column predicates, one per repeated `where` parameter. */
  where: string[]
}

/**
 * Splits the box into terms, keeping a quoted phrase together.
 *
 * This mirrors the server's tokenizer, because the two have to agree about
 * what a term is: a phrase the client split in half would be promoted to a
 * predicate or counted as two words, and the results would stop matching what
 * the box appears to say. An unterminated quote runs to the end — it is being
 * typed, and the alternative is the list emptying on the keystroke that opens
 * it.
 */
export function tokenizeEvents(text: string): string[] {
  const out: string[] = []
  let cur = ''
  let quoted = false
  for (const ch of text) {
    if (ch === '"') {
      quoted = !quoted
      continue
    }
    if (!quoted && /\s/.test(ch)) {
      if (cur !== '') out.push(cur)
      cur = ''
      continue
    }
    cur += ch
  }
  if (cur !== '') out.push(cur)
  return out
}

/**
 * The free-text half of what is in the box.
 *
 * A term reads as a predicate from the moment it has an operator, so `count>`
 * on its way to `count>3` is never sent as a word to search messages for.
 * Quotes are preserved around a phrase, since that is what tells the server it
 * is one thing.
 */
export function freeTextOf(text: string): string {
  return tokenizeEvents(text)
    .filter((t) => !splitWhereTerm(t))
    .map((t) => (/\s/.test(t) ? `"${t}"` : t))
    .join(' ')
}

/** Whether this token is a column predicate rather than words to search for. */
export function isWhereTerm(token: string): boolean {
  return !!splitWhereTerm(token)
}

/** The last whitespace-separated token, which is the one being typed. */
export function trailingToken(text: string): string {
  const m = /(\S*)$/.exec(text)
  return m ? m[1] : ''
}

/**
 * Why a term cannot be applied, or undefined if it can.
 *
 * Two things are refused. A predicate the server would reject — an unknown
 * column, an ordering on text — is caught here so a typo is a correction in
 * the bar rather than an error page. And `reason=BackOff`, which is what
 * anyone who has used the resource search will type first: `=` is a label
 * selector's operator, events have no label selector, and searching messages
 * for the literal string "reason=BackOff" finds nothing at all. Saying so is
 * the only version of this that teaches the reader the operator they want.
 */
export function eventTermProblem(
  token: string,
  columns: PredicateColumn[] | undefined,
): string | undefined {
  const bad = whereProblem(token, columns)
  if (bad) return bad
  if (splitWhereTerm(token)) return undefined

  const equality = /^([A-Za-z_][A-Za-z0-9_.-]*)=(.+)$/.exec(token)
  if (!equality || !columns?.length) return undefined
  const [, key, value] = equality
  if (!columns.some((c) => c.key === key)) return undefined
  return `${key}= is not a filter here. Use ${key}=~${value} to match a pattern.`
}

/** Escapes a value picked from the feed so it reads as itself in a pattern. */
export function quoteValue(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/**
 * The pattern that matches exactly one observed value. Anchored, because it
 * was chosen from a list of what is actually there rather than typed as a
 * fragment: picking `Pulled` should not also bring in `PulledImageTwice`.
 */
export function anchoredValue(value: string): string {
  return `^${quoteValue(value)}$`
}

/** The predicate that selects exactly one observed value. */
export function valueTerm(column: string, value: string): string {
  return `${column}=~${anchoredValue(value)}`
}

/** The query with one more predicate, dropping an exact repeat. */
export function addWhereTerm(query: EventQuery, term: string): EventQuery {
  if (query.where.includes(term)) return query
  return { ...query, where: [...query.where, term] }
}

/** The query without one predicate, removed by value rather than by index. */
export function removeWhereTerm(query: EventQuery, term: string): EventQuery {
  return { ...query, where: query.where.filter((t) => t !== term) }
}

/** One row of the feed, as far as this module needs to know. */
type EventRow = Record<string, unknown>

/**
 * What a column actually holds in the rows on screen, most common first.
 *
 * The suggestions are drawn from the feed rather than from a fixed list
 * because event reasons are open-ended — every controller and every operator
 * invents its own — so the only honest vocabulary is the one this cluster is
 * using right now.
 */
export function columnValues(rows: EventRow[], key: string, limit = 12): string[] {
  const counts = new Map<string, number>()
  for (const row of rows) {
    const value = row[key]
    if (typeof value !== 'string' || value === '') continue
    counts.set(value, (counts.get(value) ?? 0) + 1)
  }
  return [...counts.entries()]
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .slice(0, limit)
    .map(([value]) => value)
}

export interface ReasonTally {
  reason: string
  count: number
  /** How many of them were Warnings, which is what decides the chip's tone. */
  warnings: number
}

export interface EventSummary {
  warnings: number
  normal: number
  /** The reasons present, most frequent first. */
  reasons: ReasonTally[]
}

/**
 * What the feed on screen is made of.
 *
 * An event feed is repetitive by nature — one crash-looping pod writes the
 * same BackOff a hundred times — so the shape of the page is a poor guide to
 * what is happening in the cluster. Counting the reasons answers "what is
 * going on here" in one line, and gives the reader something to click that
 * they would otherwise have to know the vocabulary to type.
 *
 * It counts rows, not events: a row already carries its own `count` of
 * repeats, and mixing the two would produce a number that is neither.
 */
export function summarizeEvents(rows: EventRow[], limit = 8): EventSummary {
  let warnings = 0
  let normal = 0
  const tallies = new Map<string, ReasonTally>()

  for (const row of rows) {
    const warning = row.type === 'Warning'
    if (warning) warnings++
    else if (row.type === 'Normal') normal++

    const reason = typeof row.reason === 'string' ? row.reason : ''
    if (reason === '') continue
    const tally = tallies.get(reason) ?? { reason, count: 0, warnings: 0 }
    tally.count++
    if (warning) tally.warnings++
    tallies.set(reason, tally)
  }

  const reasons = [...tallies.values()].sort(
    // Warnings first among equals: a reason that is failing is what the reader
    // came for, and it is routinely rarer than the chatter around it.
    (a, b) => b.count - a.count || b.warnings - a.warnings || a.reason.localeCompare(b.reason),
  )
  return { warnings, normal, reasons: reasons.slice(0, limit) }
}
