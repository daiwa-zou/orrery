import clsx from 'clsx'
import { Link, useParams } from 'react-router-dom'
import { useCacheStats, useNodeMetrics, useOverview, usePodMetrics } from '../api/hooks'
import type { CountSummary } from '../api/types'
import {
  Age,
  Badge,
  Corners,
  EmptyState,
  ErrorState,
  Spinner,
  StatusBadge,
} from '../components/primitives'
import { cpu, memory, toneFor } from '../lib/format'

function Card({
  title,
  children,
  action,
  flush,
}: {
  title: string
  children: React.ReactNode
  action?: React.ReactNode
  /** Skips the body padding, for cards whose rows carry their own. */
  flush?: boolean
}) {
  return (
    <section className="blueprint bg-surface">
      <Corners />
      <header className="flex items-center justify-between border-b border-border px-3.5 py-[9px]">
        <h2 className="text-[11px] font-semibold tracking-[.1em] text-ink-faint uppercase">
          {title}
        </h2>
        {action}
      </header>
      <div className={flush ? 'px-3.5 py-1.5' : 'p-3.5'}>{children}</div>
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
      <div className="blueprint bg-surface px-3 py-2.5">
        <Corners />
        <p className="text-[11px] tracking-[.06em] text-ink-faint uppercase">{label}</p>
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
    <div className="blueprint bg-surface px-3 py-2.5 transition-colors hover:border-accent-text/45">
      <Corners />
      <p className="text-[11px] tracking-[.06em] text-ink-faint uppercase">{label}</p>
      <p className="mt-1 font-condensed text-[27px] leading-none font-semibold tabular-nums text-ink">
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

/** The smaller inset tiles inside the Workloads card. */
function WorkloadTile({
  label,
  summary,
  to,
}: {
  label: string
  summary?: CountSummary
  to: string
}) {
  const blocked = !summary || summary.forbidden || summary.unavailable
  const body = (
    <div className="border border-border bg-canvas px-2.5 py-2 transition-colors hover:border-accent-text/45">
      <p className="text-[11px] text-ink-faint">{label}</p>
      {blocked ? (
        <p className="mt-0.5 text-xs text-ink-faint">
          {summary?.unavailable ? 'unavailable' : 'not permitted'}
        </p>
      ) : (
        <p className="mt-0.5 font-condensed text-[21px] leading-tight font-semibold tabular-nums text-ink">
          {summary.total.toLocaleString()}
        </p>
      )}
    </div>
  )
  return blocked ? body : <Link to={to}>{body}</Link>
}

/** A horizontal usage bar; warning at 75%, danger at 90%. */
function UsageBar({ label, used, total, format, thin }: {
  label: string
  used: number
  total: number
  format: (n: number) => string
  /** The 5px node-utilisation variant; default is the 7px capacity bar. */
  thin?: boolean
}) {
  const pct = total > 0 ? Math.min(100, (used / total) * 100) : 0
  const tone = pct >= 90 ? 'bg-danger' : pct >= 75 ? 'bg-warn' : 'bg-accent'

  return (
    <div className={clsx(thin && 'flex items-center gap-2')}>
      <div
        className={clsx(
          'flex items-baseline justify-between',
          thin ? 'w-8 shrink-0' : 'mb-1',
        )}
      >
        <span className={clsx(thin ? 'text-[10.5px]' : 'text-xs', 'text-ink-faint')}>{label}</span>
        {!thin && (
          <span className="font-mono text-[11px] tabular-nums text-ink-faint">
            {format(used)} / {format(total)}
          </span>
        )}
      </div>
      <div
        className={clsx('border border-border bg-canvas', thin ? 'h-[5px] flex-1' : 'h-[7px]')}
      >
        <div className={`h-full ${tone} transition-[width] duration-500`} style={{ width: `${pct}%` }} />
      </div>
      {thin && (
        <span className="w-11 shrink-0 text-right font-mono text-[10.5px] text-ink-faint">
          {format(used)}
        </span>
      )}
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

/** How many rows the "Top pods by usage" card shows. */
const TOP_PODS = 6

export function Overview() {
  const { cluster } = useParams<{ cluster: string }>()
  const { data, isLoading, error, refetch } = useOverview(cluster)
  const metrics = useNodeMetrics(cluster)
  const podMetrics = usePodMetrics(cluster)
  const stats = useCacheStats(cluster)

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

  const topPods = [...(podMetrics.data?.pods ?? [])]
    .sort((a, b) => b.usage.cpuMilli - a.usage.cpuMilli)
    .slice(0, TOP_PODS)

  const controlPlane: [string, string][] = [
    ['Version', health.version ?? 'unknown'],
    ['Auth mode', data.cluster.authMode],
    ['API latency', `${health.latencyMs}ms`],
  ]
  if (stats.data) {
    controlPlane.push(
      ['Cached objects', stats.data.totalObjects.toLocaleString()],
      ['Informers', String(stats.data.informers.length)],
    )
  }

  return (
    <div className="mx-auto max-w-[1160px] space-y-4 px-4.5 py-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="font-condensed text-2xl font-semibold text-ink">
            {data.cluster.displayName}
          </h1>
          <p className="mt-0.5 text-[13px] text-ink-faint">
            {health.version ?? 'unknown version'} · API latency {health.latencyMs}ms · auth mode{' '}
            <span className="font-mono">{data.cluster.authMode}</span>
          </p>
        </div>
        <StatusBadge value={health.status} title={health.message} />
      </div>

      {health.message && health.status !== 'healthy' && (
        <p className="bg-warn/10 px-3 py-2 text-[13px] text-warn ring-1 ring-warn/25">
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

      <div className="grid items-start gap-4 lg:grid-cols-3">
        <div className="space-y-4 lg:col-span-2">
          <Card title="Workloads">
            <div className="grid gap-2.5 sm:grid-cols-2 xl:grid-cols-3">
              {Object.entries(WORKLOAD_LABELS).map(([key, meta]) => (
                <WorkloadTile
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
            flush
            action={
              <Link
                to={`/c/${cluster}/events`}
                className="text-xs text-accent-text hover:text-accent-text-hover"
              >
                All events
              </Link>
            }
          >
            {/* Defensive ?? []: an older backend serialises "no warnings" as
                null, and this card must not take the whole page down over it. */}
            {(data.warnings ?? []).length === 0 ? (
              <p className="py-4 text-center text-[13px] text-ink-faint">
                No warning events. That is a good sign.
              </p>
            ) : (
              <ul>
                {(data.warnings ?? []).map((w, i) => (
                  <li
                    key={`${w.object}-${w.reason}-${i}`}
                    className="flex gap-2.5 border-b border-ink/7 py-2 text-[13px] last:border-b-0"
                  >
                    <Badge tone="warn">{w.reason}</Badge>
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-ink">
                        <span className="text-ink-faint">{w.namespace}/</span>
                        {w.object}
                      </p>
                      <p className="text-[12.5px] leading-snug text-ink-muted">{w.message}</p>
                    </div>
                    <div className="shrink-0 text-right text-[11px] text-ink-faint">
                      <Age timestamp={w.lastSeen} />
                      {w.count > 1 && <p>×{w.count}</p>}
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <Card title="Top pods by usage" flush>
            {!podMetrics.data?.available ? (
              <p className="py-4 text-center text-[13px] text-ink-faint">
                {podMetrics.data?.reason ?? 'Metrics are unavailable.'}
              </p>
            ) : topPods.length === 0 ? (
              <p className="py-4 text-center text-[13px] text-ink-faint">No pod metrics yet.</p>
            ) : (
              <ul>
                {topPods.map((p) => (
                  <li key={`${p.namespace}/${p.name}`} className="border-b border-ink/7 last:border-b-0">
                    <Link
                      to={`/c/${cluster}/r/core/v1/pods/${p.namespace}/${p.name}`}
                      className="flex items-center gap-2.5 py-[7px] transition-colors hover:bg-ink/4"
                    >
                      <span className="min-w-0 flex-1 truncate text-[13px] text-ink">
                        <span className="text-ink-faint">{p.namespace}/</span>
                        {p.name}
                      </span>
                      <span className="w-[70px] text-right font-mono text-xs text-ink-muted">
                        {cpu(p.usage.cpuMilli)}
                      </span>
                      <span className="w-20 text-right font-mono text-xs text-ink-muted">
                        {memory(p.usage.memoryMiB)}
                      </span>
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </div>

        <div className="space-y-4">
          <Card title="Capacity">
            {data.capacity && data.requested ? (
              <div className="space-y-3.5">
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
                <p className="text-[11px] leading-normal text-ink-faint">
                  Requests against allocatable capacity — this is what the scheduler packs
                  against.
                </p>
              </div>
            ) : (
              <p className="text-[13px] text-ink-faint">Node capacity is not visible to you.</p>
            )}
          </Card>

          <Card title="Live utilisation">
            {metrics.isLoading ? (
              <div className="flex items-center gap-2 text-[13px] text-ink-faint">
                <Spinner className="size-3.5" /> Reading metrics
              </div>
            ) : !metrics.data?.available ? (
              <p className="text-[13px] text-ink-faint">
                {metrics.data?.reason ?? 'Metrics are unavailable.'}
              </p>
            ) : (
              <div className="space-y-3">
                {(metrics.data.nodes ?? []).map((n) => (
                  <div key={n.name}>
                    <p className="mb-1 truncate font-mono text-[11px] text-ink-muted">{n.name}</p>
                    <div className="space-y-1.5">
                      <UsageBar
                        thin
                        label="CPU"
                        used={n.usage.cpuMilli}
                        total={n.allocatable.cpuMilli}
                        format={cpu}
                      />
                      <UsageBar
                        thin
                        label="Mem"
                        used={n.usage.memoryMiB}
                        total={n.allocatable.memoryMiB}
                        format={memory}
                      />
                    </div>
                  </div>
                ))}
                {!data.nodes.forbidden && (
                  <Link
                    to={`/c/${cluster}/r/core/v1/nodes`}
                    className="inline-block text-xs text-accent-text hover:text-accent-text-hover"
                  >
                    All {data.nodes.total} nodes →
                  </Link>
                )}
              </div>
            )}
          </Card>

          <Card title="Control plane" flush>
            {controlPlane.map(([k, v]) => (
              <div
                key={k}
                className="flex justify-between gap-2.5 border-b border-ink/6 py-1.5 text-[12.5px] last:border-b-0"
              >
                <span className="text-ink-faint">{k}</span>
                <span className="text-right font-mono text-ink-muted">{v}</span>
              </div>
            ))}
          </Card>
        </div>
      </div>
    </div>
  )
}
