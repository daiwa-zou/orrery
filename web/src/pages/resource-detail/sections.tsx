import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import { api, proxyURL } from '../../api/client'
import type { KubeObject } from '../../api/types'
import {
  Badge,
  Button,
  Corners,
  Eyebrow,
  GatedButton,
  Spinner,
  StatusBadge,
} from '../../components/primitives'
import type { ContainerRow } from '../../lib/containerRows'
import { decodeSecretValue } from '../../lib/secrets'

/**
 * The panels that render one object: its containers, its resolved environment,
 * a ConfigMap or Secret's payload, its status conditions, and the proxy links
 * into a pod or service.
 *
 * They live apart from the page for the ordinary reason — ResourceDetail was
 * eighteen hundred lines and most of them were these — and they are read-only
 * views over an object the page has already fetched, which is what makes them
 * separable at all. Anything that changes the cluster is in ./actions.
 */

export function ContainerSection({
  rows,
  onLogs,
  onDebug,
  debugAllowed,
  debugPending,
  podFinished,
}: {
  rows: ContainerRow[]
  onLogs: (container: string) => void
  onDebug: (container: string) => void
  debugAllowed: boolean
  debugPending: boolean
  /**
   * Whether the pod has reached Succeeded or Failed. The kubelet starts no
   * containers in a pod it has finished with, so offering Debug there is
   * offering a button whose only outcome is a wait that never resolves.
   */
  podFinished?: boolean
}) {
  if (rows.length === 0) return null

  return (
    <section className="blueprint bg-surface p-3.5">
      <Corners />
      <Eyebrow className="mb-2">Containers</Eyebrow>
      <div className="overflow-x-auto">
        <table className="w-full text-[12.5px]">
          <thead>
            <tr className="border-b border-border text-left text-[11px] tracking-[.08em] text-ink-faint uppercase">
              <th className="py-1.5 pr-3 font-semibold">Name</th>
              <th className="py-1.5 pr-3 font-semibold">State</th>
              <th className="py-1.5 pr-3 text-right font-semibold">Ready</th>
              <th className="py-1.5 pr-3 text-right font-semibold">Restarts</th>
              <th className="py-1.5 pr-3 font-semibold">Last exit</th>
              <th className="py-1.5 pr-3 font-semibold">Image</th>
              <th className="py-1.5 font-semibold" />
            </tr>
          </thead>
          <tbody>
            {rows.map((c) => (
              <tr key={`${c.init}-${c.name}`} className="border-b border-ink/8">
                <td className="py-1.5 pr-3 font-medium text-ink">
                  {c.name}
                  {c.init && (
                    <span className="ml-1.5 text-[10px] text-ink-faint uppercase">init</span>
                  )}
                </td>
                <td className="py-1.5 pr-3" title={c.stateDetail}>
                  <StatusBadge value={c.state} />
                </td>
                <td className="py-1.5 pr-3 text-right">
                  {c.ready ? (
                    <Badge tone="ok">yes</Badge>
                  ) : (
                    <span className="text-ink-faint">no</span>
                  )}
                </td>
                <td className="py-1.5 pr-3 text-right font-mono tabular-nums">
                  <span className={c.restarts > 0 ? 'font-semibold text-warn' : undefined}>
                    {c.restarts}
                  </span>
                </td>
                <td className="py-1.5 pr-3 font-mono text-[11.5px] whitespace-nowrap text-ink-muted">
                  {c.lastExit ?? '—'}
                </td>
                <td className="max-w-[18rem] truncate py-1.5 pr-3 font-mono text-[11.5px] text-ink-muted" title={c.image}>
                  {c.image}
                </td>
                <td className="py-1.5 text-right whitespace-nowrap">
                  {!c.init && (
                    <GatedButton
                      size="sm"
                      variant="ghost"
                      // Dimmed with the reason rather than hidden, which is how
                      // every other unavailable action reads here: a missing
                      // button is a question ("where is Debug?"), a dimmed one
                      // with a tooltip is an answer.
                      allowed={debugAllowed && !podFinished}
                      disabled={debugPending}
                      deniedTitle={
                        podFinished
                          ? 'This pod has finished, so the node will not start a debug container in it'
                          : 'Requires patch on pods/ephemeralcontainers'
                      }
                      title={`Start a debug container sharing ${c.name}'s process namespace`}
                      onClick={() => onDebug(c.name)}
                    >
                      Debug
                    </GatedButton>
                  )}
                  <Button size="sm" variant="ghost" onClick={() => onLogs(c.name)}>
                    Logs
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

/**
 * Resolved environment variables, fetched only when opened: resolution reads
 * the referenced ConfigMaps and Secrets under the viewer's own RBAC, and that
 * work (and those reads) should not happen for every pod page view.
 */
export function EnvSection({ cluster, namespace, pod }: { cluster: string; namespace: string; pod: string }) {
  const [open, setOpen] = useState(false)
  const [revealed, setRevealed] = useState<Set<string>>(new Set())

  const env = useQuery({
    queryKey: ['podenv', cluster, namespace, pod],
    queryFn: ({ signal }) => api.podEnv(cluster, namespace, pod, signal),
    enabled: open,
  })

  const toggleReveal = (key: string) =>
    setRevealed((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })

  return (
    <section className="blueprint bg-surface p-3.5">
      <Corners />
      <div className="flex items-center justify-between">
        <Eyebrow>Environment</Eyebrow>
        <Button size="sm" variant="ghost" onClick={() => setOpen((v) => !v)}>
          {open ? 'Hide' : 'Show'}
        </Button>
      </div>

      {open && env.isLoading && (
        <p className="mt-2 flex items-center gap-2 text-[12.5px] text-ink-faint">
          <Spinner className="size-3.5" /> Resolving references
        </p>
      )}
      {open && env.error && (
        <p className="mt-2 text-[12.5px] text-danger">{(env.error as Error).message}</p>
      )}

      {open &&
        env.data?.containers.map((c) => (
          <div key={c.name} className="mt-2.5">
            <h3 className="mb-1 font-mono text-[11.5px] text-ink-muted">
              {c.name}
              {c.init && <span className="ml-1.5 text-[10px] text-ink-faint uppercase">init</span>}
            </h3>
            {c.env.length === 0 ? (
              <p className="text-[12px] text-ink-faint">No environment variables.</p>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-[12px]">
                  <tbody>
                    {c.env.map((e) => {
                      const key = `${c.name}/${e.name}`
                      const hidden = e.sensitive && !revealed.has(key)
                      return (
                        <tr key={key} className="border-b border-ink/8 align-top">
                          <td className="py-1 pr-3 font-mono whitespace-nowrap text-ink">
                            {e.name}
                          </td>
                          <td className="w-full py-1 pr-3 font-mono text-ink-muted">
                            {e.error ? (
                              <span className="text-warn">{e.error}</span>
                            ) : hidden ? (
                              <span className="tracking-widest text-ink-faint">••••••••</span>
                            ) : (
                              <span className="break-all">{e.value}</span>
                            )}
                          </td>
                          <td className="py-1 pr-3 text-right whitespace-nowrap text-[11px] text-ink-faint">
                            {e.source === 'literal' ? '' : (e.from ?? e.source)}
                          </td>
                          <td className="py-1 text-right">
                            {e.sensitive && !e.error && (
                              <Button size="sm" variant="ghost" onClick={() => toggleReveal(key)}>
                                {hidden ? 'Reveal' : 'Hide'}
                              </Button>
                            )}
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        ))}
    </section>
  )
}

/**
 * The data of a Secret or ConfigMap as a key table. Secret values stay masked
 * until asked for, key by key — reading the object needed RBAC `get` on it,
 * but a value on screen should still be a deliberate act, not a side effect
 * of opening the page.
 */
export function DataSection({ obj }: { obj: KubeObject }) {
  const [revealed, setRevealed] = useState<Set<string>>(new Set())
  const isSecret = obj.kind === 'Secret'

  const data = (obj as { data?: Record<string, string> }).data ?? {}
  const binaryData = (obj as { binaryData?: Record<string, string> }).binaryData ?? {}
  const keys = [...Object.keys(data), ...Object.keys(binaryData)].sort()
  if (keys.length === 0) return null

  const toggle = (k: string) =>
    setRevealed((prev) => {
      const next = new Set(prev)
      if (next.has(k)) next.delete(k)
      else next.add(k)
      return next
    })

  return (
    <section className="blueprint bg-surface p-3.5">
      <Corners />
      <Eyebrow className="mb-2">Data</Eyebrow>
      <table className="w-full text-[12px]">
        <tbody>
          {keys.map((k) => {
            const fromBinary = k in binaryData
            const decoded = isSecret || fromBinary ? decodeSecretValue(data[k] ?? binaryData[k]) : undefined
            const value = decoded ? (decoded.text ?? '') : (data[k] ?? '')
            const binary = decoded?.binary ?? false
            const size = decoded ? decoded.size : value.length
            const hidden = isSecret && !revealed.has(k)
            return (
              <tr key={k} className="border-b border-ink/8 align-top">
                <td className="py-1 pr-3 font-mono whitespace-nowrap text-ink">{k}</td>
                <td className="w-full py-1 pr-3 font-mono text-ink-muted">
                  {binary ? (
                    <Badge tone="idle">binary · {size} bytes</Badge>
                  ) : hidden ? (
                    <span className="tracking-widest text-ink-faint">••••••••</span>
                  ) : (
                    <pre className="max-h-48 overflow-auto break-all whitespace-pre-wrap">{value}</pre>
                  )}
                </td>
                <td className="py-1 text-right">
                  {isSecret && !binary && (
                    <Button size="sm" variant="ghost" onClick={() => toggle(k)}>
                      {hidden ? 'Reveal' : 'Hide'}
                    </Button>
                  )}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </section>
  )
}

export function StatusSection({ status }: { status?: Record<string, unknown> }) {
  if (!status || Object.keys(status).length === 0) return null

  const conditions = Array.isArray(status.conditions)
    ? (status.conditions as Record<string, string>[])
    : []
  const rest = Object.fromEntries(Object.entries(status).filter(([k]) => k !== 'conditions'))

  return (
    <section className="blueprint bg-surface p-3.5">
      <Corners />
      <Eyebrow className="mb-2">Status</Eyebrow>

      {conditions.length > 0 && (
        <table className="mb-4 w-full text-[12.5px]">
          <thead>
            <tr className="border-b border-border text-left text-[11px] tracking-[.08em] text-ink-faint uppercase">
              <th className="py-1.5 pr-3 font-semibold">Condition</th>
              <th className="py-1.5 pr-3 font-semibold">Status</th>
              <th className="py-1.5 pr-3 font-semibold">Reason</th>
              <th className="py-1.5 font-semibold">Message</th>
            </tr>
          </thead>
          <tbody>
            {conditions.map((c, i) => (
              <tr key={`${c.type}-${i}`} className="border-b border-ink/8">
                <td className="py-1.5 pr-3 whitespace-nowrap text-ink">{c.type}</td>
                <td className="py-1.5 pr-3">
                  <Badge
                    tone={c.status === 'True' ? 'ok' : c.status === 'False' ? 'danger' : 'idle'}
                  >
                    {c.status}
                  </Badge>
                </td>
                <td className="py-1.5 pr-3 text-ink-muted">{c.reason ?? '—'}</td>
                <td className="py-1.5 text-ink-muted">{c.message ?? '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {Object.keys(rest).length > 0 && (
        <pre className="max-h-80 overflow-auto bg-code p-3 font-mono text-xs text-ink-muted">
          {JSON.stringify(rest, null, 2)}
        </pre>
      )}
    </section>
  )
}

/** kubectl rollout pause / resume. */
export function ProxySection({
  cluster,
  kind,
  namespace,
  name,
  obj,
  allowed,
  enabled,
}: {
  cluster: string
  kind: string
  namespace: string
  name: string
  obj: KubeObject
  allowed: boolean
  /** The server serves the proxy route at all. */
  enabled: boolean
}) {
  const ports: { label: string; port: number }[] = []
  if (kind === 'Service') {
    const spec = obj.spec as { ports?: { name?: string; port: number }[] }
    for (const p of spec?.ports ?? []) {
      ports.push({ label: p.name ? `${p.name} (${p.port})` : String(p.port), port: p.port })
    }
  } else {
    const spec = obj.spec as { containers?: { name: string; ports?: { containerPort: number; name?: string }[] }[] }
    for (const c of spec?.containers ?? []) {
      for (const p of c.ports ?? []) {
        ports.push({
          label: `${c.name}:${p.name ?? p.containerPort}`,
          port: p.containerPort,
        })
      }
    }
  }
  // Nothing to offer when the server is not serving the route: an Open button
  // that 404s is worse than no button.
  if (!enabled) return null
  if (ports.length === 0) return null

  const ptype = kind === 'Service' ? 'services' : 'pods'

  return (
    <section className="blueprint bg-surface p-3.5">
      <Corners />
      <Eyebrow className="mb-2">HTTP proxy</Eyebrow>
      <p className="mb-2 text-xs text-ink-muted">
        Opens the port through the API server&apos;s proxy — port-forward for HTTP, read-only,
        under your own identity.
      </p>
      <div className="flex flex-wrap gap-2">
        {ports.map((p) =>
          allowed ? (
            <a
              key={`${p.label}-${p.port}`}
              href={proxyURL(cluster, namespace, ptype, name, p.port)}
              target="_blank"
              rel="noreferrer"
              className="bg-canvas px-2 py-1 font-mono text-xs text-accent-text ring-1 ring-border hover:text-accent-text-hover hover:ring-border-strong"
            >
              {p.label} ↗
            </a>
          ) : (
            <span
              key={`${p.label}-${p.port}`}
              title={`Requires get on ${ptype}/proxy`}
              className="cursor-not-allowed bg-canvas px-2 py-1 font-mono text-xs text-ink-faint opacity-40 ring-1 ring-border"
            >
              {p.label} ↗
            </span>
          ),
        )}
      </div>
    </section>
  )
}

/** kubectl rollout history + undo, as a modal. */
