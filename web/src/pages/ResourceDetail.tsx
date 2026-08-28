import clsx from 'clsx'
import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, apiGroup, type ResourceRef } from '../api/client'
import { stalledReason, useAccess, useEvents, useLiveResource, useMe } from '../api/hooks'
import type { AccessCheck, ObjectRef } from '../api/types'
import { DataTable } from '../components/DataTable'
import { LogViewer } from '../components/LogViewer'
import { WorkloadLogs } from '../components/WorkloadLogs'
import { containerNamesOf, defaultContainerOf } from '../lib/podTemplate'
import { podSelectorOf } from '../lib/selector'
import { MetadataEditor } from '../components/MetadataEditor'
import { RelatedSection } from '../components/RelatedSection'
import type { Taint } from './resource-detail/actions'
import {
  CronJobActions,
  DrainButton,
  EvictButton,
  PauseButton,
  RollbackButton,
  TaintsButton,
} from './resource-detail/actions'
import { containerRows } from '../lib/containerRows'
import { debugContainerState, debugStillStarting } from '../lib/debugContainer'
import {
  ContainerSection,
  DataSection,
  EnvSection,
  ProxySection,
  StatusSection,
} from './resource-detail/sections'
import {
  Age,
  Badge,
  Button,
  Corners,
  EmptyState,
  ErrorState,
  Field,
  GatedButton,
  Modal,
  Loading,
  StatusBadge,
} from '../components/primitives'
import { useToast } from '../components/Toast'
import { kindToResource, RESTARTABLE_KINDS, splitApiVersion } from '../lib/format'

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

/**
 * The terminal pane while a debug container is still being started.
 *
 * Attaching before the kubelet has pulled the image fails with `container not
 * found`, and the exec stream does not retry — so the wait happens here,
 * where the reason for it can be shown. A pull that is backing off looks
 * identical to a slow one from the API's side, so leaving is offered rather
 * than a timeout guessing on the viewer's behalf.
 */
function DebugStarting({
  container,
  image,
  detail,
  onCancel,
}: {
  container: string
  image: string
  detail?: string
  onCancel: () => void
}) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
      <Loading label={`Starting ${container}`} className="py-0" />
      <p className="font-mono text-[11.5px] text-ink-faint">{image}</p>
      <p className="max-w-[420px] text-sm text-ink-muted">
        {detail ?? 'Waiting for the node to pull the image and start the container.'}
      </p>
      <Button onClick={onCancel}>Stop waiting</Button>
    </div>
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
  // A debug container that exists in the pod spec but has no process to
  // attach to yet. The terminal shows why it is waiting instead of opening a
  // shell that the API server would refuse with `container not found`.
  const [debugStarting, setDebugStarting] = useState<{ container: string; image: string }>()

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

  const { data: obj, isLoading, error, stalled, refetch } = useLiveResource(ref)

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

  const eventsStalled = stalledReason(events)
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

  // The pod watch is what tells us the debug container has started. Deriving
  // it from the object already on screen means no second poll loop, and it
  // keeps the wait honest: whatever the kubelet says, the pane says.
  const debugState = useMemo(
    () => (debugStarting ? debugContainerState(obj, debugStarting.container) : undefined),
    [obj, debugStarting],
  )

  useEffect(() => {
    if (!debugStarting || !debugState) return
    if (debugState.phase === 'running') {
      setTermContainer(debugStarting.container)
      setDebugStarting(undefined)
      return
    }
    // A debug image whose entrypoint exits leaves nothing to attach to, and
    // the container cannot be removed to try again — worth saying loudly.
    if (debugState.phase === 'terminated') {
      toast.push({
        tone: 'danger',
        title: `${debugStarting.container} exited before a shell could attach`,
        description: debugState.detail,
      })
      setDebugStarting(undefined)
    }
  }, [debugStarting, debugState, toast])

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
      // Created, not started: the call only writes the container into the pod
      // spec. The terminal opens on the waiting pane and swaps itself for a
      // shell when the watch reports the container running.
      setDebugStarting({ container: res.container, image: res.image })
      setTab('terminal')
      toast.push({
        tone: 'ok',
        title: `Debug container created`,
        description: `${res.container} (${res.image}) — attaching once the kubelet starts it.`,
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
  // A parked retry does not resume on its own, so waiting shows nothing for
  // as long as anyone is willing to wait.
  if (stalled) return <ErrorState error={stalled} retry={refetch} />
  // Nothing loading, nothing failed, nothing here. Rendering an empty page
  // leaves the reader to guess which of those it was; a stale bookmark and a
  // deleted pod both land here and both deserve a sentence.
  if (!obj) {
    return (
      <EmptyState
        title={`${name} was not found`}
        description="It may have been deleted, or this link may name a namespace it does not live in."
      />
    )
  }

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
            ) : eventsStalled ? (
              // Not "No events": nothing was read, so nothing is known.
              <ErrorState error={eventsStalled} retry={events.refetch} />
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

        {tab === 'terminal' &&
          isPod &&
          (debugStarting && debugState && debugStillStarting(debugState) ? (
            <DebugStarting
              container={debugStarting.container}
              image={debugStarting.image}
              detail={debugState.detail}
              onCancel={() => setDebugStarting(undefined)}
            />
          ) : (
            <Suspense fallback={<Loading label="Loading terminal" />}>
              <Terminal
                cluster={cluster!}
                namespace={ns!}
                pod={name!}
                container={termContainer ?? defaultContainerOf(obj)}
              />
            </Suspense>
          ))}
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
