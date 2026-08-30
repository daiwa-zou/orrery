import { Fragment, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'

import { api, type ResourceRef, type Revision } from '../../api/client'
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
/**
 * The lines that differ, under the row they belong to.
 *
 * Naming a field says whether to look; this says what would be going back —
 * which argument, and to what. The legend is not decoration: a diff with no
 * stated direction is ambiguous exactly where it matters, and reading it
 * backwards means restoring the thing you meant to remove.
 */
function RevisionDiff({ rev }: { rev: Revision }) {
  const lines = rev.diff ?? []
  if (lines.length === 0) return null

  return (
    <div className="mt-1 bg-canvas p-2 ring-1 ring-border">
      <p className="mb-1.5 text-[11px] text-ink-faint">
        <span className="text-danger">−</span> deployed now ·{' '}
        <span className="text-ok">+</span> what revision {rev.revision} would restore
      </p>
      <pre className="overflow-x-auto font-mono text-[11px] leading-[1.45]">
        {lines.map((line, i) => (
          <div
            key={`${i}-${line.text}`}
            className={
              line.op === '-'
                ? 'bg-danger/10 text-danger'
                : line.op === '+'
                  ? 'bg-ok/10 text-ok'
                  : line.op === '…'
                    ? 'text-ink-faint'
                    : 'text-ink-muted'
            }
          >
            {line.op === '…' ? '  ⋯' : `${line.op} ${line.text}`}
          </div>
        ))}
      </pre>
      {!!rev.diffTruncated && (
        // A diff that stops partway reads as a complete one unless it says so.
        <p className="mt-1.5 text-[11px] text-ink-faint">
          {rev.diffTruncated.toLocaleString()} more changed{' '}
          {rev.diffTruncated === 1 ? 'line' : 'lines'} not shown — the object's YAML has all of it.
        </p>
      )}
    </div>
  )
}

/**
 * What rolling back to one revision would do, in the terms the choice is made
 * in.
 *
 * Three answers, and the middle one is why this exists. A revision can differ
 * from the deployed template in ways no column shows — an environment
 * variable, a resource limit, a probe — so two rows with the same image and
 * the same age were, until now, indistinguishable choices. And a revision can
 * be *identical*, where rolling back is a no-op that looks exactly like a
 * decision; saying so is the difference between choosing and guessing.
 *
 * The third case is the honest one: the server found a difference it does not
 * name. Better to say that than to imply there is none.
 */
function RevisionChange({ rev }: { rev: Revision }) {
  if (rev.current) return <span className="text-ink-faint">deployed now</span>

  if (rev.identical) {
    return (
      <span className="text-ink-faint">
        none — the pod template is identical, so rolling back changes nothing
      </span>
    )
  }

  const changes = rev.changes ?? []
  if (changes.length === 0) {
    // Different, but not in any part this server names. Saying so beats both
    // silence and a claim of sameness.
    return <span className="text-ink-muted">the pod template, in some other part</span>
  }

  return (
    <ul className="space-y-0.5">
      {changes.slice(0, 4).map((change) => (
        <li key={change} className="truncate font-mono text-[11px] text-ink-muted" title={change}>
          {change}
        </li>
      ))}
      {changes.length > 4 && (
        <li className="text-[11px] text-ink-faint" title={changes.join('\n')}>
          +{changes.length - 4} more
        </li>
      )}
    </ul>
  )
}

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
  /** Which revision's diff is open. One at a time: the point is to compare a
   *  candidate against what is deployed, not five candidates against each
   *  other. */
  const [openDiff, setOpenDiff] = useState<number | null>(null)
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
                <th className="py-1.5 pr-4 font-medium">Revision</th>
                <th className="py-1.5 pr-4 font-medium">Images</th>
                {/* The column this table was missing: a revision number and a
                    date do not say what going back to it would do. Named for
                    what the cell holds — a difference — since half the entries
                    are field names and "rolling back would args" is not a
                    sentence. */}
                <th className="py-1.5 pr-4 font-medium">Difference from current</th>
                <th className="py-1.5 pr-4 text-right font-medium">Pods</th>
                <th className="py-1.5 pr-4 text-right font-medium">Age</th>
                <th className="py-1.5 font-medium" />
              </tr>
            </thead>
            <tbody>
              {revisions.map((rev) => (
                <Fragment key={rev.revision}>
                <tr className="border-b border-border/50 align-top">
                  <td className="py-2 pr-4">
                    {/* gap-2, because the badge was touching the number and
                        read as part of it — "2current". */}
                    <span className="flex items-center gap-2">
                      <span className="tabular-nums">{rev.revision}</span>
                      {rev.current && (
                        <Badge tone="info" title="The template currently deployed">
                          current
                        </Badge>
                      )}
                    </span>
                    {/* The ReplicaSet behind the revision, for anyone
                        cross-checking against kubectl. */}
                    <span className="mt-0.5 block truncate font-mono text-[10.5px] text-ink-faint" title={rev.name}>
                      {rev.name}
                    </span>
                  </td>
                  <td className="max-w-[16rem] truncate py-2 pr-4 font-mono text-xs text-ink-muted" title={rev.images.join('\n')}>
                    {rev.images.join(', ') || '—'}
                  </td>
                  <td className="max-w-[26rem] py-2 pr-4 text-xs">
                    <RevisionChange rev={rev} />
                    {rev.changeCause && (
                      <span className="mt-1 block text-[11px] text-ink-faint" title="kubernetes.io/change-cause">
                        “{rev.changeCause}”
                      </span>
                    )}
                    {/* Behind a disclosure rather than always open: five
                        revisions of diffs at once is a wall, and the summary
                        above is what most readers need. */}
                    {(rev.diff?.length ?? 0) > 0 && (
                      <>
                        <button
                          type="button"
                          onClick={() => setOpenDiff(openDiff === rev.revision ? null : rev.revision)}
                          aria-expanded={openDiff === rev.revision}
                          className="mt-1 text-[11px] text-accent-text hover:text-accent-text-hover"
                        >
                          {openDiff === rev.revision ? 'Hide' : 'Show'} the lines that differ
                        </button>
                      </>
                    )}
                  </td>
                  {/* Ready of desired: a revision that never became ready is a
                      poor thing to go back to, and this is the only warning of
                      it on the page. */}
                  <td
                    className="py-2 pr-4 text-right tabular-nums"
                    title={`${rev.ready} of ${rev.replicas} pods from this revision became ready`}
                  >
                    <span className={rev.replicas > 0 && rev.ready < rev.replicas ? 'text-warn' : 'text-ink-muted'}>
                      {rev.ready}/{rev.replicas}
                    </span>
                  </td>
                  <td className="py-2 pr-4 text-right text-ink-muted">
                    <Age timestamp={rev.createdAt} />
                  </td>
                  <td className="py-2 text-right">
                    {!rev.current && (
                      <GatedButton
                        size="sm"
                        allowed={!rev.identical}
                        deniedTitle="This revision's pod template is identical to the one deployed now, so rolling back would change nothing"
                        disabled={busy}
                        onClick={() => undo(rev.revision)}
                      >
                        Roll back
                      </GatedButton>
                    )}
                  </td>
                </tr>
                {/* A row of its own, spanning the table: a diff inside one
                    cell widens that column and squeezes every other one. */}
                {openDiff === rev.revision && (
                  <tr className="border-b border-border/50">
                    <td colSpan={6} className="pb-3">
                      <RevisionDiff rev={rev} />
                    </td>
                  </tr>
                )}
                </Fragment>
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
            {/*
              A separate sentence, not a bigger number beside the one above.
              These pods were not evicted either, so the node is not drained —
              but the reason is not permission, and saying it was sends the
              reader to their cluster administrator instead of to a retry.
            */}
            {(result.notChecked ?? 0) > 0 && (
              <p className="text-xs text-warn">
                {result.notChecked} pod(s) could not be checked against your permissions, so they
                were not touched. The node is not fully drained; try again.
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
