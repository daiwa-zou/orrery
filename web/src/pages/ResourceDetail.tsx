import clsx from 'clsx'
import { lazy, Suspense, useCallback, useMemo, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, apiGroup, proxyURL, type ResourceRef } from '../api/client'
import { useAccess, useEvents, useLiveResource, useMe } from '../api/hooks'
import type { AccessCheck, KubeObject, ObjectRef } from '../api/types'
import { DataTable } from '../components/DataTable'
import { LogViewer } from '../components/LogViewer'
import { WorkloadLogs } from '../components/WorkloadLogs'
import { containerNamesOf, defaultContainerOf } from '../lib/podTemplate'
import { podSelectorOf } from '../lib/selector'
import { MetadataEditor } from '../components/MetadataEditor'
import { RelatedSection } from '../components/RelatedSection'
import {
  Age,
  Badge,
  Button,
  Corners,
  ErrorState,
  Field,
  GatedButton,
  Modal,
  Loading,
  Spinner,
  StatusBadge,
} from '../components/primitives'
import { useToast } from '../components/Toast'
import { kindToResource, RESTARTABLE_KINDS, splitApiVersion } from '../lib/format'
import { decodeSecretValue } from '../lib/secrets'

// CodeMirror and xterm.js are ~770K of the bundle between them and are only
// reachable from these two tabs. Loading them lazily keeps them out of the
// initial download for the majority of sessions that never open either.
const Terminal = lazy(() =>
  import('../components/Terminal').then((m) => ({ default: m.Terminal })),
)
const YamlEditor = lazy(() =>
  import('../components/YamlEditor').then((m) => ({ default: m.YamlEditor })),
)

type Tab = 'overview' | 'yaml' | 'events' | 'logs' | 'terminal'

const SCALABLE = new Set(['Deployment', 'StatefulSet', 'ReplicaSet', 'ReplicationController'])

interface ContainerRow {
  name: string
  image: string
  ready: boolean
  restarts: number
  state: string
  stateDetail?: string
  lastExit?: string
  init: boolean
}

/**
 * Flattens containerStatuses into the table that answers "why is this pod
 * restarting?" — the state, the restart count and the last exit code are the
 * three facts otherwise buried in the status JSON.
 */
function containerRows(obj?: KubeObject): ContainerRow[] {
  if (!obj) return []
  const status = obj.status as {
    containerStatuses?: Record<string, unknown>[]
    initContainerStatuses?: Record<string, unknown>[]
  }

  const toRow = (s: Record<string, unknown>, init: boolean): ContainerRow => {
    const state = (s.state ?? {}) as Record<string, Record<string, unknown> | undefined>
    let name = 'Unknown'
    let detail: string | undefined
    if (state.running) {
      name = 'Running'
    } else if (state.waiting) {
      name = String(state.waiting.reason ?? 'Waiting')
      detail = state.waiting.message as string | undefined
    } else if (state.terminated) {
      name = String(state.terminated.reason ?? 'Terminated')
      detail = state.terminated.message as string | undefined
    }

    const last = (s.lastState as Record<string, Record<string, unknown>> | undefined)?.terminated
    let lastExit: string | undefined
    if (last) {
      lastExit = `exit ${last.exitCode}`
      if (last.reason) lastExit += ` (${last.reason})`
    }

    return {
      name: String(s.name ?? ''),
      image: String(s.image ?? ''),
      ready: s.ready === true,
      restarts: Number(s.restartCount ?? 0),
      state: name,
      stateDetail: detail,
      lastExit,
      init,
    }
  }

  return [
    ...(status?.initContainerStatuses ?? []).map((s) => toRow(s, true)),
    ...(status?.containerStatuses ?? []).map((s) => toRow(s, false)),
  ]
}

function ContainerSection({
  rows,
  onLogs,
  onDebug,
  debugAllowed,
  debugPending,
}: {
  rows: ContainerRow[]
  onLogs: (container: string) => void
  onDebug: (container: string) => void
  debugAllowed: boolean
  debugPending: boolean
}) {
  if (rows.length === 0) return null

  return (
    <section className="blueprint bg-surface p-3.5">
      <Corners />
      <h2 className="mb-1.5 text-[11px] font-semibold tracking-[.1em] text-ink-faint uppercase">
        Containers
      </h2>
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
                      allowed={debugAllowed}
                      disabled={debugPending}
                      deniedTitle="Requires patch on pods/ephemeralcontainers"
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
function EnvSection({ cluster, namespace, pod }: { cluster: string; namespace: string; pod: string }) {
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
        <h2 className="text-[11px] font-semibold tracking-[.1em] text-ink-faint uppercase">
          Environment
        </h2>
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
function DataSection({ obj }: { obj: KubeObject }) {
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
      <h2 className="mb-1.5 text-[11px] font-semibold tracking-[.1em] text-ink-faint uppercase">
        Data
      </h2>
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

function TabButton({
  active,
  onClick,
  disabled,
  deniedTitle,
  children,
}: {
  active: boolean
  onClick: () => void
  /** Dimmed and inert, with deniedTitle explaining the missing permission. */
  disabled?: boolean
  deniedTitle?: string
  children: React.ReactNode
}) {
  return (
    <span className="inline-flex" title={disabled ? deniedTitle : undefined}>
      <button
        onClick={onClick}
        disabled={disabled}
        className={clsx(
          'border-b-2 px-3 py-[7px] text-[13.5px] transition-colors',
          disabled && 'cursor-not-allowed opacity-40',
          active
            ? 'border-accent text-ink'
            : 'border-transparent text-ink-muted',
          !disabled && !active && 'hover:text-ink',
        )}
      >
        {children}
      </button>
    </span>
  )
}

export function ResourceDetail() {
  const params = useParams()
  // Remount per object: tab choice, modal state and editor drafts belong to
  // one object, and carrying them across navigation leaves a Pod's "logs" tab
  // selected on a ReplicaSet that has no such tab — a blank page.
  return (
    <ResourceDetailInner
      key={`${params.cluster}/${params.group}/${params.version}/${params.resource}/${params.namespace}/${params.name}`}
    />
  )
}

function ResourceDetailInner() {
  const { cluster, group, version, resource, namespace, name } = useParams<{
    cluster: string
    group: string
    version: string
    resource: string
    namespace: string
    name: string
  }>()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()
  // The proxy is switchable server-side; when it is off the route does not
  // exist, so the section is hidden rather than left to fail on click. A
  // server that does not report the flag at all is treated as having it on.
  const me = useMe()
  const proxyEnabled = me.data?.features?.proxy !== false

  const ns = namespace === '_' ? undefined : namespace
  const ref: ResourceRef = {
    cluster: cluster!,
    group: apiGroup(group!),
    version: version!,
    resource: resource!,
    namespace: ns,
    name,
  }

  const [tab, setTab] = useState<Tab>('overview')
  const [scaleOpen, setScaleOpen] = useState(false)
  const [scaleBusy, setScaleBusy] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [replicas, setReplicas] = useState(0)
  const [yamlDirty, setYamlDirty] = useState(false)
  const [logContainer, setLogContainer] = useState<string>()
  // Which container the terminal attaches to. Set when a debug container is
  // started, so the new shell opens where it was created rather than in the
  // pod's first container.
  const [termContainer, setTermContainer] = useState<string>()
  const [debugPending, setDebugPending] = useState(false)

  const switchTab = (next: Tab) => {
    if (
      next !== tab &&
      tab === 'yaml' &&
      yamlDirty &&
      !window.confirm('Discard your unsaved YAML changes?')
    ) {
      return
    }
    setTab(next)
  }

  const { data: obj, isLoading, error, refetch } = useLiveResource(ref)

  const yamlQuery = useQuery({
    queryKey: ['yaml', ref.cluster, ref.group, ref.version, ref.resource, ref.namespace, ref.name],
    queryFn: ({ signal }) => api.getYaml(ref, signal),
    enabled: tab === 'yaml',
  })

  const events = useEvents(cluster, {
    namespace: ns,
    involvedName: name,
    involvedKind: obj?.kind,
    involvedUID: obj?.metadata.uid,
  })

  const kind = obj?.kind ?? ''
  const isPod = kind === 'Pod'
  const isNode = kind === 'Node'
  const isDeployment = kind === 'Deployment'
  const isCronJob = kind === 'CronJob'
  const containers = containerNamesOf(obj)
  // The label selector this workload uses to own its pods. Computed here
  // rather than beside its first use because the access checks below need to
  // know whether there is a pod set to ask about.
  const podSelector = useMemo(() => podSelectorOf(kind, obj?.spec), [obj, kind])

  // Every action asks about the exact verb and resource the backend will
  // check, so a button never appears that the server would refuse. Keyed, not
  // positional: positional indexing is how a new check silently gates the
  // wrong button.
  const checkList = useMemo(() => {
    if (!obj) return []
    const self = { group: ref.group, version: ref.version, resource: ref.resource, namespace: ns, name }
    const pods = { group: '', version: 'v1', resource: 'pods', namespace: ns, name }
    const list: { key: string; check: AccessCheck }[] = [
      { key: 'update', check: { ...self, verb: 'update' } },
      { key: 'patch', check: { ...self, verb: 'patch' } },
      { key: 'delete', check: { ...self, verb: 'delete' } },
      { key: 'scale', check: { ...self, verb: 'patch', subresource: 'scale' } },
    ]
    if (isPod) {
      list.push(
        { key: 'exec', check: { ...pods, verb: 'create', subresource: 'exec' } },
        { key: 'logs', check: { ...pods, verb: 'get', subresource: 'log' } },
        { key: 'evict', check: { ...pods, verb: 'create', subresource: 'eviction' } },
        { key: 'proxy', check: { ...pods, verb: 'get', subresource: 'proxy' } },
        {
          key: 'debug',
          check: { ...pods, verb: 'patch', subresource: 'ephemeralcontainers' },
        },
      )
    }
    if (!isPod && podSelector) {
      // A merged feed reads many pods, so the question is namespace-wide
      // rather than about one name — which is exactly what the server checks
      // before it opens the stream, since it refuses a caller who may read
      // some of a workload's pods but not others.
      list.push({
        key: 'logs',
        check: { verb: 'get', group: '', version: 'v1', resource: 'pods', subresource: 'log', namespace: ns },
      })
    }
    if (kind === 'Service') {
      list.push({
        key: 'proxy',
        check: { verb: 'get', group: '', version: 'v1', resource: 'services', subresource: 'proxy', namespace: ns, name },
      })
    }
    if (isCronJob) {
      list.push({
        key: 'createJob',
        check: { verb: 'create', group: 'batch', version: 'v1', resource: 'jobs', namespace: ns },
      })
    }
    if (isDeployment) {
      list.push({
        key: 'listReplicaSets',
        check: { verb: 'list', group: 'apps', version: 'v1', resource: 'replicasets', namespace: ns },
      })
    }
    return list
  }, [obj, ref.group, ref.version, ref.resource, ns, name, isPod, isCronJob, isDeployment, kind, podSelector])

  const access = useAccess(cluster, checkList.map((c) => c.check))
  const may = useCallback(
    (key: string) => {
      const i = checkList.findIndex((c) => c.key === key)
      return i >= 0 ? (access.data?.[i]?.allowed ?? false) : false
    },
    [checkList, access.data],
  )
  const mayUpdate = may('update')
  const mayDelete = may('delete')
  const mayScale = may('scale')
  const mayExec = may('exec')
  const mayPatch = may('patch')

  // Memoised because the `?? []` default is a fresh array on every render,
  // which would defeat every memo downstream of it.
  const owners = useMemo(() => obj?.metadata.ownerReferences ?? [], [obj])

  // Shown while the neighbourhood walk is in flight, and kept if it fails, so
  // this page is never worse at naming an owner than it was before. The
  // resource is guessed from the kind here — which is exactly what every owner
  // link did before the server started resolving it through discovery.
  const fallbackOwners = useMemo<ObjectRef[]>(
    () =>
      owners.map((o) => {
        const { group, version } = splitApiVersion(o.apiVersion)
        return {
          relation: 'owner',
          depth: 1,
          kind: o.kind,
          name: o.name,
          uid: o.uid,
          group,
          version: version || 'v1',
          resource: kindToResource(o.kind),
          namespace: ns,
        }
      }),
    [owners, ns],
  )

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['object'] })
    qc.invalidateQueries({ queryKey: ['yaml'] })
    qc.invalidateQueries({ queryKey: ['list'] })
  }

  const doScale = async () => {
    setScaleBusy(true)
    try {
      await api.scale(cluster!, { group: ref.group, version: ref.version, resource: ref.resource, namespace: ns, name }, replicas)
      toast.push({ tone: 'ok', title: `Scaled ${name} to ${replicas}` })
      invalidate()
    } catch (e) {
      toast.push({ tone: 'danger', title: 'Scale failed', description: (e as Error).message })
    } finally {
      setScaleBusy(false)
      setScaleOpen(false)
    }
  }

  const doRestart = async () => {
    try {
      await api.restart(cluster!, { group: ref.group, version: ref.version, resource: ref.resource, namespace: ns, name })
      toast.push({ tone: 'ok', title: `Rolling restart of ${name} started` })
      invalidate()
    } catch (e) {
      toast.push({ tone: 'danger', title: 'Restart failed', description: (e as Error).message })
    }
  }

  const doDelete = async () => {
    try {
      await api.remove(ref)
      toast.push({ tone: 'ok', title: `Deleting ${name}` })
      navigate(`/c/${cluster}/r/${group}/${version}/${resource}${ns ? `?namespace=${ns}` : ''}`)
    } catch (e) {
      toast.push({ tone: 'danger', title: 'Delete failed', description: (e as Error).message })
    } finally {
      setDeleteOpen(false)
    }
  }

  const doCordon = async (unschedulable: boolean) => {
    try {
      await api.cordon(cluster!, name!, unschedulable)
      toast.push({ tone: 'ok', title: unschedulable ? `${name} cordoned` : `${name} uncordoned` })
      invalidate()
    } catch (e) {
      toast.push({ tone: 'danger', title: 'Cordon failed', description: (e as Error).message })
    }
  }

  const saveMetadata = (field: 'labels' | 'annotations') => async (
    changes: Record<string, string | null>,
  ) => {
    try {
      await api.patch(ref, { metadata: { [field]: changes } })
      toast.push({
        tone: 'ok',
        title: `${field === 'labels' ? 'Labels' : 'Annotations'} of ${name} updated`,
      })
      invalidate()
    } catch (e) {
      toast.push({ tone: 'danger', title: 'Update failed', description: (e as Error).message })
      throw e // Keeps the editor open with the draft intact.
    }
  }

  /**
   * Attach an ephemeral debug container and drop straight into it.
   *
   * Confirmed first because it cannot be undone: Kubernetes has no API for
   * removing an ephemeral container, so it stays on the pod until the pod is
   * replaced. That is a surprise worth spending a dialog on.
   */
  const startDebug = async (target: string) => {
    if (!cluster || !ns || !name) return
    const ok = window.confirm(
      `Start a debug container alongside "${target}"?\n\n` +
        'It shares that container\'s process namespace, so you can inspect its ' +
        'processes and filesystem even if it has no shell of its own.\n\n' +
        'Ephemeral containers cannot be removed — it will stay on this pod until ' +
        'the pod is replaced.',
    )
    if (!ok) return

    setDebugPending(true)
    try {
      const res = await api.debug(cluster, ns, name, target)
      setTermContainer(res.container)
      setTab('terminal')
      toast.push({
        tone: 'ok',
        title: `Debug container started`,
        description: `${res.container} (${res.image}) — it may take a moment to pull and start.`,
      })
      refetch()
    } catch (e) {
      toast.push({
        tone: 'danger',
        title: 'Could not start a debug container',
        description: (e as Error).message,
      })
    } finally {
      setDebugPending(false)
    }
  }

  const saveYaml = async (next: string) => {
    await api.replace(ref, next)
    toast.push({ tone: 'ok', title: `${name} updated` })
    invalidate()
  }

  const currentReplicas = useMemo(() => {
    const spec = obj?.spec as { replicas?: number } | undefined
    return spec?.replicas ?? 0
  }, [obj])

  if (isLoading) {
    return (
      <Loading label={`Loading ${name}`} />
    )
  }
  if (error) return <ErrorState error={error} retry={refetch} />
  if (!obj) return null

  const status = obj.status as Record<string, unknown> | undefined
  // For pods, mirror what the list column computes server-side: a container
  // stuck waiting or terminated abnormally names the badge, so the header
  // never says "Pending" while the container table says ImagePullBackOff.
  let phase = typeof status?.phase === 'string' ? status.phase : undefined
  if (isPod) {
    const reason = typeof status?.reason === 'string' ? status.reason : undefined
    phase = reason ?? phase
    for (const c of containerRows(obj)) {
      if (c.init) continue
      if (c.state !== 'Running' && c.state !== 'Completed' && c.state !== 'Unknown') {
        phase = c.state
      }
    }
  }

  return (
    <div className="flex h-full flex-col">
      <header className="border-b border-border bg-surface px-4 py-3">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="max-w-[560px] truncate font-condensed text-lg font-semibold text-ink">
                {obj.metadata.name}
              </h1>
              {phase && <StatusBadge value={phase} />}
              {obj.metadata.deletionTimestamp && <Badge tone="warn">terminating</Badge>}
            </div>
            <p className="mt-0.5 font-mono text-[11.5px] text-ink-faint">
              {obj.apiVersion} · {obj.kind}
              {ns && ` · ${ns}`}
            </p>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            {podSelector && ns && (
              <Button
                size="sm"
                title={`Pods matching ${podSelector}`}
                onClick={() =>
                  navigate(
                    `/c/${cluster}/r/core/v1/pods?namespace=${ns}&labelSelector=${encodeURIComponent(podSelector)}`,
                  )
                }
              >
                View pods
              </Button>
            )}
            {isNode && (
              <Button
                size="sm"
                title="Pods scheduled on this node"
                onClick={() =>
                  navigate(
                    `/c/${cluster}/r/core/v1/pods?fieldSelector=${encodeURIComponent(
                      `spec.nodeName=${name}`,
                    )}`,
                  )
                }
              >
                View pods
              </Button>
            )}
            {SCALABLE.has(kind) && (
              <GatedButton
                allowed={mayScale}
                deniedTitle={`Requires patch on ${resource}/scale`}
                size="sm"
                onClick={() => {
                  setReplicas(currentReplicas)
                  setScaleOpen(true)
                }}
              >
                Scale
              </GatedButton>
            )}
            {RESTARTABLE_KINDS.has(kind) && (
              <GatedButton
                allowed={mayPatch}
                deniedTitle={`Requires patch on ${resource}`}
                size="sm"
                onClick={doRestart}
                title="Stamps the pod template to trigger a rollout"
              >
                Restart
              </GatedButton>
            )}
            {isDeployment && ns && (
              <>
                <RollbackButton
                  cluster={cluster!}
                  namespace={ns}
                  name={name!}
                  allowed={mayPatch && may('listReplicaSets')}
                  onDone={invalidate}
                />
                <PauseButton
                  paused={(obj.spec as { paused?: boolean })?.paused === true}
                  objRef={ref}
                  allowed={mayPatch}
                  onDone={invalidate}
                />
              </>
            )}
            {isCronJob && ns && (
              <CronJobActions
                cluster={cluster!}
                namespace={ns}
                name={name!}
                suspended={(obj.spec as { suspend?: boolean })?.suspend === true}
                canRun={may('createJob')}
                canSuspend={mayPatch}
                onDone={invalidate}
              />
            )}
            {isPod && ns && (
              <EvictButton cluster={cluster!} namespace={ns} pod={name!} allowed={may('evict')} />
            )}
            {isNode && (
              <>
                <GatedButton
                  allowed={mayPatch}
                  deniedTitle="Requires patch on nodes"
                  size="sm"
                  onClick={() => doCordon(!(obj.spec as { unschedulable?: boolean })?.unschedulable)}
                >
                  {(obj.spec as { unschedulable?: boolean })?.unschedulable ? 'Uncordon' : 'Cordon'}
                </GatedButton>
                <DrainButton cluster={cluster!} node={name!} allowed={mayPatch} onDone={invalidate} />
                <TaintsButton
                  objRef={ref}
                  taints={((obj.spec as { taints?: Taint[] })?.taints ?? [])}
                  allowed={mayPatch}
                  onDone={invalidate}
                />
              </>
            )}
            <GatedButton
              allowed={mayDelete}
              deniedTitle={`Requires delete on ${resource}`}
              size="sm"
              variant="danger"
              onClick={() => setDeleteOpen(true)}
            >
              Delete
            </GatedButton>
          </div>
        </div>

        <nav className="-mb-3 mt-2 flex gap-1 border-b border-transparent">
          <TabButton active={tab === 'overview'} onClick={() => switchTab('overview')}>
            Overview
          </TabButton>
          <TabButton active={tab === 'yaml'} onClick={() => switchTab('yaml')}>
            YAML
          </TabButton>
          <TabButton active={tab === 'events'} onClick={() => switchTab('events')}>
            Events
            {events.data && events.data.total > 0 && (
              <span className="ml-1.5 text-xs text-ink-faint">{events.data.total}</span>
            )}
          </TabButton>
          {(isPod || podSelector) && (
            <TabButton
              active={tab === 'logs'}
              disabled={!may('logs')}
              deniedTitle="Requires get on pods/log"
              onClick={() => switchTab('logs')}
            >
              Logs
            </TabButton>
          )}
          {isPod && (
            <TabButton
              active={tab === 'terminal'}
              disabled={!mayExec}
              deniedTitle="Requires create on pods/exec"
              onClick={() => switchTab('terminal')}
            >
              Terminal
            </TabButton>
          )}
        </nav>
      </header>

      <div className="min-h-0 flex-1 overflow-auto">
        {tab === 'overview' && (
          <div className="mx-auto max-w-[980px] space-y-4 p-4">
            <section className="blueprint bg-surface p-3.5">
              <Corners />
              <h2 className="mb-2 text-[11px] font-semibold tracking-[.1em] text-ink-faint uppercase">
                Metadata
              </h2>
              <dl>
                <Field label="Name">{obj.metadata.name}</Field>
                {ns && <Field label="Namespace">{ns}</Field>}
                <Field label="Created">
                  <Age timestamp={obj.metadata.creationTimestamp} /> ago
                  <span className="ml-2 text-ink-faint">{obj.metadata.creationTimestamp}</span>
                </Field>
                <Field label="UID">
                  <span className="font-mono text-xs">{obj.metadata.uid}</span>
                </Field>
                <Field label="Labels">
                  <MetadataEditor
                    field="labels"
                    values={obj.metadata.labels}
                    canEdit={mayPatch}
                    onSave={saveMetadata('labels')}
                  />
                </Field>
                <Field label="Annotations">
                  <MetadataEditor
                    field="annotations"
                    values={obj.metadata.annotations}
                    canEdit={mayPatch}
                    onSave={saveMetadata('annotations')}
                  />
                </Field>
              </dl>
            </section>

            <RelatedSection
              subject={ref}
              enabled={tab === 'overview'}
              fallbackOwners={fallbackOwners}
            />

            {isPod && (
              <ContainerSection
                rows={containerRows(obj)}
                debugAllowed={may('debug')}
                debugPending={debugPending}
                onLogs={(container) => {
                  setLogContainer(container)
                  setTab('logs')
                }}
                onDebug={startDebug}
              />
            )}

            {isPod && ns && <EnvSection cluster={cluster!} namespace={ns} pod={name!} />}

            {(kind === 'Secret' || kind === 'ConfigMap') && <DataSection obj={obj} />}

            {(isPod || kind === 'Service') && ns && (
              <ProxySection
                allowed={may('proxy')}
                enabled={proxyEnabled} cluster={cluster!} kind={kind} namespace={ns} name={name!} obj={obj} />
            )}

            <StatusSection status={status} />
          </div>
        )}

        {tab === 'yaml' && (
          <div className="h-full">
            {yamlQuery.isLoading ? (
              <Loading label="Loading manifest" />
            ) : yamlQuery.error ? (
              <ErrorState error={yamlQuery.error} retry={yamlQuery.refetch} />
            ) : (
              <Suspense fallback={<Loading label="Loading editor" />}>
                <YamlEditor
                  value={yamlQuery.data ?? ''}
                  readOnly={!mayUpdate}
                  onSave={mayUpdate ? saveYaml : undefined}
                  onCheck={mayUpdate ? (next) => api.replaceDryRun(ref, next) : undefined}
                  onDirtyChange={setYamlDirty}
                  notice={
                    mayUpdate
                      ? 'Applying replaces the object. Server-managed fields are stripped from this view.'
                      : 'You do not have permission to update this object.'
                  }
                />
              </Suspense>
            )}
          </div>
        )}

        {tab === 'events' && (
          <div className="p-4">
            {events.isLoading ? (
              <Loading className="py-16" label="Loading events" />
            ) : (
              <div className="blueprint bg-surface">
                <Corners />
                <DataTable
                  columns={events.data?.columns ?? []}
                  rows={events.data?.items ?? []}
                  emptyTitle="No events"
                  emptyDescription="Kubernetes keeps events for about an hour by default, so an older incident may simply have aged out."
                />
              </div>
            )}
          </div>
        )}

        {tab === 'logs' && may('logs') && isPod && (
          <LogViewer
            cluster={cluster!}
            namespace={ns!}
            pod={name!}
            containers={containers}
            initialContainer={logContainer ?? defaultContainerOf(obj)}
            lastExits={Object.fromEntries(
              containerRows(obj)
                .filter((c) => c.lastExit)
                .map((c) => [c.name, c.lastExit!]),
            )}
          />
        )}

        {tab === 'logs' && may('logs') && !isPod && podSelector && ns && (
          <WorkloadLogs
            cluster={cluster!}
            namespace={ns}
            workload={obj}
            selector={podSelector}
          />
        )}

        {tab === 'terminal' && isPod && (
          <Suspense fallback={<Loading label="Loading terminal" />}>
            <Terminal
              cluster={cluster!}
              namespace={ns!}
              pod={name!}
              container={termContainer ?? defaultContainerOf(obj)}
            />
          </Suspense>
        )}
      </div>

      <Modal
        open={scaleOpen}
        title={`Scale ${name}`}
        onClose={() => setScaleOpen(false)}
        footer={
          <>
            <Button onClick={() => setScaleOpen(false)} disabled={scaleBusy}>
              Cancel
            </Button>
            <Button variant="primary" onClick={doScale} disabled={scaleBusy}>
              Scale to {replicas}
            </Button>
          </>
        }
      >
        <p className="mb-3 text-sm text-ink-muted">
          Currently running <span className="font-medium text-ink">{currentReplicas}</span>{' '}
          replica(s).
        </p>
        <input
          type="number"
          min={0}
          value={replicas}
          onChange={(e) => setReplicas(Math.max(0, Number(e.target.value)))}
          className="w-32 rounded-md bg-surface-2 px-2 py-1.5 text-sm text-ink ring-1 ring-border"
        />
        {replicas === 0 && (
          <p className="mt-3 text-sm text-warn">
            Scaling to zero stops all replicas. The workload stays defined but serves nothing.
          </p>
        )}
      </Modal>

      <Modal
        open={deleteOpen}
        title={`Delete ${name}?`}
        onClose={() => setDeleteOpen(false)}
        footer={
          <>
            <Button onClick={() => setDeleteOpen(false)}>Cancel</Button>
            <Button variant="danger" onClick={doDelete}>
              Delete
            </Button>
          </>
        }
      >
        <p className="text-sm text-ink-muted">
          This deletes{' '}
          <span className="font-mono text-ink">
            {kind} {ns ? `${ns}/` : ''}
            {name}
          </span>
          . Dependent objects are garbage collected in the background.
        </p>
        <p className="mt-3 text-sm text-ink-faint">This cannot be undone.</p>
      </Modal>
    </div>
  )
}

/** Renders status conditions plus the remaining status fields as JSON. */
function StatusSection({ status }: { status?: Record<string, unknown> }) {
  if (!status || Object.keys(status).length === 0) return null

  const conditions = Array.isArray(status.conditions)
    ? (status.conditions as Record<string, string>[])
    : []
  const rest = Object.fromEntries(Object.entries(status).filter(([k]) => k !== 'conditions'))

  return (
    <section className="blueprint bg-surface p-3.5">
      <Corners />
      <h2 className="mb-2 text-[11px] font-semibold tracking-[.1em] text-ink-faint uppercase">
        Status
      </h2>

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
function PauseButton({
  paused,
  objRef,
  allowed,
  onDone,
}: {
  paused: boolean
  objRef: ResourceRef
  allowed: boolean
  onDone: () => void
}) {
  const [busy, setBusy] = useState(false)
  const toast = useToast()

  const toggle = async () => {
    setBusy(true)
    try {
      await api.patch(objRef, { spec: { paused: paused ? null : true } })
      toast.push({
        tone: 'ok',
        title: paused ? 'Rollouts resumed' : 'Rollouts paused',
        description: paused
          ? 'Template changes trigger rollouts again.'
          : 'Template changes accumulate without triggering a rollout until resumed.',
      })
      onDone()
    } catch (e) {
      toast.push({ tone: 'danger', title: 'Update failed', description: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }

  return (
    <GatedButton
      allowed={allowed}
      deniedTitle="Requires patch on deployments"
      size="sm"
      onClick={toggle}
      disabled={busy}
      title="kubectl rollout pause / resume"
    >
      {paused ? 'Resume rollouts' : 'Pause rollouts'}
    </GatedButton>
  )
}

interface Taint {
  key: string
  value?: string
  effect: string
}

/** kubectl taint, as a modal editor over spec.taints. */
function TaintsButton({
  objRef,
  taints,
  allowed,
  onDone,
}: {
  objRef: ResourceRef
  taints: Taint[]
  allowed: boolean
  onDone: () => void
}) {
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState<Taint[]>(taints)
  const [key, setKey] = useState('')
  const [value, setValue] = useState('')
  const [effect, setEffect] = useState('NoSchedule')
  const [busy, setBusy] = useState(false)
  const toast = useToast()

  const openModal = () => {
    setDraft(taints)
    setOpen(true)
  }

  const apply = async () => {
    setBusy(true)
    try {
      // Merge patch replaces the whole array, which is exactly right here.
      await api.patch(objRef, { spec: { taints: draft.length > 0 ? draft : null } })
      toast.push({ tone: 'ok', title: 'Taints updated' })
      setOpen(false)
      onDone()
    } catch (e) {
      toast.push({ tone: 'danger', title: 'Taint update failed', description: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <GatedButton
        allowed={allowed}
        deniedTitle="Requires patch on nodes"
        size="sm"
        onClick={openModal}
        title="kubectl taint"
      >
        Taints{taints.length > 0 && ` (${taints.length})`}
      </GatedButton>
      <Modal
        open={open}
        title="Node taints"
        wide
        onClose={() => setOpen(false)}
        footer={
          <>
            <Button onClick={() => setOpen(false)} disabled={busy}>
              Cancel
            </Button>
            <Button variant="primary" onClick={apply} disabled={busy}>
              Apply taints
            </Button>
          </>
        }
      >
        <p className="mb-3 text-sm text-ink-muted">
          Taints repel pods without a matching toleration. NoSchedule blocks new pods, NoExecute
          also evicts running ones.
        </p>
        {draft.length === 0 && <p className="text-sm text-ink-faint">No taints.</p>}
        <ul className="space-y-1">
          {draft.map((t, i) => (
            <li key={`${t.key}-${i}`} className="flex items-center gap-2 font-mono text-xs">
              <span className="flex-1 truncate text-ink">
                {t.key}
                {t.value ? `=${t.value}` : ''}:{t.effect}
              </span>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => setDraft(draft.filter((_, j) => j !== i))}
              >
                Remove
              </Button>
            </li>
          ))}
        </ul>
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <input
            value={key}
            onChange={(e) => setKey(e.target.value)}
            placeholder="key"
            aria-label="Taint key"
            className="w-40 bg-surface-2 px-2 py-1 font-mono text-xs text-ink ring-1 ring-border"
          />
          <input
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder="value (optional)"
            aria-label="Taint value"
            className="w-36 bg-surface-2 px-2 py-1 font-mono text-xs text-ink ring-1 ring-border"
          />
          <select
            value={effect}
            onChange={(e) => setEffect(e.target.value)}
            aria-label="Taint effect"
            className="bg-surface-2 px-2 py-1 text-xs text-ink ring-1 ring-border"
          >
            <option>NoSchedule</option>
            <option>PreferNoSchedule</option>
            <option>NoExecute</option>
          </select>
          <Button
            size="sm"
            disabled={!key.trim()}
            onClick={() => {
              setDraft([...draft, { key: key.trim(), value: value.trim() || undefined, effect }])
              setKey('')
              setValue('')
            }}
          >
            Add
          </Button>
        </div>
      </Modal>
    </>
  )
}

/**
 * Read-only HTTP proxy links — the browser's kubectl port-forward. Each port
 * opens through the API server's proxy subresource under the viewer's own
 * identity, so RBAC still applies.
 */
function ProxySection({
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
      <h2 className="mb-2 text-[11px] font-semibold tracking-[.1em] text-ink-faint uppercase">
        HTTP proxy
      </h2>
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
function RollbackButton({
  cluster,
  namespace,
  name,
  allowed,
  onDone,
}: {
  cluster: string
  namespace: string
  name: string
  allowed: boolean
  onDone: () => void
}) {
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const toast = useToast()

  const history = useQuery({
    queryKey: ['rollout-history', cluster, namespace, name],
    queryFn: () => api.rolloutHistory(cluster, namespace, name),
    enabled: open,
  })

  const undo = async (toRevision: number) => {
    setBusy(true)
    try {
      const res = await api.rolloutUndo(cluster, namespace, name, toRevision)
      toast.push({
        tone: 'ok',
        title: `Rolled ${name} back to revision ${res.toRevision}`,
        description: 'A new rollout with the old template is under way.',
      })
      setOpen(false)
      onDone()
    } catch (e) {
      toast.push({ tone: 'danger', title: 'Rollback failed', description: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }

  const revisions = history.data?.revisions ?? []

  return (
    <>
      <GatedButton
        allowed={allowed}
        deniedTitle="Requires patch on deployments and list on replicasets"
        size="sm"
        onClick={() => setOpen(true)}
        title="Rollout history and rollback"
      >
        History
      </GatedButton>
      <Modal open={open} title={`Rollout history of ${name}`} wide onClose={() => setOpen(false)}>
        {history.isLoading && (
          <p className="flex items-center gap-2 py-6 text-sm text-ink-faint">
            <Spinner /> Reading ReplicaSet revisions
          </p>
        )}
        {history.error != null && <ErrorState error={history.error} retry={history.refetch} />}
        {history.data && revisions.length === 0 && (
          <p className="py-6 text-center text-sm text-ink-faint">No revisions recorded.</p>
        )}
        {revisions.length > 0 && (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-border text-left text-xs text-ink-faint uppercase">
                <th className="py-1.5 pr-3 font-medium">Revision</th>
                <th className="py-1.5 pr-3 font-medium">Images</th>
                <th className="py-1.5 pr-3 text-right font-medium">Age</th>
                <th className="py-1.5 font-medium" />
              </tr>
            </thead>
            <tbody>
              {revisions.map((rev) => (
                <tr key={rev.revision} className="border-b border-border/50">
                  <td className="py-1.5 pr-3 tabular-nums">
                    {rev.revision}
                    {rev.current && (
                      <Badge tone="info" title="The template currently deployed">
                        current
                      </Badge>
                    )}
                  </td>
                  <td className="max-w-[20rem] truncate py-1.5 pr-3 font-mono text-xs text-ink-muted" title={rev.images.join('\n')}>
                    {rev.images.join(', ') || '—'}
                  </td>
                  <td className="py-1.5 pr-3 text-right">
                    <Age timestamp={rev.createdAt} />
                  </td>
                  <td className="py-1.5 text-right">
                    {!rev.current && (
                      <Button size="sm" disabled={busy} onClick={() => undo(rev.revision)}>
                        Roll back
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Modal>
    </>
  )
}

/** Run-now and suspend/resume — the two things kubectl users reach for on a CronJob. */
function CronJobActions({
  cluster,
  namespace,
  name,
  suspended,
  canRun,
  canSuspend,
  onDone,
}: {
  cluster: string
  namespace: string
  name: string
  suspended: boolean
  /** May create Jobs in this namespace — the verb behind "Run now". */
  canRun: boolean
  /** May patch the CronJob — the verb behind suspend/resume. */
  canSuspend: boolean
  onDone: () => void
}) {
  const [busy, setBusy] = useState(false)
  const toast = useToast()
  const navigate = useNavigate()

  const runNow = async () => {
    setBusy(true)
    try {
      const res = await api.triggerCronJob(cluster, namespace, name)
      toast.push({
        tone: 'ok',
        title: `Started job ${res.job}`,
        description: 'Created from this CronJob’s template, marked as a manual run.',
      })
      navigate(`/c/${cluster}/r/batch/v1/jobs/${namespace}/${res.job}`)
    } catch (e) {
      toast.push({ tone: 'danger', title: 'Run failed', description: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }

  const toggleSuspend = async () => {
    setBusy(true)
    try {
      await api.suspendCronJob(cluster, namespace, name, !suspended)
      toast.push({
        tone: 'ok',
        title: suspended ? `${name} resumed` : `${name} suspended`,
        description: suspended
          ? 'Scheduled runs will happen again.'
          : 'No new runs will be scheduled until it is resumed.',
      })
      onDone()
    } catch (e) {
      toast.push({ tone: 'danger', title: 'Update failed', description: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <GatedButton
        allowed={canRun}
        deniedTitle="Requires create on jobs"
        size="sm"
        onClick={runNow}
        disabled={busy}
        title="Create a one-off Job from this CronJob's template"
      >
        Run now
      </GatedButton>
      <GatedButton
        allowed={canSuspend}
        deniedTitle="Requires patch on cronjobs"
        size="sm"
        onClick={toggleSuspend}
        disabled={busy}
      >
        {suspended ? 'Resume' : 'Suspend'}
      </GatedButton>
    </>
  )
}

/** Eviction respects PodDisruptionBudgets, unlike a plain delete. */
function EvictButton({
  cluster,
  namespace,
  pod,
  allowed,
}: {
  cluster: string
  namespace: string
  pod: string
  allowed: boolean
}) {
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const toast = useToast()

  const evict = async () => {
    setBusy(true)
    try {
      await api.evict(cluster, namespace, pod)
      toast.push({ tone: 'ok', title: `Evicting ${pod}` })
      setOpen(false)
    } catch (e) {
      toast.push({ tone: 'danger', title: 'Eviction failed', description: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <GatedButton
        allowed={allowed}
        deniedTitle="Requires create on pods/eviction"
        size="sm"
        onClick={() => setOpen(true)}
      >
        Evict
      </GatedButton>
      <Modal
        open={open}
        title={`Evict ${pod}?`}
        onClose={() => setOpen(false)}
        footer={
          <>
            <Button onClick={() => setOpen(false)} disabled={busy}>
              Cancel
            </Button>
            <Button variant="danger" onClick={evict} disabled={busy}>
              Evict
            </Button>
          </>
        }
      >
        <p className="text-sm text-ink-muted">
          Eviction goes through the eviction API, so PodDisruptionBudgets are honoured — the
          request is refused rather than violating a budget. A controller-managed pod will be
          recreated elsewhere.
        </p>
      </Modal>
    </>
  )
}

/** Drain is destructive enough to deserve a dry run before the real thing. */
function DrainButton({
  cluster,
  node,
  allowed,
  onDone,
}: {
  cluster: string
  node: string
  allowed: boolean
  onDone: () => void
}) {
  const [open, setOpen] = useState(false)
  const [ignoreDaemonSets, setIgnoreDaemonSets] = useState(true)
  const [deleteEmptyDirData, setDeleteEmptyDirData] = useState(false)
  const [result, setResult] = useState<Awaited<ReturnType<typeof api.drain>>>()
  const [busy, setBusy] = useState(false)
  const toast = useToast()

  const run = async (dryRun: boolean) => {
    setBusy(true)
    try {
      const res = await api.drain(cluster, { node, ignoreDaemonSets, deleteEmptyDirData, dryRun })
      setResult(res)
      if (!dryRun) {
        toast.push({
          tone: res.failed.length > 0 ? 'warn' : 'ok',
          title: `Drained ${node}`,
          description: `${res.evicted.length} evicted, ${res.skipped.length} skipped, ${res.failed.length} failed`,
        })
        onDone()
      }
    } catch (e) {
      toast.push({ tone: 'danger', title: 'Drain failed', description: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <GatedButton
        allowed={allowed}
        deniedTitle="Requires patch on nodes and create on pods/eviction"
        size="sm"
        onClick={() => setOpen(true)}
      >
        Drain
      </GatedButton>
      <Modal
        open={open}
        title={`Drain ${node}`}
        wide
        onClose={() => {
          setOpen(false)
          setResult(undefined)
        }}
        footer={
          <>
            <Button
              onClick={() => {
                setOpen(false)
                setResult(undefined)
              }}
            >
              Cancel
            </Button>
            <Button onClick={() => run(true)} disabled={busy}>
              Dry run
            </Button>
            <Button variant="danger" onClick={() => run(false)} disabled={busy}>
              Drain
            </Button>
          </>
        }
      >
        <p className="mb-3 text-sm text-ink-muted">
          Cordons the node and evicts its pods through the eviction API, so PodDisruptionBudgets
          are respected.
        </p>
        <div className="space-y-2 text-sm">
          <label className="flex items-center gap-2 text-ink-muted">
            <input
              type="checkbox"
              checked={ignoreDaemonSets}
              onChange={(e) => setIgnoreDaemonSets(e.target.checked)}
            />
            Ignore DaemonSet-managed pods
          </label>
          <label className="flex items-center gap-2 text-ink-muted">
            <input
              type="checkbox"
              checked={deleteEmptyDirData}
              onChange={(e) => setDeleteEmptyDirData(e.target.checked)}
            />
            Evict pods using emptyDir volumes (their data is lost)
          </label>
        </div>

        {result && (
          <div className="mt-4 space-y-3 text-sm">
            {result.dryRun && <Badge tone="info">dry run — nothing was changed</Badge>}
            <DrainList
              title={result.dryRun ? 'Would evict' : 'Evicted'}
              items={result.evicted}
              tone="info"
            />
            <DrainList title="Skipped" items={result.skipped} tone="idle" />
            <DrainList title="Failed" items={result.failed} tone="danger" />
            {(result.notPermitted ?? 0) > 0 && (
              <p className="text-xs text-ink-faint">
                {result.notPermitted} pod(s) on this node are outside your permissions and were
                not touched.
              </p>
            )}
          </div>
        )}
      </Modal>
    </>
  )
}

function DrainList({
  title,
  items,
  tone,
}: {
  title: string
  items: string[]
  tone: 'info' | 'idle' | 'danger'
}) {
  if (items.length === 0) return null
  return (
    <div>
      <Badge tone={tone}>
        {title} ({items.length})
      </Badge>
      <ul className="mt-1 max-h-40 overflow-auto font-mono text-xs text-ink-muted">
        {items.map((i) => (
          <li key={i}>{i}</li>
        ))}
      </ul>
    </div>
  )
}
