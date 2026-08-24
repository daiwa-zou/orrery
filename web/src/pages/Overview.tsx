import { Link, useParams } from 'react-router-dom'
import { useNodeMetrics, useOverview } from '../api/hooks'
import type { CountSummary } from '../api/types'
import { Age, Badge, EmptyState, ErrorState, Spinner, StatusBadge } from '../components/primitives'
import { cpu, memory, percent, toneFor } from '../lib/format'

function Card({
  title,
  children,
  action,
}: {
  title: string
  children: React.ReactNode
  action?: React.ReactNode
}) {
  return (
    <section className="rounded-lg bg-surface ring-1 ring-border">
      <header className="flex items-center justify-between border-b border-border px-4 py-2.5">
        <h2 className="text-xs font-semibold tracking-wide text-ink-faint uppercase">{title}</h2>
        {action}
      </header>
      <div className="p-4">{children}</div>
    </section>
  )
}

/**
 * A count tile with its status breakdown. When the viewer lacks permission we
 * say so rather than rendering a confident zero — a zero and a "you can't see
 * this" mean very different things during an incident.
 */
function CountTile({
  label,
  summary,
  to,
}: {
  label: string
  summary?: CountSummary
  to?: string
}) {
  if (!summary || summary.forbidden || summary.unavailable) {
    return (
      <div className="rounded-md bg-surface-2 px-3 py-2.5 ring-1 ring-border">
        <p className="text-xs text-ink-faint">{label}</p>
        {/* "you may not" and "we could not" are different answers; showing
            the wrong one sends people chasing RBAC bugs that do not exist. */}
        <p className="mt-1 text-sm text-ink-faint">
          {summary?.unavailable ? 'temporarily unavailable' : 'not permitted'}
        </p>
      </div>
    )
  }

  const entries = Object.entries(summary.byStatus ?? {})
    .filter(([, n]) => n > 0)
    .sort((a, b) => b[1] - a[1])

  const body = (
    <div className="rounded-md bg-surface-2 px-3 py-2.5 ring-1 ring-border transition-colors hover:bg-border/40">
      <p className="text-xs text-ink-faint">{label}</p>
      <p className="mt-0.5 text-2xl font-semibold tabular-nums text-ink">
        {summary.total.toLocaleString()}
      </p>
      {entries.length > 0 && (
        <div className="mt-2 flex flex-wrap gap-1">
          {entries.slice(0, 4).map(([status, n]) => (
            <Badge key={status} tone={toneFor(status)}>
              {n} {status}
            </Badge>
          ))}
        </div>
      )}
    </div>
  )

  return to ? (
    <Link to={to} className="block">
      {body}
    </Link>
  ) : (
    body
  )
}

/** A horizontal usage bar; colour crosses into warning territory at 80%. */
function UsageBar({ label, used, total, format }: {
  label: string
  used: number
  total: number
  format: (n: number) => string
}) {
  const pct = total > 0 ? Math.min(100, (used / total) * 100) : 0
  const tone = pct >= 90 ? 'bg-danger' : pct >= 80 ? 'bg-warn' : 'bg-accent'

  return (
    <div>
      <div className="mb-1 flex items-baseline justify-between text-xs">
        <span className="text-ink-muted">{label}</span>
        <span className="tabular-nums text-ink-faint">
          {format(used)} / {format(total)} ({percent(pct)})
        </span>
      </div>
      <div className="h-2 overflow-hidden rounded-full bg-surface-2 ring-1 ring-border">
        <div className={`h-full ${tone} transition-[width] duration-500`} style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

const WORKLOAD_LABELS: Record<string, { label: string; path: string }> = {
  deployments: { label: 'Deployments', path: 'apps/v1/deployments' },
  statefulsets: { label: 'StatefulSets', path: 'apps/v1/statefulsets' },
  daemonsets: { label: 'DaemonSets', path: 'apps/v1/daemonsets' },
  jobs: { label: 'Jobs', path: 'batch/v1/jobs' },
  cronjobs: { label: 'CronJobs', path: 'batch/v1/cronjobs' },
  services: { label: 'Services', path: 'core/v1/services' },
  ingresses: { label: 'Ingresses', path: 'networking.k8s.io/v1/ingresses' },
}

export function Overview() {
  const { cluster } = useParams<{ cluster: string }>()
  const { data, isLoading, error, refetch } = useOverview(cluster)
  const metrics = useNodeMetrics(cluster)

  if (isLoading) {
    return (
      <div className="flex items-center justify-center gap-2 py-24 text-ink-faint">
        <Spinner /> Loading cluster
      </div>
    )
  }
  if (error) return <ErrorState error={error} retry={refetch} />
  if (!data) return <EmptyState title="No data" />

  const health = data.cluster.health

  return (
    <div className="mx-auto max-w-7xl space-y-4 p-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-lg font-semibold text-ink">{data.cluster.displayName}</h1>
          <p className="text-sm text-ink-faint">
            {health.version ?? 'unknown version'} · API latency {health.latencyMs}ms · auth mode{' '}
            <span className="font-mono">{data.cluster.authMode}</span>
          </p>
        </div>
        <StatusBadge value={health.status} title={health.message} />
      </div>

      {health.message && health.status !== 'healthy' && (
        <p className="rounded-md bg-warn/10 px-3 py-2 text-sm text-warn ring-1 ring-warn/25">
          {health.message}
        </p>
      )}

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <CountTile label="Nodes" summary={data.nodes} to={`/c/${cluster}/r/core/v1/nodes`} />
        <CountTile label="Namespaces" summary={data.namespaces} to={`/c/${cluster}/r/core/v1/namespaces`} />
        <CountTile label="Pods" summary={data.pods} to={`/c/${cluster}/r/core/v1/pods`} />
        <CountTile
          label="Deployments"
          summary={data.workloads.deployments}
          to={`/c/${cluster}/r/apps/v1/deployments`}
        />
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <div className="lg:col-span-2 space-y-4">
          <Card title="Workloads">
            <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
              {Object.entries(WORKLOAD_LABELS).map(([key, meta]) => (
                <CountTile
                  key={key}
                  label={meta.label}
                  summary={data.workloads[key]}
                  to={`/c/${cluster}/r/${meta.path}`}
                />
              ))}
            </div>
          </Card>

          <Card
            title="Recent warnings"
            action={
              <Link
                to={`/c/${cluster}/events`}
                className="text-xs text-accent hover:underline"
              >
                All events
              </Link>
            }
          >
            {/* Defensive ?? []: an older backend serialises "no warnings" as
                null, and this card must not take the whole page down over it. */}
            {(data.warnings ?? []).length === 0 ? (
              <p className="py-4 text-center text-sm text-ink-faint">
                No warning events. That is a good sign.
              </p>
            ) : (
              <ul className="divide-y divide-border/60">
                {(data.warnings ?? []).map((w, i) => (
                  <li key={`${w.object}-${w.reason}-${i}`} className="flex gap-3 py-2 text-sm">
                    <Badge tone="warn">{w.reason}</Badge>
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-ink">
                        <span className="text-ink-faint">{w.namespace}/</span>
                        {w.object}
                      </p>
                      <p className="text-ink-muted">{w.message}</p>
                    </div>
                    <div className="shrink-0 text-right text-xs text-ink-faint">
                      <Age timestamp={w.lastSeen} />
                      {w.count > 1 && <p>×{w.count}</p>}
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </div>

        <div className="space-y-4">
          <Card title="Capacity">
            {data.capacity && data.requested ? (
              <div className="space-y-4">
                <UsageBar
                  label="CPU requested"
                  used={data.requested.cpuMilli}
                  total={data.capacity.cpuMilli}
                  format={cpu}
                />
                <UsageBar
                  label="Memory requested"
                  used={data.requested.memoryMiB}
                  total={data.capacity.memoryMiB}
                  format={memory}
                />
                <p className="text-xs text-ink-faint">
                  Requests against allocatable capacity — this is what the scheduler packs against.
                </p>
              </div>
            ) : (
              <p className="text-sm text-ink-faint">Node capacity is not visible to you.</p>
            )}
          </Card>

          <Card title="Live utilisation">
            {metrics.isLoading ? (
              <div className="flex items-center gap-2 text-sm text-ink-faint">
                <Spinner className="size-3.5" /> Reading metrics
              </div>
            ) : !metrics.data?.available ? (
              <p className="text-sm text-ink-faint">
                {metrics.data?.reason ?? 'Metrics are unavailable.'}
              </p>
            ) : (
              <ul className="space-y-3">
                {(metrics.data.nodes ?? []).map((n) => (
                  <li key={n.name}>
                    <p className="mb-1 truncate text-xs text-ink-muted">{n.name}</p>
                    <div className="space-y-1.5">
                      <UsageBar
                        label="CPU"
                        used={n.usage.cpuMilli}
                        total={n.allocatable.cpuMilli}
                        format={cpu}
                      />
                      <UsageBar
                        label="Memory"
                        used={n.usage.memoryMiB}
                        total={n.allocatable.memoryMiB}
                        format={memory}
                      />
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </div>
      </div>
    </div>
  )
}
