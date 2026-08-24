import clsx from 'clsx'
import { useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type ResourceRef } from '../api/client'
import { useAccess, useEvents, useResource } from '../api/hooks'
import type { KubeObject } from '../api/types'
import { DataTable } from '../components/DataTable'
import { LogViewer } from '../components/LogViewer'
import { Terminal } from '../components/Terminal'
import { YamlEditor } from '../components/YamlEditor'
import {
  Age,
  Badge,
  Button,
  ErrorState,
  Field,
  LabelChips,
  Modal,
  Spinner,
  StatusBadge,
} from '../components/primitives'
import { useToast } from '../components/Toast'
import { kindToResource, splitApiVersion } from '../lib/format'

type Tab = 'overview' | 'yaml' | 'events' | 'logs' | 'terminal'

const SCALABLE = new Set(['Deployment', 'StatefulSet', 'ReplicaSet', 'ReplicationController'])
const RESTARTABLE = new Set(['Deployment', 'StatefulSet', 'DaemonSet'])
/** Kinds whose spec.selector selects pods, enabling a "view pods" jump. */
const POD_OWNERS = new Set([
  'Deployment',
  'StatefulSet',
  'DaemonSet',
  'ReplicaSet',
  'ReplicationController',
  'Job',
])

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
}: {
  rows: ContainerRow[]
  onLogs: (container: string) => void
}) {
  if (rows.length === 0) return null

  return (
    <section className="rounded-lg bg-surface p-4 ring-1 ring-border">
      <h2 className="mb-2 text-xs font-semibold tracking-wide text-ink-faint uppercase">
        Containers
      </h2>
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border text-left text-xs text-ink-faint uppercase">
              <th className="py-1.5 pr-3 font-medium">Name</th>
              <th className="py-1.5 pr-3 font-medium">State</th>
              <th className="py-1.5 pr-3 text-right font-medium">Ready</th>
              <th className="py-1.5 pr-3 text-right font-medium">Restarts</th>
              <th className="py-1.5 pr-3 font-medium">Last exit</th>
              <th className="py-1.5 pr-3 font-medium">Image</th>
              <th className="py-1.5 font-medium" />
            </tr>
          </thead>
          <tbody>
            {rows.map((c) => (
              <tr key={`${c.init}-${c.name}`} className="border-b border-border/50">
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
                <td className="py-1.5 pr-3 text-right tabular-nums">
                  <span className={c.restarts > 0 ? 'font-medium text-warn' : undefined}>
                    {c.restarts}
                  </span>
                </td>
                <td className="py-1.5 pr-3 font-mono text-xs text-ink-muted">
                  {c.lastExit ?? '—'}
                </td>
                <td className="max-w-[18rem] truncate py-1.5 pr-3 font-mono text-xs text-ink-muted" title={c.image}>
                  {c.image}
                </td>
                <td className="py-1.5 text-right">
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

function containerNames(obj?: KubeObject): string[] {
  if (!obj) return []
  const spec = obj.spec as { containers?: { name: string }[]; initContainers?: { name: string }[] }
  return [...(spec?.initContainers ?? []), ...(spec?.containers ?? [])].map((c) => c.name)
}

function TabButton({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      onClick={onClick}
      className={clsx(
        'border-b-2 px-3 py-2 text-sm transition-colors',
        active
          ? 'border-accent text-ink'
          : 'border-transparent text-ink-muted hover:text-ink',
      )}
    >
      {children}
    </button>
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

  const ns = namespace === '_' ? undefined : namespace
  const ref: ResourceRef = {
    cluster: cluster!,
    group: group === 'core' ? '' : group!,
    version: version!,
    resource: resource!,
    namespace: ns,
    name,
  }

  const [tab, setTab] = useState<Tab>('overview')
  const [scaleOpen, setScaleOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [replicas, setReplicas] = useState(0)
  const [yamlDirty, setYamlDirty] = useState(false)
  const [logContainer, setLogContainer] = useState<string>()

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

  const { data: obj, isLoading, error, refetch } = useResource(ref)

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
  const containers = containerNames(obj)

  const access = useAccess(
    cluster,
    obj
      ? [
          { verb: 'update', group: ref.group, version: ref.version, resource: ref.resource, namespace: ns, name },
          { verb: 'delete', group: ref.group, version: ref.version, resource: ref.resource, namespace: ns, name },
          { verb: 'update', group: ref.group, version: ref.version, resource: ref.resource, subresource: 'scale', namespace: ns, name },
          { verb: 'create', group: '', version: 'v1', resource: 'pods', subresource: 'exec', namespace: ns, name },
        ]
      : [],
  )
  const [mayUpdate, mayDelete, mayScale, mayExec] = [
    access.data?.[0]?.allowed ?? false,
    access.data?.[1]?.allowed ?? false,
    access.data?.[2]?.allowed ?? false,
    access.data?.[3]?.allowed ?? false,
  ]

  const owners = obj?.metadata.ownerReferences ?? []

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['object'] })
    qc.invalidateQueries({ queryKey: ['yaml'] })
    qc.invalidateQueries({ queryKey: ['list'] })
  }

  const doScale = async () => {
    try {
      await api.scale(cluster!, { group: ref.group, version: ref.version, resource: ref.resource, namespace: ns, name }, replicas)
      toast.push({ tone: 'ok', title: `Scaled ${name} to ${replicas}` })
      invalidate()
    } catch (e) {
      toast.push({ tone: 'danger', title: 'Scale failed', description: (e as Error).message })
    } finally {
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

  const saveYaml = async (next: string) => {
    await api.replace(ref, next)
    toast.push({ tone: 'ok', title: `${name} updated` })
    invalidate()
  }

  const currentReplicas = useMemo(() => {
    const spec = obj?.spec as { replicas?: number } | undefined
    return spec?.replicas ?? 0
  }, [obj])

  // The label selector this workload uses to own its pods, as query syntax.
  // ReplicationControllers predate LabelSelector and use a bare map.
  const podSelector = useMemo(() => {
    if (!POD_OWNERS.has(kind)) return ''
    const sel = (obj?.spec as { selector?: Record<string, unknown> } | undefined)?.selector
    if (!sel || typeof sel !== 'object') return ''
    const labels = (sel.matchLabels as Record<string, unknown> | undefined) ?? sel
    return Object.entries(labels)
      .filter(([, v]) => typeof v === 'string')
      .map(([k, v]) => `${k}=${v}`)
      .join(',')
  }, [obj, kind])

  if (isLoading) {
    return (
      <div className="flex items-center justify-center gap-2 py-24 text-ink-faint">
        <Spinner /> Loading {name}
      </div>
    )
  }
  if (error) return <ErrorState error={error} retry={refetch} />
  if (!obj) return null

  const status = obj.status as Record<string, unknown> | undefined
  const phase = typeof status?.phase === 'string' ? status.phase : undefined

  return (
    <div className="flex h-full flex-col">
      <header className="border-b border-border bg-surface px-4 py-3">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="truncate text-base font-semibold text-ink">{obj.metadata.name}</h1>
              {phase && <StatusBadge value={phase} />}
              {obj.metadata.deletionTimestamp && <Badge tone="warn">terminating</Badge>}
            </div>
            <p className="mt-0.5 font-mono text-xs text-ink-faint">
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
            {SCALABLE.has(kind) && mayScale && (
              <Button
                size="sm"
                onClick={() => {
                  setReplicas(currentReplicas)
                  setScaleOpen(true)
                }}
              >
                Scale
              </Button>
            )}
            {RESTARTABLE.has(kind) && mayUpdate && (
              <Button size="sm" onClick={doRestart} title="Stamps the pod template to trigger a rollout">
                Restart
              </Button>
            )}
            {isNode && mayUpdate && (
              <>
                <Button
                  size="sm"
                  onClick={() => doCordon(!(obj.spec as { unschedulable?: boolean })?.unschedulable)}
                >
                  {(obj.spec as { unschedulable?: boolean })?.unschedulable ? 'Uncordon' : 'Cordon'}
                </Button>
                <DrainButton cluster={cluster!} node={name!} onDone={invalidate} />
              </>
            )}
            {mayDelete && (
              <Button size="sm" variant="danger" onClick={() => setDeleteOpen(true)}>
                Delete
              </Button>
            )}
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
          {isPod && (
            <TabButton active={tab === 'logs'} onClick={() => switchTab('logs')}>
              Logs
            </TabButton>
          )}
          {isPod && mayExec && (
            <TabButton active={tab === 'terminal'} onClick={() => switchTab('terminal')}>
              Terminal
            </TabButton>
          )}
        </nav>
      </header>

      <div className="min-h-0 flex-1 overflow-auto">
        {tab === 'overview' && (
          <div className="mx-auto max-w-5xl space-y-4 p-4">
            <section className="rounded-lg bg-surface p-4 ring-1 ring-border">
              <h2 className="mb-2 text-xs font-semibold tracking-wide text-ink-faint uppercase">
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
                  <LabelChips labels={obj.metadata.labels} />
                </Field>
                <Field label="Annotations">
                  <LabelChips labels={obj.metadata.annotations} />
                </Field>
                {owners.length > 0 && (
                  <Field label="Controlled by">
                    <div className="flex flex-wrap gap-2">
                      {owners.map((o) => (
                        <Link key={o.uid} to={ownerHref(cluster!, o, ns)} className="text-accent hover:underline">
                          {o.kind}/{o.name}
                        </Link>
                      ))}
                    </div>
                  </Field>
                )}
              </dl>
            </section>

            {isPod && (
              <ContainerSection
                rows={containerRows(obj)}
                onLogs={(container) => {
                  setLogContainer(container)
                  setTab('logs')
                }}
              />
            )}

            <StatusSection status={status} />
          </div>
        )}

        {tab === 'yaml' && (
          <div className="h-full">
            {yamlQuery.isLoading ? (
              <div className="flex items-center justify-center gap-2 py-24 text-ink-faint">
                <Spinner /> Loading manifest
              </div>
            ) : yamlQuery.error ? (
              <ErrorState error={yamlQuery.error} retry={yamlQuery.refetch} />
            ) : (
              <YamlEditor
                value={yamlQuery.data ?? ''}
                readOnly={!mayUpdate}
                onSave={mayUpdate ? saveYaml : undefined}
                onDirtyChange={setYamlDirty}
                notice={
                  mayUpdate
                    ? 'Applying replaces the object. Server-managed fields are stripped from this view.'
                    : 'You do not have permission to update this object.'
                }
              />
            )}
          </div>
        )}

        {tab === 'events' && (
          <div className="p-4">
            {events.isLoading ? (
              <div className="flex items-center justify-center gap-2 py-16 text-ink-faint">
                <Spinner /> Loading events
              </div>
            ) : (
              <div className="rounded-lg bg-surface ring-1 ring-border">
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

        {tab === 'logs' && isPod && (
          <LogViewer
            cluster={cluster!}
            namespace={ns!}
            pod={name!}
            containers={containers}
            initialContainer={logContainer}
          />
        )}

        {tab === 'terminal' && isPod && (
          <Terminal
            cluster={cluster!}
            namespace={ns!}
            pod={name!}
            container={containers[containers.length - 1] ?? ''}
          />
        )}
      </div>

      <Modal
        open={scaleOpen}
        title={`Scale ${name}`}
        onClose={() => setScaleOpen(false)}
        footer={
          <>
            <Button onClick={() => setScaleOpen(false)}>Cancel</Button>
            <Button variant="primary" onClick={doScale}>
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

/**
 * Owner references carry their apiVersion, so the link can be exact — a CRD
 * owner on v1beta1 works just as well as a core Deployment. The kind→resource
 * pluralisation is the only guess left.
 */
function ownerHref(
  cluster: string,
  owner: { apiVersion?: string; kind: string; name: string },
  ns?: string,
): string {
  const { group, version } = splitApiVersion(owner.apiVersion)
  const seg = group === '' ? 'core' : group
  return `/c/${cluster}/r/${seg}/${version || 'v1'}/${kindToResource(owner.kind)}/${ns ?? '_'}/${owner.name}`
}

/** Renders status conditions plus the remaining status fields as JSON. */
function StatusSection({ status }: { status?: Record<string, unknown> }) {
  if (!status || Object.keys(status).length === 0) return null

  const conditions = Array.isArray(status.conditions)
    ? (status.conditions as Record<string, string>[])
    : []
  const rest = Object.fromEntries(Object.entries(status).filter(([k]) => k !== 'conditions'))

  return (
    <section className="rounded-lg bg-surface p-4 ring-1 ring-border">
      <h2 className="mb-2 text-xs font-semibold tracking-wide text-ink-faint uppercase">Status</h2>

      {conditions.length > 0 && (
        <table className="mb-4 w-full text-sm">
          <thead>
            <tr className="border-b border-border text-left text-xs text-ink-faint uppercase">
              <th className="py-1.5 pr-3 font-medium">Condition</th>
              <th className="py-1.5 pr-3 font-medium">Status</th>
              <th className="py-1.5 pr-3 font-medium">Reason</th>
              <th className="py-1.5 font-medium">Message</th>
            </tr>
          </thead>
          <tbody>
            {conditions.map((c, i) => (
              <tr key={`${c.type}-${i}`} className="border-b border-border/50">
                <td className="py-1.5 pr-3 text-ink">{c.type}</td>
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
        <pre className="max-h-80 overflow-auto rounded bg-canvas p-3 font-mono text-xs text-ink-muted">
          {JSON.stringify(rest, null, 2)}
        </pre>
      )}
    </section>
  )
}

/** Drain is destructive enough to deserve a dry run before the real thing. */
function DrainButton({
  cluster,
  node,
  onDone,
}: {
  cluster: string
  node: string
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
      <Button size="sm" onClick={() => setOpen(true)}>
        Drain
      </Button>
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
            <DrainList title="Would evict" items={result.evicted} tone="info" />
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
