import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import clsx from 'clsx'
import type { FacetsResponse } from '../api/types'
import {
  composeSearchInput,
  parseSearchInput,
  sameQuery,
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

/** The token being typed: everything after the last whitespace. */
function activeToken(text: string): { head: string; token: string } {
  const m = /^(.*?)(\S*)$/s.exec(text)!
  return { head: m[1], token: m[2] }
}

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
  placeholder = 'Search or filter…',
}: {
  query: SearchQuery
  onCommit: (next: SearchQuery) => void
  facets?: FacetsResponse
  /** Called on first focus so facet loading can be lazy. */
  onActivate?: () => void
  placeholder?: string
}) {
  const [text, setText] = useState(() => composeSearchInput(query))
  const [open, setOpen] = useState(false)
  const [highlight, setHighlight] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const textRef = useRef(text)
  useEffect(() => {
    textRef.current = text
  })

  // Adopt outside changes (back button, label chips, deep links) — but not
  // the echo of our own commit, which would reorder what the user typed.
  useEffect(() => {
    if (sameQuery(parseSearchInput(textRef.current), query)) return
    setText(composeSearchInput(query))
  }, [query.q, query.labelSelector, query.fieldSelector]) // eslint-disable-line react-hooks/exhaustive-deps

  // Debounced commit; invalid mid-edit selectors hold the previous query.
  useEffect(() => {
    const parsed = parseSearchInput(text)
    if (!parsed.committable || sameQuery(parsed, query)) return
    const t = window.setTimeout(() => onCommit(parsed), 300)
    return () => window.clearTimeout(t)
  }, [text, query, onCommit])

  const { head, token } = activeToken(text)
  const suggestions = useMemo(
    () => (open ? buildSuggestions(token, facets) : []),
    [open, token, facets],
  )

  useEffect(() => {
    setHighlight(0)
  }, [token, open])

  const accept = useCallback(
    (s: Suggestion) => {
      setText(head + s.token + (s.token.endsWith('=') ? '' : ' '))
      setOpen(true)
      inputRef.current?.focus()
    },
    [head],
  )

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      setOpen(false)
      return
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

  return (
    <div className="relative w-full max-w-md min-w-56">
      <input
        ref={inputRef}
        value={text}
        onChange={(e) => {
          setText(e.target.value)
          setOpen(true)
        }}
        onFocus={() => {
          onActivate?.()
          setOpen(true)
        }}
        onBlur={() => window.setTimeout(() => setOpen(false), 150)}
        onKeyDown={onKeyDown}
        placeholder={placeholder}
        aria-label="Search and filter"
        aria-expanded={open && suggestions.length > 0}
        aria-autocomplete="list"
        role="combobox"
        title={'Free text matches name, namespace and labels.\nFilter terms: app=web, tier!=cache, !canary, app in (web,api), status.phase=Running'}
        className="w-full bg-surface-2 px-2.5 py-1.5 text-[12.5px] text-ink ring-1 ring-border placeholder:text-ink-faint"
      />
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
    </div>
  )
}
