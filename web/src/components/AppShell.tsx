import clsx from 'clsx'
import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import {
  Link,
  NavLink,
  useMatch,
  useNavigate,
  useParams,
  useSearchParams,
} from 'react-router-dom'
import { api, groupSegment } from '../api/client'
import { useClusters, useDiscovery, useListAccess, useMe, useNamespaces } from '../api/hooks'
import type { HealthStatus } from '../api/types'
import { HEALTH_TONE, navLabel } from '../lib/format'
import {
  currentTheme,
  navStateKey,
  recordRecent,
  setTheme,
  stringArrayIn,
  writeJSON,
  type Theme,
} from '../lib/storage'
import {
  namespaceListReason,
  namespaceSearch,
  namespacesIn,
  onlyNamespace,
  scopeIsEmpty,
  scopeDescription,
  scopeLabel,
  toggleNamespace,
  withNamespaces,
} from '../lib/scope'
import { useStoredRaw } from '../lib/useStored'
import { SearchIcon } from './icons'
import { Button, Eyebrow, Listbox, Spinner, type ListboxItem } from './primitives'
import { CommandPalette } from './CommandPalette'
import { LogoMark, Wordmark } from './Logo'
import { ShortcutsOverlay } from './ShortcutsOverlay'
import { buildNav, type NavItem } from './nav'

/** The sidebar's top-level link style: an accent bar and soft fill on the
 *  active route, shared so the links cannot drift apart visually. */
function sidebarLinkClass({ isActive }: { isActive: boolean }): string {
  return clsx(
    'block border-l-2 py-1.5 pr-3 pl-4 text-[13px] transition-colors',
    isActive
      ? 'border-accent bg-accent-soft text-ink'
      : 'border-transparent text-ink-muted hover:border-border-strong hover:text-ink',
  )
}

/**
 * One of the cluster's own views — Nodes, Namespaces — in the block above the
 * sections.
 *
 * It reads like Overview and Events beside it rather than like a section item,
 * because that is what it is: a view of the cluster, not of what is running in
 * a namespace. The access dimming is the sections' though, so a reader who
 * cannot list nodes is told so here too.
 */
function ClusterScopedLink({
  item,
  cluster,
  denied,
}: {
  item: NavItem
  cluster: string
  denied?: boolean
}) {
  const to = `/c/${cluster}/r/${groupSegment(item.group)}/${item.version}/${item.resource}`
  return (
    <NavLink
      to={to}
      title={denied ? `You cannot list ${item.kind} cluster-wide` : item.kind}
      className={({ isActive }) => clsx(sidebarLinkClass({ isActive }), denied && 'opacity-40')}
    >
      {navLabel(item.kind)}
    </NavLink>
  )
}

function HealthDot({ status }: { status: HealthStatus }) {
  const tone = HEALTH_TONE[status]
  return (
    <span
      title={status}
      aria-label={`Cluster is ${status}`}
      className={clsx('size-2 shrink-0 rounded-full', {
        'bg-ok': tone === 'ok',
        'bg-warn': tone === 'warn',
        'bg-danger': tone === 'danger',
        'bg-idle': tone === 'idle',
      })}
    />
  )
}

/**
 * The cluster switcher is the one control that has to be right: picking the
 * wrong cluster is how people delete the wrong thing. Unreachable clusters
 * stay listed and clearly marked rather than disappearing.
 */
function ClusterSwitcher({ current }: { current?: string }) {
  const { data, isLoading } = useClusters()
  const navigate = useNavigate()

  // Memoised: a fresh [] every render would rebuild the option rows below it
  // on every render too.
  const clusters = useMemo(() => data?.clusters ?? [], [data])
  const active = clusters.find((c) => c.name === current)

  const items = useMemo(
    () =>
      clusters.map((c): ListboxItem => ({
        value: c.name,
        label: c.displayName,
        content: (
          <>
            <HealthDot status={c.health.status} />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-[13px] text-ink">{c.displayName}</span>
              <span className="block truncate font-mono text-[10.5px] text-ink-faint">
                {c.available
                  ? `${c.health.version ?? 'unknown version'} · ${c.health.latencyMs}ms`
                  : (c.error ?? 'unreachable')}
              </span>
              {c.labels && Object.keys(c.labels).length > 0 && (
                <span className="mt-1 flex flex-wrap gap-1">
                  {Object.entries(c.labels).map(([k, v]) => (
                    <span
                      key={k}
                      className="bg-canvas px-1 font-mono text-[10px] text-ink-faint ring-1 ring-border"
                    >
                      {k}={v}
                    </span>
                  ))}
                </span>
              )}
            </span>
          </>
        ),
      })),
    [clusters],
  )

  if (isLoading) {
    return (
      <div className="flex items-center gap-2 px-3 py-2 text-sm text-ink-faint">
        <Spinner className="size-3.5" /> Loading clusters
      </div>
    )
  }

  return (
    <Listbox
      items={items}
      value={current ?? ''}
      onSelect={(name) => navigate(`/c/${name}`)}
      ariaLabel="Clusters"
    >
      <span className="flex items-center gap-2">
        <HealthDot status={active?.health.status ?? 'unknown'} />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm font-medium text-ink">
            {active?.displayName ?? 'Select a cluster'}
          </span>
          <span className="block truncate font-mono text-[10.5px] text-ink-faint">
            {active?.health.version ?? `${clusters.length} available`}
          </span>
        </span>
      </span>
    </Listbox>
  )
}

/** The neutral first row: no namespace filter at all. */
function NamespacePicker({ cluster }: { cluster: string }) {
  const { names, isLoading, error } = useNamespaces(cluster)
  const [params, setParams] = useSearchParams()
  const labelId = useId()

  const scope = namespacesIn(params)
  const empty = scopeIsEmpty(params)
  /**
   * What is ticked.
   *
   * No parameter means everywhere, so everywhere is what the boxes show: the
   * list opens with all of them ticked, and narrowing is unticking. Drawing
   * that state as nothing ticked would read as "no namespaces selected" for
   * the state that is showing every one of them — and unticking all of them is
   * its own state, which shows nothing.
   */
  const ticked = empty ? [] : scope.length > 0 ? scope : names
  const selected = useMemo(
    () => new Set(ticked),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [ticked.join('\u0000')],
  )

  const emptyListReason = names.length > 0 ? undefined : namespaceListReason(isLoading, error)

  // Namespaces only. "All namespaces" used to lead the list, from when this
  // was a single-select and picking one row was the only way to change the
  // scope; with checkboxes and a Clear beneath them it was a third way to do
  // what Clear does, and the one row in the list that was not a namespace.
  const items = useMemo(
    (): ListboxItem[] => names.map((n) => ({ value: n, label: n })),
    [names],
  )

  const commit = (next: string[], none = false) =>
    setParams(withNamespaces(params, next, none), { replace: true })

  return (
    <div>
      <Eyebrow as="span" id={labelId} className="mb-1 block">
        Namespace
      </Eyebrow>
      <Listbox
        items={items}
        selected={selected}
        labelledBy={labelId}
        // The one place the word "namespace" appears is now the one control
        // named after it: the rows set the scope, and the row under them goes
        // to the namespaces themselves — status, labels, the ones that are
        // stuck Terminating, and the actions that operate on them.
        footer={
          <div className="text-[13px]">
            {/* An empty list of namespaces has three quite different causes
                and used to look the same for all of them: this cluster really
                has none, you may not list them, or the request did not come
                back. The last two are the common ones — a narrowly bound user
                is exactly who cannot list namespaces — and a picker that shows
                nothing without saying why reads as the first. */}
            {emptyListReason && (
              <p className="border-b border-border px-3 py-2 text-[12px] text-warn">
                {emptyListReason}
              </p>
            )}
            {/* Two rows rather than one: this popup is as wide as a 250px
                sidebar, and three controls on a line wrap into each other. */}
            <div className="flex items-center gap-3 border-b border-border px-3 py-1.5 text-[12px]">
              {/* One control, because with everything ticked by default
                  "select all" and "clear the selection" are the same state:
                  both show every namespace. This is the way back to it. */}
              <button
                type="button"
                onClick={() => commit([])}
                disabled={scope.length === 0 && !empty}
                title="Show every namespace you may read"
                className="text-ink-muted transition-colors hover:text-ink disabled:cursor-not-allowed disabled:opacity-40"
              >
                Select all
              </button>
              <span className="ml-auto text-[11px] text-ink-faint tabular-nums">
                {ticked.length} of {names.length}
              </span>
            </div>
            <Link
              to={`/c/${cluster}/r/core/v1/namespaces`}
              className="flex items-center justify-between px-3 py-2 text-accent-text transition-colors hover:bg-surface-2 hover:text-accent-text-hover"
            >
              Manage namespaces
              <span aria-hidden>→</span>
            </Link>
          </div>
        }
        onSelect={(value) => {
          const next = toggleNamespace(ticked, value)
          // Every namespace ticked is the default, and it travels as no
          // parameter at all: a shorter link, and one that keeps meaning
          // "everywhere" after a namespace is created tomorrow.
          if (next.length === names.length) return commit([])
          // None ticked is a state of its own, and it shows nothing. It has to
          // be spelled, or it would be indistinguishable from the default and
          // the boxes would spring back to all under the reader's hands.
          if (next.length === 0) return commit([], true)
          commit(next)
        }}
      >
        {/* "All", not "All namespaces": the eyebrow above already said the
            word, and the row inside the list still says it in full where it
            has to stand on its own. */}
        <span
          className={clsx('block truncate text-sm', empty ? 'text-warn' : 'text-ink')}
          title={scopeDescription(scope, empty)}
        >
          {scopeLabel(scope, empty)}
        </span>
      </Listbox>
    </div>
  )
}

/**
 * One collapsible group of resource links.
 *
 * Open state lives in the parent so it can be persisted per cluster; this
 * component only reports the toggle.
 */
function NavGroup({
  title,
  items,
  cluster,
  namespaces,
  noNamespaces,
  open,
  onToggle,
  showGroup,
  listAccess,
}: {
  title: string
  items: NavItem[]
  cluster: string
  /** The namespace scope, carried onto every namespaced link in the group. */
  namespaces: string[]
  /** Whether that scope is the deliberate nothing, which links carry too. */
  noNamespaces?: boolean
  open: boolean
  onToggle: () => void
  /** Appends the API group to each row — needed where kinds are unfamiliar. */
  showGroup?: boolean
  /**
   * "group/resource" → may list, from useListAccess. The map itself is
   * undefined while loading, and an undefined *entry* is a review that could
   * not be performed — neither is a "no", which is why the rows below test
   * for `=== false` rather than falsiness.
   */
  listAccess?: Map<string, boolean | undefined>
}) {
  if (items.length === 0) return null

  return (
    <div className="mt-4 first:mt-0">
      <button
        onClick={onToggle}
        aria-expanded={open}
        className="group flex w-full items-center gap-1.5 px-3 py-1 text-[10px] font-semibold tracking-wider text-ink-faint uppercase transition-colors hover:text-ink-muted"
      >
        <span
          aria-hidden
          className="w-2 text-[9px] opacity-60 transition-opacity group-hover:opacity-100"
        >
          {open ? '▾' : '▸'}
        </span>
        <span className="truncate">{title}</span>
        {!open && <span className="ml-auto tabular-nums opacity-70">{items.length}</span>}
      </button>

      {open && (
        <ul className="mt-0.5">
          {items.map((item) => {
            const groupSeg = groupSegment(item.group)
            const to = `/c/${cluster}/r/${groupSeg}/${item.version}/${item.resource}`
            // Cluster-scoped resources carry no scope: the picker does not
            // apply to them, and appending it would imply otherwise.
            const search = item.namespaced ? namespaceSearch(namespaces, noNamespaces) : ''
            // Dimmed, not hidden: hiding makes "where did Pods go?" support
            // tickets, and a namespace-scoped user may still see partial
            // results in a narrower scope than the one just checked.
            const denied = listAccess?.get(`${item.group}/${item.resource}`) === false

            return (
              <li key={`${item.group}/${item.resource}`}>
                <NavLink
                  to={to + search}
                  title={
                    denied
                      ? `You cannot list ${item.kind} ${
                          namespaces.length > 0
                            ? `in ${namespaces.join(', ')}`
                            : 'cluster-wide'
                        }`
                      : showGroup
                        ? `${item.kind} · ${item.group || 'core'}`
                        : item.kind
                  }
                  className={({ isActive }) =>
                    clsx(
                      // A left rule marks the active row instead of a filled
                      // block, which keeps the column quiet when scanning it.
                      'flex items-baseline gap-2 border-l-2 py-[3px] pr-3 pl-4 text-[13px] transition-colors',
                      denied && 'opacity-40',
                      isActive
                        ? 'border-accent bg-accent-soft text-ink'
                        : 'border-transparent text-ink-muted hover:border-border-strong hover:text-ink',
                    )
                  }
                >
                  <span className="truncate">{navLabel(item.kind)}</span>
                  {showGroup && item.group && (
                    <span className="ml-auto shrink-0 truncate font-mono text-[10px] text-ink-faint">
                      {item.group.replace(/\.k8s\.io$/, '')}
                    </span>
                  )}
                </NavLink>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

/**
 * Switches the palette and remembers the choice.
 *
 * The initial value is read from the document, not from storage, because
 * index.html has already resolved an absent preference against the system
 * setting — so a reader on a light desktop starts light without ever having
 * picked anything.
 */
function ThemeToggle() {
  const [theme, setLocal] = useState<Theme>(() => currentTheme())

  const flip = () => {
    const next: Theme = theme === 'dark' ? 'light' : 'dark'
    setTheme(next)
    setLocal(next)
  }

  return (
    <Button
      size="sm"
      icon
      onClick={flip}
      title={theme === 'dark' ? 'Switch to the light palette' : 'Switch to the dark palette'}
      aria-label={theme === 'dark' ? 'Switch to the light palette' : 'Switch to the dark palette'}
      className="text-ink-faint hover:text-ink"
    >
      {theme === 'dark' ? '☾' : '☀'}
    </Button>
  )
}

export function UserMenu() {
  const { data } = useMe()
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const popoverRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    popoverRef.current?.focus()
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false)
        triggerRef.current?.focus()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])

  if (!data) return null

  const label = data.user.name || data.user.email || data.user.username

  const signOut = async () => {
    // signedOut=1 tells the login page this was deliberate, so autoLogin
    // (when enabled) does not bounce the user straight back in.
    try {
      const res = await api.logout()
      window.location.href = res.endSessionURL ?? '/login?signedOut=1'
    } catch {
      // The server-side session may or may not have been destroyed; either
      // way the only useful place to send the user is the login page.
      window.location.href = '/login?signedOut=1'
    }
  }

  return (
    <div className="relative">
      <button
        ref={triggerRef}
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label={`Account: ${label}`}
        className="flex h-7 items-center gap-2 px-2 text-xs text-ink-muted transition-colors hover:text-ink"
      >
        <span className="grid size-5 place-items-center border border-accent-text/40 bg-accent/30 text-[11px] font-semibold text-ink">
          {label.slice(0, 1).toUpperCase()}
        </span>
        <span className="hidden max-w-40 truncate sm:block">{label}</span>
      </button>

      {open && (
        <>
          <div className="fixed inset-0 z-30" onClick={() => setOpen(false)} />
          <div
            ref={popoverRef}
            role="dialog"
            aria-label="Account"
            tabIndex={-1}
            className="animate-in absolute right-0 z-40 mt-1.5 w-[270px] bg-raised p-3 shadow-[0_16px_40px_rgba(0,0,0,.6)] ring-1 ring-border-strong outline-none"
          >
            <p className="truncate text-[13px] font-medium text-ink">{label}</p>
            <p className="truncate font-mono text-[11px] text-ink-faint">{data.user.username}</p>

            {data.user.groups && data.user.groups.length > 0 && (
              <div className="mt-2.5">
                <p className="text-[11px] text-ink-faint">Groups</p>
                <div className="mt-1 flex flex-wrap gap-1">
                  {data.user.groups.map((g) => (
                    <span
                      key={g}
                      className="bg-canvas px-1.5 py-0.5 font-mono text-[10px] text-ink-muted ring-1 ring-border"
                    >
                      {g}
                    </span>
                  ))}
                </div>
              </div>
            )}

            {data.anonymous ? (
              <p className="mt-3 bg-warn/10 px-2 py-1.5 text-[11px] text-warn ring-1 ring-warn/25">
                Authentication is disabled on this server.
              </p>
            ) : (
              <div className="mt-3">
                <Button size="sm" onClick={signOut}>
                  Sign out
                </Button>
              </div>
            )}
          </div>
        </>
      )}
    </div>
  )
}

const CUSTOM_SECTION = 'Custom resources'
const ALL_SECTION = 'All resources'

/** Custom-resource groups are the usual reason to browse, so a short list opens. */
const AUTO_OPEN_CUSTOM_LIMIT = 8

export function AppShell({ children }: { children: React.ReactNode }) {
  const { cluster } = useParams<{ cluster: string }>()
  const [params] = useSearchParams()
  const namespaces = namespacesIn(params)
  const noNamespaces = scopeIsEmpty(params)
  // The single namespace, for the panes that can only take one — the command
  // palette's jump targets, and the access checks that ask about one scope.
  const namespace = onlyNamespace(namespaces) ?? ''
  const { data: discovery, isLoading: discoveryLoading } = useDiscovery(cluster)

  const nav = useMemo(() => buildNav(discovery), [discovery])

  // One batched question per scope: which of these can this identity list?
  // The answers dim what would only produce a 403.
  const allNavItems = useMemo(
    () => [...nav.primary.flatMap((s) => s.items), ...nav.custom, ...nav.rest],
    [nav],
  )
  const listAccess = useListAccess(cluster, namespace, allNavItems)

  // AppShell renders *outside* the nested resource routes, so useParams cannot
  // see the resource being viewed. Matching the path explicitly is what gives
  // the sidebar and the breadcrumb something to work with.
  const resourceMatch = useMatch({ path: '/c/:cluster/r/:group/:version/:resource', end: false })
  const detailMatch = useMatch('/c/:cluster/r/:group/:version/:resource/:namespace/:name')
  const currentResource = resourceMatch?.params.resource
  const currentGroup = resourceMatch?.params.group

  const matchesRoute = useCallback(
    (item: NavItem) =>
      item.resource === currentResource &&
      (currentGroup === 'core' ? item.group === '' : item.group === currentGroup),
    [currentResource, currentGroup],
  )

  const defaultOpen = useMemo(() => {
    const titles = nav.primary.map((s) => s.title)
    if (nav.custom.length > 0 && nav.custom.length <= AUTO_OPEN_CUSTOM_LIMIT) {
      titles.push(CUSTOM_SECTION)
    }
    return titles
  }, [nav])

  // Open state is per cluster, because the resources differ between them, and
  // it is read during render rather than copied into state by an effect: an
  // effect runs after paint, so the sidebar would draw once with the default
  // sections open and then rearrange itself into the stored shape.
  const navKey = navStateKey(cluster ?? '')
  const navRaw = useStoredRaw(navKey)
  const openSections = useMemo(
    () => (cluster ? stringArrayIn(navRaw, defaultOpen) : defaultOpen),
    [navRaw, cluster, defaultOpen],
  )

  const toggleSection = useCallback(
    (title: string) => {
      if (!cluster) return
      const next = openSections.includes(title)
        ? openSections.filter((t) => t !== title)
        : [...openSections, title]
      // The write is the state change: it notifies, and the value above is
      // recomputed from the store.
      writeJSON(navKey, next)
    },
    [cluster, navKey, openSections],
  )

  // Whatever you are looking at is always visible, whatever the stored state
  // says, so a deep link never lands you inside a collapsed group.
  const activeSection = useMemo(() => {
    if (!currentResource) return undefined
    for (const section of nav.primary) {
      if (section.items.some(matchesRoute)) return section.title
    }
    if (nav.custom.some(matchesRoute)) return CUSTOM_SECTION
    if (nav.rest.some(matchesRoute)) return ALL_SECTION
    return undefined
  }, [nav, currentResource, matchesRoute])

  const isOpen = (title: string) => openSections.includes(title) || title === activeSection

  // Remember where you have been, to prefill the palette.
  useEffect(() => {
    if (!cluster || !currentResource) return
    const item = [...nav.primary.flatMap((s) => s.items), ...nav.custom, ...nav.rest].find(
      matchesRoute,
    )
    if (!item) return
    recordRecent({
      cluster,
      group: item.group,
      version: item.version,
      resource: item.resource,
      kind: item.kind,
      namespaced: item.namespaced,
    })
  }, [cluster, currentResource, matchesRoute, nav])

  const [paletteOpen, setPaletteOpen] = useState(false)
  const [shortcutsOpen, setShortcutsOpen] = useState(false)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        // Inside an exec session the keystroke belongs to the shell — Ctrl+K
        // is readline's kill-to-end-of-line, not our palette.
        if (e.target instanceof Element && e.target.closest('.xterm')) return
        e.preventDefault()
        setPaletteOpen((v) => !v)
        return
      }
      if (e.key === '?' && e.target instanceof Element) {
        // "?" is a character people type; only steal it outside of inputs.
        if (e.target.closest('input, textarea, select, .xterm, [contenteditable]')) return
        e.preventDefault()
        setShortcutsOpen((v) => !v)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <div className="flex h-full">
      <aside className="flex w-[250px] shrink-0 flex-col border-r border-border bg-surface">
        <div className="border-b border-border p-3">
          <Link to="/" className="mb-3 flex items-center gap-2.5">
            <LogoMark className="size-5" />
            <Wordmark />
          </Link>
          <ClusterSwitcher current={cluster} />
        </div>

        {cluster && (
          <>
            <div className="border-b border-border p-3">
              <NamespacePicker cluster={cluster} />
            </div>

            <nav className="flex-1 overflow-y-auto px-0 py-2">
              <NavLink to={`/c/${cluster}`} end className={sidebarLinkClass}>
                Overview
              </NavLink>
              <NavLink
                to={`/c/${cluster}/events${namespaceSearch(namespaces, noNamespaces)}`}
                className={sidebarLinkClass}
              >
                Events
              </NavLink>
              {/* The cluster's own resources belong with the cluster's own
                  pages. Below this point every list is filtered by the
                  namespace picker; nothing above it is. */}
              {nav.clusterScoped.map((item) => (
                <ClusterScopedLink
                  key={`${item.group}/${item.resource}`}
                  item={item}
                  cluster={cluster}
                  denied={listAccess?.get(`${item.group}/${item.resource}`) === false}
                />
              ))}

              {discoveryLoading && (
                <p className="flex items-center gap-2 px-3 py-2 text-sm text-ink-faint">
                  <Spinner className="size-3.5" /> Reading API surface
                </p>
              )}

              <div className="mt-2">
                {nav.primary.map((section) => (
                  <NavGroup
                    key={section.title}
                    title={section.title}
                    items={section.items}
                    cluster={cluster}
                    namespaces={namespaces}
                    noNamespaces={noNamespaces}
                    open={isOpen(section.title)}
                    onToggle={() => toggleSection(section.title)}
                    listAccess={listAccess}
                  />
                ))}

                <NavGroup
                  title={CUSTOM_SECTION}
                  items={nav.custom}
                  cluster={cluster}
                  namespaces={namespaces}
                  noNamespaces={noNamespaces}
                  open={isOpen(CUSTOM_SECTION)}
                  onToggle={() => toggleSection(CUSTOM_SECTION)}
                  showGroup
                  listAccess={listAccess}
                />

                <NavGroup
                  title={ALL_SECTION}
                  items={nav.rest}
                  cluster={cluster}
                  namespaces={namespaces}
                  noNamespaces={noNamespaces}
                  open={isOpen(ALL_SECTION)}
                  onToggle={() => toggleSection(ALL_SECTION)}
                  showGroup
                  listAccess={listAccess}
                />
              </div>
            </nav>
          </>
        )}
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-[46px] shrink-0 items-center justify-between gap-3 border-b border-border bg-surface px-4">
          <Breadcrumbs
            cluster={cluster}
            resource={currentResource}
            name={detailMatch?.params.name}
          />
          <div className="flex items-center gap-2.5">
            {/* Search reaches every cluster and every resource, which is not
                what the sidebar below it describes: that column is one
                cluster's navigation, and a control that ignores the scope it
                sat under read as part of it. Here it sits with the other
                things that belong to the whole console. */}
            {cluster && (
              <button
                onClick={() => setPaletteOpen(true)}
                title="Search objects across every cluster, or jump to a resource (⌘K)"
                className="flex h-7 items-center gap-2 bg-surface-2 px-2.5 text-left text-xs text-ink-faint ring-1 ring-border transition-colors hover:text-ink-muted sm:w-56"
              >
                <SearchIcon className="size-3.5 shrink-0" />
                {/* The word goes first on a wide header and drops on a narrow
                    one, where the icon and the shortcut still say what it is. */}
                <span className="hidden sm:inline">Search</span>
                <kbd className="ml-auto hidden border border-ink/18 px-1 font-sans text-[10px] text-ink-faint sm:inline">
                  ⌘K
                </kbd>
              </button>
            )}
            <Button
              size="sm"
              icon
              onClick={() => setShortcutsOpen(true)}
              title="Keyboard shortcuts"
              aria-label="Keyboard shortcuts"
              className="text-ink-faint hover:text-ink"
            >
              ?
            </Button>
            <ThemeToggle />
            <UserMenu />
          </div>
        </header>
        <main className="min-h-0 flex-1 overflow-auto">{children}</main>
      </div>

      {cluster && (
        <CommandPalette
          open={paletteOpen}
          onClose={() => setPaletteOpen(false)}
          cluster={cluster}
          namespace={namespace}
          primary={nav.primary.flatMap((s) => s.items)}
        />
      )}
      <ShortcutsOverlay open={shortcutsOpen} onClose={() => setShortcutsOpen(false)} />
    </div>
  )
}

function Breadcrumbs({
  cluster,
  resource,
  name,
}: {
  cluster?: string
  resource?: string
  name?: string
}) {
  if (!cluster) return <span className="text-sm text-ink-faint">Orrery</span>

  return (
    <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-1.5 text-[13px]">
      <Link to={`/c/${cluster}`} className="truncate text-ink-muted hover:text-ink">
        {cluster}
      </Link>
      {resource && (
        <>
          <span className="text-ink-faint">/</span>
          <span className="truncate text-ink-muted">{resource}</span>
        </>
      )}
      {name && (
        <>
          <span className="text-ink-faint">/</span>
          <span className="truncate font-medium text-ink">{name}</span>
        </>
      )}
    </nav>
  )
}
