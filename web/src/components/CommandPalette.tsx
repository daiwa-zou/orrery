import clsx from 'clsx'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { groupSegment } from '../api/client'
import { useClusters, useDiscovery, useNamespaces } from '../api/hooks'
import type { APIResource } from '../api/types'
import { navLabel } from '../lib/format'
import { readRecents } from '../lib/storage'
import { isBrowsable, isCustomGroup, type NavItem } from './nav'

/** The subset of a resource the ranker needs, so it can be tested on its own. */
export interface RankableResource {
  kind: string
  name: string
  singularName?: string
  shortNames?: string[]
  group: string
}

/**
 * Scores a resource against a query. Lower is better; Infinity means no match.
 *
 * The ordering is deliberate. An exact short-name hit wins outright, because
 * short names are what people type when they know exactly what they want —
 * "cm" should land on ConfigMaps, not on whatever else happens to contain
 * those two letters. Exact full names come next, then prefixes, then
 * substrings, with the API group considered last so it never outranks a kind.
 */
export function scoreResource(r: RankableResource, query: string): number {
  const q = query.trim().toLowerCase()
  if (!q) return 0

  const kind = r.kind.toLowerCase()
  const name = r.name.toLowerCase()
  const singular = (r.singularName ?? '').toLowerCase()
  const shorts = (r.shortNames ?? []).map((s) => s.toLowerCase())
  const group = r.group.toLowerCase()

  if (shorts.includes(q)) return 0
  if (kind === q || name === q || singular === q) return 1
  if (shorts.some((s) => s.startsWith(q))) return 2
  if (kind.startsWith(q)) return 3
  if (name.startsWith(q) || (singular !== '' && singular.startsWith(q))) return 4
  if (group.startsWith(q)) return 5
  if (kind.includes(q)) return 6
  if (name.includes(q)) return 7
  if (group.includes(q)) return 8
  return Infinity
}

/** Ranks and filters resources for a query, best first. */
export function rankResources<T extends RankableResource>(items: T[], query: string): T[] {
  return items
    .map((item) => ({ item, score: scoreResource(item, query) }))
    .filter(({ score }) => score !== Infinity)
    .sort((a, b) => {
      if (a.score !== b.score) return a.score - b.score
      // Built-ins before custom resources at equal score, then alphabetical, so
      // an operator's CRD cannot bury the kind of the same name people meant.
      const aCustom = isCustomGroup(a.item.group)
      const bCustom = isCustomGroup(b.item.group)
      if (aCustom !== bCustom) return aCustom ? 1 : -1
      return a.item.kind.localeCompare(b.item.kind)
    })
    .map(({ item }) => item)
}

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
  const [selected, setSelected] = useState(0)

  const { data: discovery } = useDiscovery(open ? cluster : undefined)
  const { data: clusterList } = useClusters()
  const { names: namespaceNames } = useNamespaces(open ? cluster : undefined)

  const listRef = useRef<HTMLUListElement>(null)

  // Reset on each open so the palette never reopens mid-search. Focus is
  // handled by autoFocus below rather than here: the input mounts fresh every
  // time, and scheduling a focus() on the next animation frame silently does
  // nothing when the tab is not being painted.
  useEffect(() => {
    if (open) {
      setQuery('')
      setSelected(0)
    }
  }, [open])

  const resources = useMemo<APIResource[]>(
    () => discovery?.groups.flatMap((g) => g.resources).filter((r) => isBrowsable(r.group)) ?? [],
    [discovery],
  )

  const resourceHref = (r: { group: string; version: string; name: string; namespaced: boolean }) => {
    const scope = r.namespaced && namespace ? `?namespace=${namespace}` : ''
    return `/c/${cluster}/r/${groupSegment(r.group)}/${r.version}/${r.name}${scope}`
  }

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
      // Nothing typed: offer where you have been, then the everyday set.
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

    return out
  }, [query, resources, primary, namespaceNames, clusterList, cluster, namespace, navigate])

  // Clamp the cursor when the result set shrinks under it.
  useEffect(() => {
    setSelected((i) => (i >= entries.length ? Math.max(0, entries.length - 1) : i))
  }, [entries.length])

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
        setSelected((i) => (entries.length === 0 ? 0 : (i + 1) % entries.length))
        break
      case 'ArrowUp':
        e.preventDefault()
        setSelected((i) => (entries.length === 0 ? 0 : (i - 1 + entries.length) % entries.length))
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
        className="animate-in h-fit w-full max-w-xl overflow-hidden rounded-lg bg-surface shadow-2xl ring-1 ring-border"
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
              setSelected(0)
            }}
            placeholder="Search resources, namespaces and clusters"
            aria-label="Search"
            aria-controls="palette-results"
            aria-activedescendant={entries[selected]?.id}
            className="w-full bg-transparent py-3 text-sm text-ink outline-none placeholder:text-ink-faint"
          />
        </div>

        <ul id="palette-results" ref={listRef} role="listbox" className="max-h-80 overflow-y-auto py-1">
          {entries.length === 0 && (
            <li className="px-3 py-6 text-center text-sm text-ink-faint">
              Nothing matches “{query}”.
            </li>
          )}

          {entries.map((entry, i) => {
            const header = entry.section !== lastSection ? entry.section : null
            lastSection = entry.section
            const isSelected = i === selected

            return (
              <li key={entry.id}>
                {header && (
                  <p className="px-3 pt-2 pb-1 text-[10px] font-semibold tracking-wider text-ink-faint uppercase">
                    {header}
                  </p>
                )}
                <button
                  id={entry.id}
                  role="option"
                  aria-selected={isSelected}
                  data-selected={isSelected}
                  onMouseEnter={() => setSelected(i)}
                  onClick={() => choose(entry)}
                  className={clsx(
                    'flex w-full items-baseline gap-3 px-3 py-1.5 text-left text-sm',
                    isSelected ? 'bg-accent-soft/60 text-ink' : 'text-ink-muted',
                  )}
                >
                  <span className="truncate">{entry.label}</span>
                  <span className="ml-auto shrink-0 truncate font-mono text-[11px] text-ink-faint">
                    {entry.hint}
                  </span>
                </button>
              </li>
            )
          })}
        </ul>

        <div className="flex gap-3 border-t border-border px-3 py-1.5 text-[11px] text-ink-faint">
          <span>↑↓ navigate</span>
          <span>⏎ open</span>
          <span>esc close</span>
        </div>
      </div>
    </div>
  )
}
