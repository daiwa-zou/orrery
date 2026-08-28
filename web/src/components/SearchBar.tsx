import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import clsx from 'clsx'
import type { FacetsResponse } from '../api/types'
import { FilterInput } from './primitives'
import {
  addQueryTerm,
  freeTextOf,
  isFilterTerm,
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
  /** What is inserted ends with "=" → keep suggesting values for it. */
  kind: 'label' | 'field' | 'value'
  display: string
}

const MAX_SUGGESTIONS = 8

function buildSuggestions(token: string, facets: FacetsResponse | undefined): Suggestion[] {
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
      .slice(0, MAX_SUGGESTIONS)
      .map((v) => ({ token: `${key}${op}${v}`, kind: 'value', display: v }))
  }

  // Key position: labels first (the common case), but fields keep a couple of
  // slots so status.phase and friends are discoverable at the empty prompt.
  const needle = token.toLowerCase()
  const match = (key: string) => needle === '' || key.toLowerCase().includes(needle)
  const fields = facets.fields
    .filter((f) => match(f.key))
    .map((f): Suggestion => ({ token: `${f.key}=`, kind: 'field', display: f.key }))
  const labels = facets.labels
    .filter((f) => match(f.key))
    .map((f): Suggestion => ({ token: `${f.key}=`, kind: 'label', display: f.key }))
    .slice(0, MAX_SUGGESTIONS - Math.min(fields.length, 2))
  return [...labels, ...fields].slice(0, MAX_SUGGESTIONS)
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
  onActivate,
  onScopeChange,
  placeholder = 'Search or filter…',
}: {
  query: SearchQuery
  onCommit: (next: SearchQuery) => void
  facets?: FacetsResponse
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
    const next = freeTextOf(draft)
    const trailing = trailingToken(draft)
    const t = window.setTimeout(() => {
      setProblems(parseSearchInput(trailing).problems)
      if (next !== query.q) onCommit({ ...query, q: next })
    }, 300)
    return () => window.clearTimeout(t)
  }, [draft, query, onCommit])

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
    const rest = draftRef.current.slice(0, draftRef.current.length - term.length)
    setDraft(rest)
    setProblems([])
    onCommit(addQueryTerm({ ...query, q: freeTextOf(rest) }, term))
    return true
  }, [onCommit, query])

  // The suggestions are drawn from what is already filtering. That is exactly
  // the committed query now: the term being completed has not been promoted
  // yet, so it cannot narrow the vocabulary it is asking for.
  const scope = useMemo<SearchQuery>(
    () => ({ q: query.q, labelSelector: query.labelSelector, fieldSelector: query.fieldSelector }),
    [query.q, query.labelSelector, query.fieldSelector],
  )
  const reportedScope = useRef(scope)
  useEffect(() => {
    if (sameQuery(reportedScope.current, scope)) return
    reportedScope.current = scope
    onScopeChange?.(scope)
  }, [scope, onScopeChange])

  const suggestions = useMemo(
    () => (open ? buildSuggestions(token, facets) : []),
    [open, token, facets],
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
      // A key still needs its value, so it stays in the box with the dropdown
      // open on the values. A finished term has nothing left to type and goes
      // straight to the chips.
      if (s.token.endsWith('=')) {
        setDraft(head + s.token)
        setOpen(true)
        inputRef.current?.focus()
        return
      }
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
                <span className="shrink-0 text-[10px] tracking-wide text-ink-faint uppercase">
                  {s.kind}
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
