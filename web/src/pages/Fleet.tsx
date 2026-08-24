import { Link } from 'react-router-dom'
import type { ClusterSummary, HealthStatus } from '../api/types'
import { Badge } from '../components/primitives'

const TONE: Record<HealthStatus, 'ok' | 'warn' | 'danger' | 'idle'> = {
  healthy: 'ok',
  degraded: 'warn',
  unreachable: 'danger',
  unknown: 'idle',
}

function ClusterCard({ cluster }: { cluster: ClusterSummary }) {
  return (
    <Link
      to={`/c/${cluster.name}`}
      className="block rounded-lg border border-border bg-surface p-4 transition-colors hover:border-border-strong hover:bg-surface-2/50"
    >
      <div className="flex items-center justify-between gap-2">
        <h2 className="truncate text-sm font-semibold text-ink">{cluster.displayName}</h2>
        <Badge tone={TONE[cluster.health.status]}>{cluster.health.status}</Badge>
      </div>

      <dl className="mt-3 space-y-1 text-xs">
        {cluster.available ? (
          <>
            <div className="flex justify-between gap-2">
              <dt className="text-ink-faint">Version</dt>
              <dd className="font-mono text-ink-muted">{cluster.health.version ?? '—'}</dd>
            </div>
            <div className="flex justify-between gap-2">
              <dt className="text-ink-faint">API latency</dt>
              <dd className="tabular-nums text-ink-muted">{cluster.health.latencyMs}ms</dd>
            </div>
            <div className="flex justify-between gap-2">
              <dt className="text-ink-faint">Auth</dt>
              <dd className="font-mono text-ink-muted">{cluster.authMode}</dd>
            </div>
          </>
        ) : (
          <p className="text-danger">{cluster.error ?? 'Unreachable'}</p>
        )}
      </dl>

      {cluster.labels && Object.keys(cluster.labels).length > 0 && (
        <div className="mt-3 flex flex-wrap gap-1">
          {Object.entries(cluster.labels).map(([k, v]) => (
            <span
              key={k}
              className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[10px] text-ink-faint ring-1 ring-border"
            >
              {k}={v}
            </span>
          ))}
        </div>
      )}
    </Link>
  )
}

/** Side-by-side health for every registered cluster — the "is everything up?"
 *  page a multi-cluster console owes its user. */
export function Fleet({ clusters }: { clusters: ClusterSummary[] }) {
  const unhealthy = clusters.filter((c) => c.health.status !== 'healthy').length

  return (
    <div className="mx-auto w-full max-w-5xl px-6 py-10">
      <h1 className="text-lg font-semibold text-ink">Clusters</h1>
      <p className="mt-1 text-sm text-ink-muted">
        {clusters.length} registered
        {unhealthy > 0 ? (
          <> · <span className="text-warn">{unhealthy} not healthy</span></>
        ) : (
          ' · all healthy'
        )}
      </p>

      <div className="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {clusters.map((c) => (
          <ClusterCard key={c.name} cluster={c} />
        ))}
      </div>
    </div>
  )
}
