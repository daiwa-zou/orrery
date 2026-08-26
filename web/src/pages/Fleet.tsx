import clsx from 'clsx'
import { useMemo } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useFleetOverviews } from '../api/hooks'
import type { ClusterSummary, HealthStatus, Overview } from '../api/types'
import { UserMenu } from '../components/AppShell'
import { LogoMark, Wordmark } from '../components/Logo'
import { Badge, Corners } from '../components/primitives'
import { fleetHealth, fleetHealthLabel, needsAttention } from '../lib/fleetHealth'
import { HEALTH_TONE } from '../lib/format'

const DOT_COLOR: Record<HealthStatus, string> = {
  healthy: 'var(--color-ok)',
  degraded: 'var(--color-warn)',
  unreachable: 'var(--color-danger)',
  unknown: 'var(--color-idle)',
}

/** Ring radii and nominal capacities, inner to outer. The last ring absorbs
 *  whatever a large fleet overflows. */
const RINGS: [number, number][] = [
  [58, 3],
  [98, 4],
  [138, 5],
]

const CX = 450
const CY = 160
/** Vertical squash that turns the circles into orbital ellipses. */
const SQUASH = 0.72

function ringOf(index: number): number {
  if (index < RINGS[0][1]) return 0
  if (index < RINGS[0][1] + RINGS[1][1]) return 1
  return 2
}

/** Shortens cluster names so twelve labels fit between the rings. */
function orbitLabel(name: string): string {
  return name.replace(/^prod-/, 'p-').replace(/^staging-/, 's-')
}

/**
 * The fleet as an orbital diagram: one body per cluster on three dashed
 * rings, coloured by the live health probe. Decorative in shape, but every
 * body is a real link and an unhealthy one pulses for attention.
 */
function FleetOrbit({ clusters }: { clusters: ClusterSummary[] }) {
  const navigate = useNavigate()

  // Ring membership first, so each ring spaces its own bodies evenly.
  const counts = [0, 0, 0]
  const ringIndex = clusters.map((_, i) => {
    const ring = Math.min(ringOf(i), 2)
    counts[ring] += 1
    return ring
  })
  const placed: number[] = [0, 0, 0]

  return (
    <div className="blueprint mt-[22px] bg-[#161b22]">
      <Corners />
      <div className="flex items-center justify-between px-3.5 pt-2.5 font-mono text-[10px]">
        <span className="tracking-[.14em] text-accent">FLEET ORBIT — LIVE PROBES</span>
        <span className="text-ink-faint">probe interval 15s · click a body to open</span>
      </div>
      <svg viewBox="0 0 900 320" className="block w-full">
        {RINGS.map(([r], i) => (
          <ellipse
            key={r}
            cx={CX}
            cy={CY}
            rx={r}
            ry={r * SQUASH}
            fill="none"
            stroke={`rgba(231,234,238,${[0.16, 0.13, 0.1][i]})`}
            strokeWidth="1"
            strokeDasharray="4 4"
            className="orbit-ring"
            style={{ '--orbit-duration': `${[6, 9, 13][i]}s` } as React.CSSProperties}
          />
        ))}
        <line x1={CX - 6} y1={CY} x2={CX + 6} y2={CY} stroke="rgba(148,188,227,.7)" strokeWidth="1" />
        <line x1={CX} y1={CY - 6} x2={CX} y2={CY + 6} stroke="rgba(148,188,227,.7)" strokeWidth="1" />
        <circle cx={CX} cy={CY} r="3.4" fill="var(--color-accent-text)" />
        <text x={CX} y={CY + 22} textAnchor="middle" fontSize="9" fill="var(--color-ink-faint)" fontFamily="var(--font-mono)">
          orrery
        </text>

        {clusters.map((c, i) => {
          const ring = ringIndex[i]
          const [radius] = RINGS[ring]
          const angle = (placed[ring]++ / counts[ring]) * Math.PI * 2 + ring * 0.9 + 0.4
          const x = CX + radius * Math.cos(angle)
          const y = CY + radius * SQUASH * Math.sin(angle)
          const color = DOT_COLOR[c.health.status]
          const unhealthy = c.health.status !== 'healthy'

          return (
            <g
              key={c.name}
              role="link"
              tabIndex={0}
              aria-label={`Open ${c.name} (${c.health.status})`}
              className="cursor-pointer outline-none focus-visible:opacity-80"
              onClick={() => navigate(`/c/${c.name}`)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') navigate(`/c/${c.name}`)
              }}
            >
              <title>{`${c.name} — ${c.health.status}`}</title>
              {/* An oversized invisible hit target; 4.5px is a hard click. */}
              <circle cx={x} cy={y} r="10" fill="transparent" />
              <circle
                cx={x}
                cy={y}
                r="4.5"
                fill={color}
                stroke="var(--color-canvas)"
                strokeWidth="1.5"
                className={unhealthy ? 'orbit-pulse' : undefined}
              />
              <text
                x={x}
                y={y + 15}
                textAnchor="middle"
                fontSize="9"
                fill={unhealthy ? color : 'var(--color-ink-faint)'}
                fontFamily="var(--font-mono)"
              >
                {orbitLabel(c.name)}
              </text>
            </g>
          )
        })}
      </svg>
    </div>
  )
}

function ClusterCard({ cluster, overview }: { cluster: ClusterSummary; overview?: Overview }) {
  const workloads = fleetHealth(overview)
  return (
    <Link
      to={`/c/${cluster.name}`}
      className="blueprint block bg-surface p-3.5 transition-colors hover:border-accent-text/45"
    >
      <Corners />
      <div className="flex items-center justify-between gap-2">
        <h2 className="truncate font-condensed text-[16.5px] font-semibold tracking-[.02em] text-ink">
          {cluster.displayName}
        </h2>
        <Badge tone={HEALTH_TONE[cluster.health.status]}>{cluster.health.status}</Badge>
      </div>

      <dl className="mt-2.5 space-y-1 text-xs">
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
          <p className="leading-snug text-danger">{cluster.error ?? 'Unreachable'}</p>
        )}
      </dl>

      {/* The badge above is the probe: whether the API server answered. This
          is what is running on the other side of it, which is the question the
          page looks like it is answering. */}
      {cluster.available && (
        <p
          className={clsx(
            'mt-2.5 border-t border-border pt-2 text-xs',
            needsAttention(workloads) ? 'text-warn' : 'text-ink-faint',
          )}
        >
          {fleetHealthLabel(workloads)}
        </p>
      )}

      {cluster.labels && Object.keys(cluster.labels).length > 0 && (
        <div className="mt-2.5 flex flex-wrap gap-1">
          {Object.entries(cluster.labels).map(([k, v]) => (
            <span
              key={k}
              className="bg-canvas px-1.5 py-px font-mono text-[10px] text-ink-faint ring-1 ring-border"
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
  // Only reachable clusters are asked; an unreachable one already says so on
  // its own card and a request to it would only time out.
  const reachable = useMemo(
    () => clusters.filter((c) => c.available).map((c) => c.name),
    [clusters],
  )
  const results = useFleetOverviews(reachable)
  const overviews = useMemo(() => {
    const byName: Record<string, Overview | undefined> = {}
    reachable.forEach((name, i) => {
      byName[name] = results[i]?.data
    })
    return byName
  }, [reachable, results])

  const healthy = clusters.filter((c) => c.health.status === 'healthy').length
  const degraded = clusters.filter((c) => c.health.status === 'degraded').length
  const unreachable = clusters.filter((c) => c.health.status === 'unreachable').length
  const unhealthy = clusters.length - healthy
  // "all healthy" is a claim about the probe, and it is the first thing anyone
  // reads on this page. Saying it over a cluster whose pods are crash-looping
  // is the difference between a summary and a reassurance.
  const needingAttention = reachable.filter((name) => needsAttention(fleetHealth(overviews[name]))).length

  return (
    <div className="flex h-full flex-col">
      <div className="flex h-12 shrink-0 items-center justify-between border-b border-border bg-surface px-5">
        <div className="flex items-center gap-2.5">
          <LogoMark className="size-[22px]" />
          <Wordmark className="text-base tracking-[.1em]" />
        </div>
        <UserMenu />
      </div>

      <div className="flex-1 overflow-auto">
        <div className="mx-auto w-full max-w-[1180px] px-7 pt-8 pb-15">
          <div className="flex flex-wrap items-baseline justify-between gap-2">
            <div>
              <h1 className="font-condensed text-3xl font-semibold tracking-[.02em] text-ink">
                Clusters
              </h1>
              <p className="mt-1 text-[13.5px] text-ink-muted">
                {clusters.length} registered ·{' '}
                {unhealthy > 0 ? (
                  <span className="text-warn">{unhealthy} not reachable</span>
                ) : needingAttention > 0 ? (
                  <span className="text-warn">
                    {needingAttention} needing attention
                  </span>
                ) : (
                  'all reachable, workloads healthy'
                )}
              </p>
            </div>
            <div className="flex gap-3.5 font-mono text-[11px] text-ink-faint">
              <span className="flex items-center gap-1.5">
                <span className="inline-block size-[7px] rounded-full bg-ok" /> {healthy} healthy
              </span>
              <span className="flex items-center gap-1.5">
                <span className="inline-block size-[7px] rounded-full bg-warn" /> {degraded}{' '}
                degraded
              </span>
              <span className="flex items-center gap-1.5">
                <span className="inline-block size-[7px] rounded-full bg-danger" /> {unreachable}{' '}
                unreachable
              </span>
            </div>
          </div>

          <FleetOrbit clusters={clusters} />

          <div className="mt-[26px] grid grid-cols-[repeat(auto-fill,minmax(250px,1fr))] gap-x-[18px] gap-y-[22px]">
            {clusters.map((c) => (
              <ClusterCard key={c.name} cluster={c} overview={overviews[c.name]} />
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}
