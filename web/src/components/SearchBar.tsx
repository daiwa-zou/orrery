import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import clsx from 'clsx'
import type { FacetsResponse } from '../api/types'
import { splitSelector } from '../lib/labels'
import { FilterInput } from './primitives'
import { useToast } from './Toast'
import {
  addQueryTerm,
  columnOperators,
  SELECTOR_OPERATORS,
  freeTextOf,
  isFilterTerm,
  equalityKey,
  queryHasTerm,
  splitWhereTerm,
  whereProblem,
  type PredicateColumn,
  parseSearchInput,
  queryTerms,
  removeQueryTerm,
  sameQuery,
  trailingToken,
  type SearchProblem,
  type SearchQuery,
} from '../lib/searchQuery'

interface Suggestion {
  /** Replacement for the token being typed. */
  token: string
  /**
   * Which of the three steps this row completes. Everything but a value is
   * half a term, and the list stays open for the next step.
   */
  kind: 'label' | 'field' | 'value' | 'column' | 'operator'
  display: string
  /** Overrides the kind caption, for a row whose meaning is worth spelling. */
  hint?: string
}

const MAX_SUGGESTIONS = 8

/** Durations worth offering once an age comparison has been started. */
const AGE_VALUES = ['5m', '1h', '6h', '1d', '7d', '30d']

/** How prominent a column is at the empty prompt: what can be *ordered*
 *  first, because comparing a magnitude is the capability nobody guesses is
 *  there, where a pattern on a name is a familiar idea. */
function columnRank(type: string | undefined): number {
  return type === 'number' || type === 'age' ? 0 : 1
}

function buildSuggestions(
  token: string,
  facets: FacetsResponse | undefined,
  columns: PredicateColumn[] | undefined,
  applied: SearchQuery,
): Suggestion[] {
  // A key already pinned to a value has nothing left to offer: the same value
  // again is a repeat, and a different one contradicts the term that is
  // already there. Either way the reader is being pointed at a dead end.
  const pinned = new Set(
    [...splitSelector(applied.labelSelector), ...splitSelector(applied.fieldSelector)]
      .map(equalityKey)
      .filter((k): k is string => k !== undefined),
  )
  // Value position for a predicate whose operator is typed but whose value is
  // not. Only age has a small, guessable vocabulary; a number or a pattern
  // does not, and offering nothing is better than offering noise.
  const started = splitWhereTerm(token)
  if (started?.value === '') {
    const col = columns?.find((c) => c.key === started.column)
    if (col?.type === 'age') {
      return AGE_VALUES.map((v) => ({ token: `${token}${v}`, kind: 'value', display: v }))
    }
    return []
  }
  if (!facets) return []
  const term = /^([^=!\s]+)(!=|==?)(.*)$/.exec(token)

  if (term) {
    // Value position: suggest the values seen for this key.
    const [, key, op, prefix] = term
    const facet =
      facets.labels.find((f) => f.key === key) ?? facets.fields.find((f) => f.key === key)
    if (!facet) return []
    return facet.values
      .filter((v) => v.toLowerCase().startsWith(prefix.toLowerCase()) && v !== prefix)
      .filter((v) => !queryHasTerm(applied, `${key}${op}${v}`))
      .slice(0, MAX_SUGGESTIONS)
      .map((v) => ({ token: `${key}${op}${v}`, kind: 'value', display: v }))
  }

  const needle = token.toLowerCase()
  const match = (key: string) => needle === '' || key.toLowerCase().includes(needle)

  // Operator position: the token names something we know of, and what is
  // missing is the comparison. Which operators exist at all depends on what
  // the key is — a label is equal to a value or it is not, where a column with
  // a magnitude can be ordered — so this step is where that difference gets
  // taught, in words, instead of being decided silently for the reader.
  const selectorKey =
    facets.labels.some((f) => f.key === token) || facets.fields.some((f) => f.key === token)
  const namedColumn = (columns ?? []).find((c) => c.key === token && !c.key.startsWith('_'))
  if (selectorKey || namedColumn) {
    const ops: Suggestion[] = []
    if (selectorKey && !pinned.has(token)) {
      ops.push(
        ...SELECTOR_OPERATORS.map(
          (o): Suggestion => ({
            token: `${token}${o.op}`,
            kind: 'operator',
            display: `${token}${o.op}`,
            hint: o.means,
          }),
        ),
      )
    }
    if (namedColumn) {
      ops.push(
        ...columnOperators(namedColumn.type).map(
          (o): Suggestion => ({
            token: `${token}${o.op}`,
            kind: 'operator',
            display: `${token}${o.op}`,
            hint: o.means,
          }),
        ),
      )
    }
    // A key that is also the start of a longer one still has to be reachable:
    // picking `app` must not hide `app.kubernetes.io/name`.
    const longer = [...facets.labels, ...facets.fields]
      .filter((f) => f.key !== token && f.key.toLowerCase().startsWith(needle))
      .map((f): Suggestion => ({ token: f.key, kind: 'label', display: f.key }))
    return [...ops, ...longer].slice(0, MAX_SUGGESTIONS)
  }

  // Key position: labels first (the common case), but fields keep a couple of
  // slots so status.phase and friends are discoverable at the empty prompt.
  // Nothing here carries an operator: choosing what to filter on and choosing
  // how to compare it are two decisions, and answering the second one on the
  // reader's behalf is what hid `>=`, `<` and `!~` from everyone.
  const fields = facets.fields
    .filter((f) => match(f.key) && !pinned.has(f.key))
    .map((f): Suggestion => ({ token: f.key, kind: 'field', display: f.key }))
  const labels = facets.labels
    .filter((f) => match(f.key) && !pinned.has(f.key))
    .map((f): Suggestion => ({ token: f.key, kind: 'label', display: f.key }))

  // Columns are how anyone finds out that restarts>3 and age<1h are things
  // they can write at all. `_labels` is a rendering detail, not a column
  // anyone can compare.
  const named = new Set([...fields, ...labels].map((s) => s.token))
  const cols = (columns ?? [])
    .filter((c) => !c.key.startsWith('_') && match(c.key) && !named.has(c.key))
    .sort((a, b) => columnRank(a.type) - columnRank(b.type))
    .map((c): Suggestion => ({ token: c.key, kind: 'column', display: c.key }))

  const room = MAX_SUGGESTIONS - Math.min(fields.length, 2) - Math.min(cols.length, 3)
  return [...labels.slice(0, Math.max(room, 1)), ...fields.slice(0, 2), ...cols.slice(0, 3)].slice(
    0,
    MAX_SUGGESTIONS,
  )
}

/**
 * The unified search input: free text, label terms and field terms in one
 * box, with autocomplete for keys and values drawn from the objects the
 * caller may actually see. Committed (debounced) values land in the URL as
 * the three server params, so deep links and the back button keep working.
 */
export function SearchBar({
  query,
  onCommit,
  facets,
  columns,
  onActivate,
  onScopeChange,
  placeholder = 'Search or filter…',
}: {
  query: SearchQuery
  onCommit: (next: SearchQuery) => void
  facets?: FacetsResponse
  /**
   * The columns of the table being listed, which is what a predicate may
   * compare. Absent until the first page arrives, and a predicate is simply
   * not checked until then.
   */
  columns?: PredicateColumn[]
  /** Called on first focus so facet loading can be lazy. */
  onActivate?: () => void
  /**
   * The search the suggestions should be drawn from: everything committed
   * except the term being typed. The caller fetches facets for it, so the
   * dropdown only ever offers keys and values that still match something.
   */
  onScopeChange?: (scope: SearchQuery) => void
  placeholder?: string
}) {
  // The box holds only what is being composed: words to search for, and at
  // most one filter term part-way through being written. A finished term
  // leaves for the applied-filters row, so nothing is ever shown in two
  // places at once.
  const [draft, setDraft] = useState(() => query.q)
  const [open, setOpen] = useState(false)
  const [highlight, setHighlight] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const problemsId = useId()
  const [problems, setProblems] = useState<SearchProblem[]>([])
  /**
   * A key chosen from the list whose operator has not been picked yet.
   *
   * It sits in the box looking like a word, and it must not be sent as one:
   * committing `app` as free text between the two clicks empties the list
   * under the reader mid-decision. It stays pending only while it is still the
   * trailing token — edit it and it becomes ordinary text again.
   */
  const [pendingKey, setPendingKey] = useState<string>()
  const toast = useToast()
  const draftRef = useRef(draft)
  useEffect(() => {
    draftRef.current = draft
  })

  // Adopt outside changes to the free text (back button, deep links) without
  // clobbering typing. Filter terms are not adopted: they live in the chips
  // now, and writing them back into the box is exactly the duplication this
  // structure removes.
  useEffect(() => {
    if (freeTextOf(draftRef.current) === query.q) return
    setDraft(query.q)
  }, [query.q]) // eslint-disable-line react-hooks/exhaustive-deps

  // Free text applies as you type, because refining a word is iterative and
  // nobody wants to press Enter to see "ngin" become "nginx". Filter terms do
  // not: they are promoted at a boundary the reader chooses, so a half-typed
  // `app=w` is never applied as though it were finished.
  useEffect(() => {
    const trailing = trailingToken(draft)
    const pending = pendingKey !== undefined && trailing === pendingKey
    const next = freeTextOf(pending ? draft.slice(0, draft.length - trailing.length) : draft)
    const t = window.setTimeout(() => {
      const bad = whereProblem(trailing, columns)
      if (bad) {
        setProblems([{ term: trailing, reason: bad }])
        return
      }
      setProblems(parseSearchInput(trailing).problems)
      if (next !== query.q) onCommit({ ...query, q: next })
    }, 300)
    return () => window.clearTimeout(t)
  }, [draft, query, onCommit, columns, pendingKey])

  // The box's cross clears the box. It used to clear everything, which was
  // right when the box held everything; now that the applied filters live in
  // their own row with their own crosses, wiping them from here would be
  // clearing something the reader cannot even see from this control.
  const clear = useCallback(() => {
    setProblems([])
    setDraft('')
    onCommit({ ...query, q: '' })
  }, [onCommit, query])

  const token = trailingToken(draft)

  /**
   * Move the finished term out of the box and into the chips. Returns whether
   * anything moved, so the keystroke that triggered it can be swallowed.
   */
  const promote = useCallback(() => {
    const term = trailingToken(draftRef.current)
    if (!isFilterTerm(term)) return false
    // A predicate naming a column that is not there, or comparing one that
    // cannot be ordered, is refused here rather than sent — the server would
    // answer 400 and replace the whole page with an error, which is a harsh
    // response to a typo.
    const bad = whereProblem(term, columns)
    if (bad) {
      setProblems([{ term, reason: bad }])
      return false
    }
    const rest = draftRef.current.slice(0, draftRef.current.length - term.length)

    // Already applied: the term is still consumed, because the reader asked
    // for a state that is already true and leaving their text in the box
    // would read as a failure. But it is said out loud — otherwise the typing
    // simply vanishes with nothing on screen changing, which is the one case
    // where doing the right thing looks like doing nothing.
    if (queryHasTerm(query, term)) {
      setDraft(rest)
      setProblems([])
      toast.push({ tone: 'ok', title: `${term} is already applied` })
      return true
    }

    setDraft(rest)
    setProblems([])
    onCommit(addQueryTerm({ ...query, q: freeTextOf(rest) }, term))
    return true
  }, [onCommit, query, columns, toast])

  // The suggestions are drawn from what is already filtering. That is exactly
  // the committed query now: the term being completed has not been promoted
  // yet, so it cannot narrow the vocabulary it is asking for.
  const whereKey = (query.where ?? []).join('\u0000')
  const scope = useMemo<SearchQuery>(
    () => ({
      q: query.q,
      labelSelector: query.labelSelector,
      fieldSelector: query.fieldSelector,
      where: query.where ?? [],
    }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [query.q, query.labelSelector, query.fieldSelector, whereKey],
  )
  const reportedScope = useRef(scope)
  useEffect(() => {
    if (sameQuery(reportedScope.current, scope)) return
    reportedScope.current = scope
    onScopeChange?.(scope)
  }, [scope, onScopeChange])

  const suggestions = useMemo(
    () => (open ? buildSuggestions(token, facets, columns, query) : []),
    [open, token, facets, columns, query],
  )

  useEffect(() => {
    setHighlight(0)
  }, [token, open])

  const accept = useCallback(
    (s: Suggestion) => {
      const head = draftRef.current.slice(
        0,
        draftRef.current.length - trailingToken(draftRef.current).length,
      )
      // Only a value finishes a term. A key still needs its operator and an
      // operator still needs its value, so either stays in the box with the
      // list open on the next step.
      if (s.kind !== 'value') {
        setDraft(head + s.token)
        setPendingKey(s.kind === 'operator' ? undefined : s.token)
        setOpen(true)
        inputRef.current?.focus()
        return
      }
      setPendingKey(undefined)
      setDraft(head)
      setProblems([])
      onCommit(addQueryTerm({ ...query, q: freeTextOf(head) }, s.token))
      setOpen(true)
      inputRef.current?.focus()
    },
    [onCommit, query],
  )

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      setOpen(false)
      return
    }

    // Space and Enter are the boundaries that say "this term is finished".
    // Space only when there is a term to finish, so it stays an ordinary
    // space while free text is being typed.
    if ((e.key === ' ' || e.key === 'Enter') && isFilterTerm(token)) {
      if (!(open && suggestions.length > 0 && e.key === 'Enter')) {
        e.preventDefault()
        promote()
        return
      }
    }

    // Backspace on an empty box takes the last chip back for editing, rather
    // than doing nothing — the usual way out of a filter added by mistake.
    if (e.key === 'Backspace' && draft === '') {
      const chips = queryTerms(query).filter((t) => t.kind !== 'text')
      const last = chips[chips.length - 1]
      if (last) {
        e.preventDefault()
        setDraft(last.term)
        onCommit(removeQueryTerm(query, last))
        return
      }
    }

    if (!open || suggestions.length === 0) {
      if (e.key === 'ArrowDown') setOpen(true)
      return
    }
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        setHighlight((h) => (h + 1) % suggestions.length)
        break
      case 'ArrowUp':
        e.preventDefault()
        setHighlight((h) => (h - 1 + suggestions.length) % suggestions.length)
        break
      case 'Enter':
      case 'Tab':
        e.preventDefault()
        accept(suggestions[highlight])
        break
    }
  }

  const invalid = problems.length > 0
  // Suggestions win the space under the bar: someone completing a term is
  // already fixing it, and does not need to be told twice.
  const showProblems = invalid && !(open && suggestions.length > 0)

  return (
    <FilterInput
      inputRef={inputRef}
      value={draft}
      onValueChange={(next) => {
        setDraft(next)
        setOpen(true)
      }}
      onClear={clear}
      invalid={invalid}
      className="w-full max-w-md min-w-56"
      onFocus={() => {
        onActivate?.()
        setOpen(true)
      }}
      onBlur={() => {
        // Leaving the box is also a boundary: a finished term should not be
        // silently abandoned because the reader clicked away instead of
        // pressing space.
        promote()
        window.setTimeout(() => setOpen(false), 150)
      }}
      onKeyDown={onKeyDown}
      placeholder={placeholder}
      aria-label="Search and filter"
      aria-expanded={open && suggestions.length > 0}
      aria-autocomplete="list"
      role="combobox"
      title={'Free text matches name, namespace and labels.\nFilter terms: app=web, tier!=cache, !canary, app in (web,api), status.phase=Running'}
      aria-describedby={showProblems ? problemsId : undefined}
    >
      {showProblems && (
        <div
          id={problemsId}
          role="status"
          className="absolute top-full left-0 z-20 mt-1 w-full min-w-64 bg-raised px-2.5 py-2 text-[11px] shadow-[0_16px_40px_rgba(0,0,0,.6)] ring-1 ring-danger/45"
        >
          {problems.map((p) => (
            <p key={p.term} className="text-ink-muted">
              <span className="font-mono text-danger">{p.term}</span> — {p.reason}
            </p>
          ))}
          {/* The load-bearing sentence. Holding the last good query is right,
              but a reader who is not told is looking at an unfiltered list
              believing they filtered it. */}
          <p className="mt-1.5 border-t border-border pt-1.5 text-ink-faint">
            Still showing the results for the last valid search.
          </p>
        </div>
      )}
      {open && suggestions.length > 0 && (
        <ul
          role="listbox"
          className="absolute top-full left-0 z-20 mt-1 max-h-72 w-full min-w-64 overflow-auto bg-raised py-1 text-sm shadow-[0_16px_40px_rgba(0,0,0,.6)] ring-1 ring-border-strong"
        >
          {suggestions.map((s, i) => (
            <li key={s.token} role="option" aria-selected={i === highlight}>
              <button
                type="button"
                className={clsx(
                  'flex w-full items-center justify-between gap-3 px-2.5 py-1.5 text-left',
                  i === highlight ? 'bg-surface-2 text-ink' : 'text-ink-muted',
                )}
                // Mousedown, so the click lands before the input's blur closes
                // the list.
                onMouseDown={(e) => {
                  e.preventDefault()
                  accept(s)
                }}
                onMouseEnter={() => setHighlight(i)}
              >
                <span className="truncate font-mono text-xs">{s.display}</span>
                {/* The kind is a category and reads as one, in caps. A hint is
                    a sentence fragment — "older than" — and must not. */}
                <span
                  className={clsx(
                    'shrink-0 text-[10px] tracking-wide text-ink-faint',
                    s.hint === undefined && 'uppercase',
                  )}
                >
                  {s.hint ?? s.kind}
                </span>
              </button>
            </li>
          ))}
          {facets?.truncated && (
            <li className="border-t border-border px-2.5 py-1 text-[11px] text-ink-faint">
              Some keys or values are omitted; type to narrow.
            </li>
          )}
        </ul>
      )}
    </FilterInput>
  )
}
