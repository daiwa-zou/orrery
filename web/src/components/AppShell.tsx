import clsx from 'clsx'
import { useMemo, useState } from 'react'
import { Link, NavLink, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import { useClusters, useDiscovery, useMe, useNamespaces } from '../api/hooks'
import type { ClusterSummary, HealthStatus } from '../api/types'
import { Badge, Button, Spinner } from './primitives'
import { buildNav, type NavSection } from './nav'

const HEALTH_TONE: Record<HealthStatus, 'ok' | 'warn' | 'danger' | 'idle'> = {
  healthy: 'ok',
  degraded: 'warn',
  unreachable: 'danger',
  unknown: 'idle',
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
  const [open, setOpen] = useState(false)
  const navigate = useNavigate()

  const clusters = data?.clusters ?? []
  const active = clusters.find((c) => c.name === current)

  if (isLoading) {
    return (
      <div className="flex items-center gap-2 px-3 py-2 text-sm text-ink-faint">
        <Spinner className="size-3.5" /> Loading clusters
      </div>
    )
  }

  const select = (cluster: ClusterSummary) => {
    setOpen(false)
    navigate(`/c/${cluster.name}`)
  }

  return (
    <div className="relative">
      <button
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 rounded-md bg-surface-2 px-3 py-2 text-left ring-1 ring-border transition-colors hover:bg-border/50"
        aria-haspopup="listbox"
        aria-expanded={open}
      >
        <HealthDot status={active?.health.status ?? 'unknown'} />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm font-medium text-ink">
            {active?.displayName ?? 'Select a cluster'}
          </span>
          <span className="block truncate text-[11px] text-ink-faint">
            {active?.health.version ?? `${clusters.length} available`}
          </span>
        </span>
        <span aria-hidden className="text-ink-faint">
          ▾
        </span>
      </button>

      {open && (
        <>
          <div className="fixed inset-0 z-30" onClick={() => setOpen(false)} />
          <ul
            role="listbox"
            className="animate-in absolute z-40 mt-1 max-h-96 w-full overflow-auto rounded-md bg-surface py-1 shadow-2xl ring-1 ring-border"
          >
            {clusters.map((c) => (
              <li key={c.name}>
                <button
                  role="option"
                  aria-selected={c.name === current}
                  onClick={() => select(c)}
                  className={clsx(
                    'flex w-full items-start gap-2 px-3 py-2 text-left hover:bg-surface-2',
                    c.name === current && 'bg-accent-soft/40',
                  )}
                >
                  <HealthDot status={c.health.status} />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm text-ink">{c.displayName}</span>
                    <span className="block truncate text-[11px] text-ink-faint">
                      {c.available
                        ? `${c.health.version ?? 'unknown version'} · ${c.health.latencyMs}ms`
                        : (c.error ?? 'unreachable')}
                    </span>
                    {c.labels && Object.keys(c.labels).length > 0 && (
                      <span className="mt-1 flex flex-wrap gap-1">
                        {Object.entries(c.labels).map(([k, v]) => (
                          <span
                            key={k}
                            className="rounded bg-surface-2 px-1 text-[10px] text-ink-faint ring-1 ring-border"
                          >
                            {k}={v}
                          </span>
                        ))}
                      </span>
                    )}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  )
}

function NamespacePicker({ cluster }: { cluster: string }) {
  const { names } = useNamespaces(cluster)
  const [params, setParams] = useSearchParams()
  const current = params.get('namespace') ?? ''

  return (
    <label className="block">
      <span className="mb-1 block text-[11px] tracking-wide text-ink-faint uppercase">
        Namespace
      </span>
      <select
        value={current}
        onChange={(e) => {
          const next = new URLSearchParams(params)
          if (e.target.value) next.set('namespace', e.target.value)
          else next.delete('namespace')
          // Changing scope invalidates the page you were on.
          next.delete('page')
          setParams(next)
        }}
        className="w-full rounded-md bg-surface-2 px-2 py-1.5 text-sm text-ink ring-1 ring-border"
      >
        <option value="">All namespaces</option>
        {names.map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </select>
    </label>
  )
}

function NavGroup({
  section,
  cluster,
  namespace,
  defaultOpen,
}: {
  section: NavSection
  cluster: string
  namespace: string
  defaultOpen: boolean
}) {
  const [open, setOpen] = useState(defaultOpen)

  return (
    <div className="mb-1">
      <button
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center justify-between px-3 py-1.5 text-[11px] font-semibold tracking-wide text-ink-faint uppercase hover:text-ink-muted"
      >
        <span className="truncate">{section.title}</span>
        <span aria-hidden className="ml-2 text-[9px]">
          {open ? '▾' : '▸'}
        </span>
      </button>

      {open && (
        <ul>
          {section.items.map((item) => {
            const groupSeg = item.group === '' ? 'core' : item.group
            const to = `/c/${cluster}/r/${groupSeg}/${item.version}/${item.resource}`
            const search = item.namespaced && namespace ? `?namespace=${namespace}` : ''
            return (
              <li key={`${item.group}/${item.resource}`}>
                <NavLink
                  to={to + search}
                  className={({ isActive }) =>
                    clsx(
                      'block truncate rounded px-3 py-1 text-sm transition-colors',
                      isActive
                        ? 'bg-accent-soft/50 text-ink'
                        : 'text-ink-muted hover:bg-surface-2 hover:text-ink',
                    )
                  }
                >
                  {item.label}
                </NavLink>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

function UserMenu() {
  const { data } = useMe()
  const [open, setOpen] = useState(false)
  if (!data) return null

  const label = data.user.name || data.user.email || data.user.username

  const signOut = async () => {
    const res = await api.logout()
    window.location.href = res.endSessionURL ?? '/login'
  }

  return (
    <div className="relative">
      <button
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm text-ink-muted hover:bg-surface-2 hover:text-ink"
      >
        <span className="grid size-6 place-items-center rounded-full bg-accent-soft text-[11px] font-semibold text-ink">
          {label.slice(0, 1).toUpperCase()}
        </span>
        <span className="hidden max-w-40 truncate sm:block">{label}</span>
      </button>

      {open && (
        <>
          <div className="fixed inset-0 z-30" onClick={() => setOpen(false)} />
          <div className="animate-in absolute right-0 z-40 mt-1 w-72 rounded-md bg-surface p-3 shadow-2xl ring-1 ring-border">
            <p className="truncate text-sm font-medium text-ink">{label}</p>
            <p className="truncate font-mono text-[11px] text-ink-faint">{data.user.username}</p>

            {data.user.groups && data.user.groups.length > 0 && (
              <div className="mt-2">
                <p className="text-[11px] text-ink-faint">Groups</p>
                <div className="mt-1 flex flex-wrap gap-1">
                  {data.user.groups.map((g) => (
                    <span
                      key={g}
                      className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[10px] text-ink-muted ring-1 ring-border"
                    >
                      {g}
                    </span>
                  ))}
                </div>
              </div>
            )}

            {data.anonymous ? (
              <p className="mt-3 rounded bg-warn/10 px-2 py-1.5 text-[11px] text-warn ring-1 ring-warn/25">
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

export function AppShell({ children }: { children: React.ReactNode }) {
  const { cluster } = useParams<{ cluster: string }>()
  const [params] = useSearchParams()
  const namespace = params.get('namespace') ?? ''
  const { data: discovery, isLoading: discoveryLoading } = useDiscovery(cluster)

  const sections = useMemo(() => buildNav(discovery), [discovery])
  const [filter, setFilter] = useState('')

  const visible = useMemo(() => {
    if (!filter.trim()) return sections
    const needle = filter.trim().toLowerCase()
    return sections
      .map((s) => ({
        ...s,
        items: s.items.filter(
          (i) =>
            i.label.toLowerCase().includes(needle) ||
            i.resource.toLowerCase().includes(needle) ||
            i.group.toLowerCase().includes(needle),
        ),
      }))
      .filter((s) => s.items.length > 0)
  }, [sections, filter])

  return (
    <div className="flex h-full">
      <aside className="flex w-64 shrink-0 flex-col border-r border-border bg-surface">
        <div className="border-b border-border p-3">
          <Link to="/" className="mb-3 flex items-center gap-2">
            <svg viewBox="0 0 32 32" className="size-6" aria-hidden>
              <circle cx="16" cy="16" r="13" fill="none" stroke="currentColor" strokeWidth="3" className="text-accent" />
              <circle cx="16" cy="16" r="4" className="fill-accent" />
            </svg>
            <span className="text-sm font-semibold tracking-tight text-ink">Clusterlens</span>
          </Link>
          <ClusterSwitcher current={cluster} />
        </div>

        {cluster && (
          <>
            <div className="space-y-2 border-b border-border p-3">
              <NamespacePicker cluster={cluster} />
              <input
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder="Filter resources"
                aria-label="Filter resource types"
                className="w-full rounded-md bg-surface-2 px-2 py-1.5 text-sm text-ink ring-1 ring-border placeholder:text-ink-faint"
              />
            </div>

            <nav className="flex-1 overflow-y-auto py-2">
              <NavLink
                to={`/c/${cluster}`}
                end
                className={({ isActive }) =>
                  clsx(
                    'mx-3 mb-2 block rounded px-3 py-1.5 text-sm transition-colors',
                    isActive
                      ? 'bg-accent-soft/50 text-ink'
                      : 'text-ink-muted hover:bg-surface-2 hover:text-ink',
                  )
                }
              >
                Overview
              </NavLink>

              {discoveryLoading && (
                <p className="flex items-center gap-2 px-3 py-2 text-sm text-ink-faint">
                  <Spinner className="size-3.5" /> Reading API surface
                </p>
              )}

              {visible.map((section, i) => (
                <NavGroup
                  key={section.title}
                  section={section}
                  cluster={cluster}
                  namespace={namespace}
                  // Curated sections open; the long tail of API groups does not.
                  defaultOpen={!section.collapsible && i < 3}
                />
              ))}
            </nav>
          </>
        )}
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-12 shrink-0 items-center justify-between gap-3 border-b border-border bg-surface px-4">
          <Breadcrumbs />
          <div className="flex items-center gap-2">
            {namespace && <Badge tone="info">ns: {namespace}</Badge>}
            <UserMenu />
          </div>
        </header>
        <main className="min-h-0 flex-1 overflow-auto">{children}</main>
      </div>
    </div>
  )
}

function Breadcrumbs() {
  const { cluster, resource, name } = useParams()
  if (!cluster) return <span className="text-sm text-ink-faint">Clusterlens</span>

  return (
    <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-1.5 text-sm">
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
