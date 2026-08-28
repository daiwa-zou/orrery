import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import clsx from 'clsx'
import type { Column, Row } from '../api/types'
import {
  addWhereTerm,
  columnValues,
  eventTermProblem,
  freeTextOf,
  isWhereTerm,
  removeWhereTerm,
  trailingToken,
  valueTerm,
  type EventQuery,
} from '../lib/eventQuery'
import { splitWhereTerm } from '../lib/searchQuery'
import { FilterInput } from './primitives'

interface Suggestion {
  /** Replacement for the token being typed. */
  token: string
  kind: 'column' | 'value'
  display: string
  /** What the row is offering, in words: the capability a column suggestion
   *  unlocks, or where a suggested value came from. */
  hint: string
}

const MAX_SUGGESTIONS = 8

/** Durations worth offering for "how recently", which is the only direction
 *  anyone asks an event feed about time. */
const RECENCY_VALUES = ['5m', '15m', '1h', '6h', '1d']

/** Thresholds worth offering for a repeat count: once, a few, a lot. */
const COUNT_VALUES = ['1', '3', '10']

/**
 * The operator a column can answer, and the direction it is asked in.
 *
 * `lastSeen<15m` is "in the last quarter hour" and `count>3` is "more than a
 * one-off". The opposite of each is a question about an event feed that nobody
 * has ever needed to ask, so the offered form is the useful one.
 */
function columnOp(type: string | undefined): string {
  if (type === 'age') return '<'
  if (type === 'number') return '>'
  return '=~'
}

function opHint(key: string, type: string | undefined): string {
  if (type === 'age') return 'in the last…'
  if (type === 'number') return 'more than…'
  return `${key} matching…`
}

/**
 * Whether a column's values are worth listing back to the reader.
 *
 * Types, reasons and object names repeat — they are a vocabulary, and the
 * cluster's own is the only accurate one. A message does not repeat: it is
 * prose, every row's is different, and a dropdown of eight whole sentences
 * offers nothing that typing a word from one would not.
 */
function enumerable(column: Column): boolean {
  return column.key !== 'message' && (column.type === 'status' || column.type === 'text')
}

function buildSuggestions(
  token: string,
  columns: Column[],
  rows: Row[],
  applied: EventQuery,
): Suggestion[] {
  const started = splitWhereTerm(token)
  if (started) {
    const col = columns.find((c) => c.key === started.column)
    if (!col) return []
    const prefix = started.value.toLowerCase()
    const offer = (values: string[], hint: string, term: (v: string) => string) =>
      values
        .filter((v) => v.toLowerCase().startsWith(prefix))
        .map((v): Suggestion => ({ token: term(v), kind: 'value', display: v, hint }))
        .filter((s) => !applied.where.includes(s.token))
        .slice(0, MAX_SUGGESTIONS)

    if (col.type === 'age') {
      return offer(RECENCY_VALUES, 'ago', (v) => `${started.column}${started.op}${v}`)
    }
    if (col.type === 'number') {
      return offer(COUNT_VALUES, 'times', (v) => `${started.column}${started.op}${v}`)
    }
    // A pattern is being written by hand from here on: the values on offer are
    // the ones this feed holds, anchored, because they were picked rather than
    // typed as a fragment.
    if (started.op !== '=~' && started.op !== '!~') return []
    if (!enumerable(col)) return []
    // "in this feed" is the honest label: these are the values on screen, not
    // every value the cluster has ever recorded.
    return offer(columnValues(rows, started.column), 'in this feed', (v) =>
      valueTerm(started.column, v),
    )
  }

  const needle = token.toLowerCase()
  return columns
    .filter((c) => !c.key.startsWith('_') && c.key.toLowerCase().includes(needle))
    .map((c): Suggestion => {
      const op = columnOp(c.type)
      return {
        token: `${c.key}${op}`,
        kind: 'column',
        display: `${c.key}${op}`,
        hint: opHint(c.key, c.type),
      }
    })
    .slice(0, MAX_SUGGESTIONS)
}

/**
 * The event feed's search box.
 *
 * It is not the resource lists' SearchBar, and the difference is deliberate:
 * that bar's vocabulary is label and field selectors, which this endpoint does
 * not take. Offering them here would let someone type `app=web`, watch it turn
 * into a chip, and read an unfiltered feed believing it was filtered. What this
 * one offers instead is the vocabulary the feed itself is written in — the
 * reasons and objects actually present — plus the column predicates that ask
 * the questions text cannot: how recently, how many times.
 */
export function EventSearchBar({
  query,
  onCommit,
  columns,
  rows,
  className,
}: {
  query: EventQuery
  onCommit: (next: EventQuery) => void
  /** The feed's columns, which is what a predicate may talk about. Empty until
   *  the first response arrives, and a predicate is not checked until then. */
  columns: Column[]
  /** The rows on screen, which is where the suggested values come from. */
  rows: Row[]
  className?: string
}) {
  const [draft, setDraft] = useState(() => query.q)
  const [open, setOpen] = useState(false)
  const [highlight, setHighlight] = useState(0)
  const [problem, setProblem] = useState<string>()
  const inputRef = useRef<HTMLInputElement>(null)
  const problemId = useId()
  const draftRef = useRef(draft)
  useEffect(() => {
    draftRef.current = draft
  })

  // Adopt outside changes to the free text — the back button, a deep link, a
  // cleared filter — without clobbering what is being typed.
  useEffect(() => {
    if (freeTextOf(draftRef.current) === query.q) return
    setDraft(query.q)
  }, [query.q]) // eslint-disable-line react-hooks/exhaustive-deps

  // Words apply as they are typed, because refining a search is iterative and
  // nobody wants to press Enter to see "backo" become "backoff". A predicate
  // does not: it is promoted at a boundary the reader chooses, so a half-typed
  // `count>` is never applied as though it were finished.
  useEffect(() => {
    const next = freeTextOf(draft)
    const trailing = trailingToken(draft)
    const t = window.setTimeout(() => {
      const bad = eventTermProblem(trailing, columns)
      setProblem(bad)
      // A term that cannot be applied is not sent as words to search for
      // either. `reason=BackOff` searched as literal text empties the feed,
      // and an empty feed under a panel promising "the last valid search" is
      // the page contradicting itself about what the reader is looking at.
      if (bad) return
      if (next !== query.q) onCommit({ ...query, q: next })
    }, 300)
    return () => window.clearTimeout(t)
  }, [draft, query, onCommit, columns])

  const clear = useCallback(() => {
    setProblem(undefined)
    setDraft('')
    onCommit({ ...query, q: '' })
  }, [onCommit, query])

  const token = trailingToken(draft)

  /** Move a finished predicate out of the box and into the chips. */
  const promote = useCallback(() => {
    const term = trailingToken(draftRef.current)
    if (!isWhereTerm(term)) return false
    const bad = eventTermProblem(term, columns)
    if (bad) {
      setProblem(bad)
      return false
    }
    const rest = draftRef.current.slice(0, draftRef.current.length - term.length)
    setDraft(rest)
    setProblem(undefined)
    onCommit(addWhereTerm({ ...query, q: freeTextOf(rest) }, term))
    return true
  }, [columns, onCommit, query])

  const suggestions = useMemo(
    () => (open ? buildSuggestions(token, columns, rows, query) : []),
    [open, token, columns, rows, query],
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
      // A column on its own still needs its value, so it stays in the box with
      // the list open. A finished term has nothing left to type.
      if (s.kind === 'column') {
        setDraft(head + s.token)
        setOpen(true)
        inputRef.current?.focus()
        return
      }
      setDraft(head)
      setProblem(undefined)
      onCommit(addWhereTerm({ ...query, q: freeTextOf(head) }, s.token))
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
    // Space only when there is one to finish, so it stays an ordinary space
    // between the words of a search.
    if ((e.key === ' ' || e.key === 'Enter') && isWhereTerm(token)) {
      if (!(open && suggestions.length > 0 && e.key === 'Enter')) {
        e.preventDefault()
        promote()
        return
      }
    }

    // Backspace on an empty box takes the last chip back for editing, which is
    // the usual way out of a filter added by mistake.
    if (e.key === 'Backspace' && draft === '') {
      const last = query.where[query.where.length - 1]
      if (last) {
        e.preventDefault()
        setDraft(last)
        onCommit(removeWhereTerm(query, last))
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

  // Someone completing a term is already fixing it, so the list wins the space
  // under the bar and the complaint waits.
  const showProblem = !!problem && !(open && suggestions.length > 0)

  return (
    <FilterInput
      inputRef={inputRef}
      value={draft}
      onValueChange={(next) => {
        setDraft(next)
        setOpen(true)
      }}
      onClear={clear}
      invalid={!!problem}
      className={clsx('w-full max-w-md min-w-56', className)}
      onFocus={() => setOpen(true)}
      onBlur={() => {
        // Leaving the box is a boundary too: a finished term should not be
        // abandoned because the reader clicked away instead of pressing space.
        promote()
        window.setTimeout(() => setOpen(false), 150)
      }}
      onKeyDown={onKeyDown}
      placeholder="Search events…"
      aria-label="Search events"
      aria-expanded={open && suggestions.length > 0}
      aria-autocomplete="list"
      role="combobox"
      title={
        'Words are ANDed and may match the object, reason, message, namespace or type.\n' +
        '"a phrase" is one word, -word excludes.\n' +
        'Filters: reason=~^Failed, object=~^Pod/web, count>3, lastSeen<15m'
      }
      aria-describedby={showProblem ? problemId : undefined}
    >
      {showProblem && (
        <div
          id={problemId}
          role="status"
          className="absolute top-full left-0 z-20 mt-1 w-full min-w-64 bg-raised px-2.5 py-2 text-[11px] shadow-[0_16px_40px_rgba(0,0,0,.6)] ring-1 ring-danger/45"
        >
          <p className="text-ink-muted">{problem}</p>
          {/* The load-bearing sentence: a reader who is not told is looking at
              a feed they believe they narrowed. */}
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
                <span className="shrink-0 text-[10px] tracking-wide text-ink-faint">
                  {s.hint}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </FilterInput>
  )
}
