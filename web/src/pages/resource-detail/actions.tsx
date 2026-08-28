import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'

import { api, type ResourceRef } from '../../api/client'
import {
  Age,
  Badge,
  Button,
  Checkbox,
  ErrorState,
  GatedButton,
  Modal,
  Select,
  Spinner,
  TextInput,
} from '../../components/primitives'
import { useToast } from '../../components/Toast'

/**
 * The buttons that change something: pausing a rollout, editing taints,
 * rolling back, triggering or suspending a CronJob, evicting a pod, draining a
 * node.
 *
 * Each owns its own confirmation, its own busy state and its own invalidation,
 * which is why they separate cleanly from the page that hosts them — and why
 * they were most of its length. Every one is gated on the permission the
 * server will check, so a button that would be refused is shown inert rather
 * than hidden.
 */

export function PauseButton({
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

export interface Taint {
  key: string
  value?: string
  effect: string
}

/** kubectl taint, as a modal editor over spec.taints. */
export function TaintsButton({
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
          <TextInput
            value={key}
            onChange={(e) => setKey(e.target.value)}
            placeholder="key"
            aria-label="Taint key"
            className="w-40 font-mono"
          />
          <TextInput
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder="value (optional)"
            aria-label="Taint value"
            className="w-36 font-mono"
          />
          <Select
            value={effect}
            onChange={(e) => setEffect(e.target.value)}
            aria-label="Taint effect"
          >
            <option>NoSchedule</option>
            <option>PreferNoSchedule</option>
            <option>NoExecute</option>
          </Select>
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
export function RollbackButton({
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
export function CronJobActions({
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
export function EvictButton({
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
export function DrainButton({
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
            <Checkbox checked={ignoreDaemonSets} onChange={(e) => setIgnoreDaemonSets(e.target.checked)} />
            Ignore DaemonSet-managed pods
          </label>
          <label className="flex items-center gap-2 text-ink-muted">
            <Checkbox checked={deleteEmptyDirData} onChange={(e) => setDeleteEmptyDirData(e.target.checked)} />
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
