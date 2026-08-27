import clsx from 'clsx'
import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { groupSegment } from '../api/client'
import { useClusters, useDiscovery, useNamespaces, useObjectSearch } from '../api/hooks'
import type { APIResource } from '../api/types'
import { consoleHref } from '../lib/consoleHref'
import { navLabel } from '../lib/format'
import { rankResources, selectedIndex } from '../lib/palette'
import { readRecents, readSaved, savedKey, type SavedSearch } from '../lib/storage'
import { isBrowsable, isCustomGroup, type NavItem } from './nav'

interface Entry {
  id: string
  section: string
  label: string
  hint: string
  run: () => void
}

interface CommandPaletteProps {
  open: boolean
  onClose: () => void
  cluster: string
  namespace: string
  /**
   * The sidebar's curated set, used to fill the palette before anything is
   * typed. Ranking an empty query would just sort every resource
   * alphabetically, which puts APIServices at the top and helps nobody.
   */
  primary: NavItem[]
}

/** How long typing must settle before the fleet is searched. */
const SEARCH_DEBOUNCE_MS = 220

const MAX_PER_SECTION = 8

export function CommandPalette({
  open,
  onClose,
  cluster,
  namespace,
  primary,
}: CommandPaletteProps) {
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const [selectedId, setSelectedId] = useState<string | undefined>(undefined)

  const { data: discovery } = useDiscovery(open ? cluster : undefined)
  const { data: clusterList } = useClusters()
  const { names: namespaceNames } = useNamespaces(open ? cluster : undefined)

  const listRef = useRef<HTMLUListElement>(null)

  // The palette's own filtering is local and instant; object search crosses
  // every cluster, so it waits for typing to settle.
  const [settled, setSettled] = useState('')
  useEffect(() => {
    const t = window.setTimeout(() => setSettled(query.trim()), SEARCH_DEBOUNCE_MS)
    return () => window.clearTimeout(t)
  }, [query])
  const objects = useObjectSearch(settled, open)

  // Reset on each open so the palette never reopens mid-search. Focus is
  // handled by autoFocus below rather than here: the input mounts fresh every
  // time, and scheduling a focus() on the next animation frame silently does
  // nothing when the tab is not being painted.
  useEffect(() => {
    if (open) {
      setQuery('')
      setSelectedId(undefined)
    }
  }, [open])

  const resources = useMemo<APIResource[]>(
    () => discovery?.groups.flatMap((g) => g.resources).filter((r) => isBrowsable(r.group)) ?? [],
    [discovery],
  )

  const resourceHref = useCallback(
    (r: { group: string; version: string; name: string; namespaced: boolean }) => {
      const scope = r.namespaced && namespace ? `?namespace=${namespace}` : ''
      return `/c/${cluster}/r/${groupSegment(r.group)}/${r.version}/${r.name}${scope}`
    },
    [cluster, namespace],
  )

  const entries = useMemo<Entry[]>(() => {
    const q = query.trim()
    const out: Entry[] = []

    const seen = new Set<string>()
    const pushResource = (
      section: string,
      r: { kind: string; group: string; version: string; name: string; namespaced: boolean },
    ) => {
      const id = `resource:${r.group}/${r.name}`
      if (seen.has(id)) return
      seen.add(id)
      out.push({
        id,
        section,
        // Show the real kind alongside the short label so an abbreviation like
        // "PVCs" is never ambiguous.
        label: navLabel(r.kind),
        hint: `${r.kind} · ${r.group || 'core'}`,
        run: () => navigate(resourceHref(r)),
      })
    }

    const savedHref = (v: SavedSearch) => {
      const qs = new URLSearchParams()
      if (v.namespace) qs.set('namespace', v.namespace)
      if (v.q) qs.set('q', v.q)
      const tail = qs.toString()
      return `/c/${v.cluster}/r/${v.group}/${v.version}/${v.resource}${tail ? `?${tail}` : ''}`
    }

    const pushSaved = (v: SavedSearch) => {
      const id = `saved:${savedKey(v)}`
      if (seen.has(id)) return
      seen.add(id)
      out.push({
        id,
        section: 'Saved',
        label: navLabel(v.kind),
        // The query is the point of a saved view, so it is the hint rather
        // than the group — "Pods · status.phase=Running" reads as the thing
        // that was actually starred.
        hint: [v.namespace || 'all namespaces', v.q || 'no filter'].join(' · '),
        run: () => navigate(savedHref(v)),
      })
    }

    const pages = [
      { id: 'page:overview', label: 'Overview', href: `/c/${cluster}` },
      {
        id: 'page:events',
        label: 'Events',
        href: `/c/${cluster}/events${namespace ? `?namespace=${namespace}` : ''}`,
      },
    ]
    const pushPage = (p: (typeof pages)[number]) =>
      out.push({
        id: p.id,
        section: 'Pages',
        label: p.label,
        hint: 'open page',
        run: () => navigate(p.href),
      })

    if (!q) {
      // Nothing typed: offer what was starred, then where you have been, then
      // the everyday set.
      for (const v of readSaved(cluster).slice(0, 6)) {
        pushSaved(v)
      }
      for (const r of readRecents(cluster).slice(0, 5)) {
        pushResource('Recent', { ...r, name: r.resource })
      }
      for (const item of primary) {
        pushResource('Common', { ...item, name: item.resource })
      }
      return out
    }

    for (const p of pages) {
      if (p.label.toLowerCase().includes(q.toLowerCase())) pushPage(p)
    }

    const needle = q.toLowerCase()
    for (const v of readSaved(cluster)) {
      if (
        navLabel(v.kind).toLowerCase().includes(needle) ||
        v.q.toLowerCase().includes(needle) ||
        v.namespace.toLowerCase().includes(needle)
      ) {
        pushSaved(v)
      }
    }

    for (const r of rankResources(resources, q).slice(0, MAX_PER_SECTION)) {
      pushResource('Resources', r)
    }

    if (q) {
      const needle = q.toLowerCase()
      for (const ns of namespaceNames.filter((n) => n.toLowerCase().includes(needle)).slice(0, 5)) {
        out.push({
          id: `namespace:${ns}`,
          section: 'Namespaces',
          label: ns,
          hint: 'scope to this namespace',
          run: () => {
            const url = new URL(window.location.href)
            url.searchParams.set('namespace', ns)
            url.searchParams.delete('page')
            navigate(url.pathname + url.search)
          },
        })
      }

      for (const c of (clusterList?.clusters ?? [])
        .filter((c) => c.name.toLowerCase().includes(needle) || c.displayName.toLowerCase().includes(needle))
        .slice(0, 5)) {
        out.push({
          id: `cluster:${c.name}`,
          section: 'Clusters',
          label: c.displayName,
          hint: c.available ? 'switch cluster' : 'unreachable',
          run: () => navigate(`/c/${c.name}`),
        })
      }
    }

    // Objects found across the fleet. Last, because everything above is a
    // local match on something the user already has open, and this is the one
    // section that had to go and ask.
    for (const hit of objects.data?.hits ?? []) {
      const where = hit.namespace ? `${hit.cluster}/${hit.namespace}` : hit.cluster
      out.push({
        id: `object:${hit.cluster}:${hit.resource}:${hit.namespace ?? ''}:${hit.name}`,
        section: 'Objects',
        label: `${hit.kind}/${hit.name}`,
        hint: hit.status ? `${where} · ${hit.status}` : where,
        run: () => navigate(consoleHref(hit)),
      })
    }

    return out
    // `open` is a real input: the Recents list is read from localStorage, so it
    // must be recomputed on every open, not only when the query changes.
  }, [open, query, resources, primary, namespaceNames, clusterList, cluster, namespace, navigate, resourceHref, objects.data])

  // Derived, so it is always in range and always points at the entry the user
  // actually chose, however the list has been rearranged underneath.
  const selected = selectedIndex(entries, selectedId)

  useEffect(() => {
    listRef.current?.querySelector('[data-selected="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [selected, entries.length])

  if (!open) return null

  const choose = (entry?: Entry) => {
    if (!entry) return
    entry.run()
    onClose()
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        if (entries.length > 0) setSelectedId(entries[(selected + 1) % entries.length].id)
        break
      case 'ArrowUp':
        e.preventDefault()
        if (entries.length > 0) {
          setSelectedId(entries[(selected - 1 + entries.length) % entries.length].id)
        }
        break
      case 'Enter':
        e.preventDefault()
        choose(entries[selected])
        break
      case 'Escape':
        e.preventDefault()
        onClose()
        break
    }
  }

  let lastSection = ''

  return (
    <div
      className="fixed inset-0 z-[70] flex justify-center bg-black/60 px-4 pt-[12vh]"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Jump to"
        className="animate-in h-fit w-full max-w-[560px] overflow-hidden bg-raised shadow-[0_16px_40px_rgba(0,0,0,.6)] ring-1 ring-border-strong"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={onKeyDown}
      >
        <div className="flex items-center gap-2 border-b border-border px-3">
          <span aria-hidden className="text-ink-faint">
            ›
          </span>
          <input
            autoFocus
            value={query}
            onChange={(e) => {
              setQuery(e.target.value)
              setSelectedId(undefined)
            }}
            placeholder="Search objects across every cluster, or jump to a resource"
            aria-label="Search"
            role="combobox"
            aria-expanded="true"
            aria-autocomplete="list"
            aria-controls="palette-results"
            aria-activedescendant={entries[selected]?.id}
            className="w-full bg-transparent py-3 text-[13.5px] text-ink outline-none placeholder:text-ink-faint"
          />
        </div>

        <ul id="palette-results" ref={listRef} role="listbox" className="max-h-80 overflow-y-auto py-1">
          {entries.length === 0 && (
            <li role="presentation" className="px-3 py-6 text-center text-sm text-ink-faint">
              Nothing matches “{query}”.
            </li>
          )}

          {entries.map((entry, i) => {
            const header = entry.section !== lastSection ? entry.section : null
            lastSection = entry.section
            const isSelected = i === selected

            return (
              <Fragment key={entry.id}>
                {/* Section headers sit between options, outside the listbox
                    semantics — a listbox may only own options. */}
                {header && (
                  <li role="presentation">
                    <p className="px-3 pt-2 pb-1 text-[10px] font-semibold tracking-wider text-ink-faint uppercase">
                      {header}
                    </p>
                  </li>
                )}
                <li
                  id={entry.id}
                  role="option"
                  aria-selected={isSelected}
                  data-selected={isSelected}
                  // mousemove, not mouseenter: arrow-key navigation scrolls the
                  // list under a stationary cursor, and the resulting synthetic
                  // mouseenter would yank the selection right back.
                  onMouseMove={() => setSelectedId(entry.id)}
                  onClick={() => choose(entry)}
                  className={clsx(
                    'flex w-full cursor-pointer items-baseline gap-3 px-3 py-1.5 text-left text-[13px]',
                    isSelected ? 'bg-accent/20 text-ink' : 'text-ink-muted',
                  )}
                >
                  <span className="truncate">{entry.label}</span>
                  <span className="ml-auto shrink-0 truncate font-mono text-[11px] text-ink-faint">
                    {entry.hint}
                  </span>
                </li>
              </Fragment>
            )
          })}
        </ul>

        <div className="flex gap-3.5 border-t border-border px-3 py-1.5 text-[10.5px] text-ink-faint">
          <span>↑↓ navigate</span>
          <span>⏎ open</span>
          <span>esc close</span>
        </div>
      </div>
    </div>
  )
}
