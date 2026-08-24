import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { api, type ResourceRef } from '../api/client'
import { useAccess, useLiveList, usePodMetrics } from '../api/hooks'
import type { AccessCheck, Column, Row } from '../api/types'
import { cpu as formatCpu, memory as formatMemory } from '../lib/format'
import { rowKey } from '../lib/selection'
import { DataTable, Pagination } from '../components/DataTable'
import { Badge, Button, ErrorState, Modal, Spinner } from '../components/primitives'
import { useToast } from '../components/Toast'

type BulkAction = 'delete' | 'restart'

/** The backend's restart action patches the pod template, so it only exists
 *  for the kinds that have one. */
const RESTARTABLE = new Set(['deployments', 'statefulsets', 'daemonsets'])

function nameList(rows: Row[]): string {
  const names = rows.map((r) => r.name)
  return names.length <= 6
    ? names.join(', ')
    : `${names.slice(0, 6).join(', ')} +${names.length - 6} more`
}

/**
 * Text input state that commits to the URL after a pause. Every committed
 * value is a server round trip, so keystrokes should not each cost one — and
 * half-typed label selectors are invalid anyway.
 */
function useDebouncedInput(
  urlValue: string,
  commit: (value: string) => void,
  delay = 300,
): [string, (v: string) => void] {
  const [value, setValue] = useState(urlValue)

  // Adopt outside changes (back button, palette) without clobbering typing.
  useEffect(() => {
    setValue(urlValue)
  }, [urlValue])

  useEffect(() => {
    if (value === urlValue) return
    const t = window.setTimeout(() => commit(value), delay)
    return () => window.clearTimeout(t)
  }, [value, urlValue, commit, delay])

  return [value, setValue]
}

/** Explains the live-update state in the header, honestly. */
function LiveIndicator({ state }: { state: 'connecting' | 'live' | 'polling' | 'off' }) {
  const map = {
    connecting: { tone: 'idle', label: 'connecting' },
    live: { tone: 'ok', label: 'live' },
    polling: { tone: 'warn', label: 'polling' },
    off: { tone: 'idle', label: 'static' },
  } as const

  const { tone, label } = map[state]
  const title =
    state === 'live'
      ? 'Streaming changes from the cluster watch'
      : state === 'polling'
        ? 'The live stream is unavailable; refreshing every 15 seconds instead'
        : undefined

  return (
    <Badge tone={tone} title={title}>
      {state === 'live' && <span className="size-1.5 animate-pulse rounded-full bg-ok" />}
      {label}
    </Badge>
  )
}

export function ResourceList() {
  const { cluster, group, version, resource } = useParams<{
    cluster: string
    group: string
    version: string
    resource: string
  }>()
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()

  const namespace = params.get('namespace') ?? ''
  const q = params.get('q') ?? ''
  const sort = params.get('sort') ?? 'name'
  const order = (params.get('order') as 'asc' | 'desc') ?? 'asc'
  const page = Number(params.get('page') ?? '1')
  const pageSize = Number(params.get('pageSize') ?? '50')
  const labelSelector = params.get('labelSelector') ?? ''
  // Deep-link only (e.g. "pods on this node"); there is no input for it.
  const fieldSelector = params.get('fieldSelector') ?? ''

  const [selected, setSelected] = useState<ReadonlySet<string>>(() => new Set())
  const [pendingBulk, setPendingBulk] = useState<{ action: BulkAction; rows: Row[] } | null>(null)
  const [bulkRunning, setBulkRunning] = useState(false)

  const ref: ResourceRef | null =
    cluster && group && version && resource
      ? { cluster, group: group === 'core' ? '' : group, version, resource }
      : null

  const listParams = useMemo(
    () => ({ namespace, q, sort, order, page, pageSize, labelSelector, fieldSelector }),
    [namespace, q, sort, order, page, pageSize, labelSelector, fieldSelector],
  )

  const { data, isLoading, error, live, refetch } = useLiveList(ref, listParams)

  const meta = data?.resource
  const canDelete = meta?.verbs.includes('delete') ?? false
  // Restart is authorized server-side as a "patch" on the workload itself.
  const canRestart =
    !!meta &&
    meta.group === 'apps' &&
    RESTARTABLE.has(meta.name) &&
    meta.verbs.includes('patch')

  // Ask once whether this user may act in this scope, so actions are only
  // offered when they would actually work.
  const checks = useMemo(() => {
    if (!meta) return []
    const base = {
      group: meta.group,
      version: meta.version,
      resource: meta.name,
      namespace: namespace || undefined,
    }
    const out: AccessCheck[] = []
    if (canDelete) out.push({ ...base, verb: 'delete' })
    if (canRestart) out.push({ ...base, verb: 'patch' })
    return out
  }, [meta, namespace, canDelete, canRestart])
  const access = useAccess(cluster, checks)
  const mayDelete = canDelete && (access.data?.[0]?.allowed ?? false)
  const mayRestart = canRestart && (access.data?.[canDelete ? 1 : 0]?.allowed ?? false)

  const update = useCallback(
    (patch: Record<string, string | null>) => {
      const next = new URLSearchParams(params)
      for (const [k, v] of Object.entries(patch)) {
        if (v === null || v === '') next.delete(k)
        else next.set(k, v)
      }
      setParams(next, { replace: true })
    },
    [params, setParams],
  )

  const commitQ = useCallback((v: string) => update({ q: v, page: '1' }), [update])
  const commitSelector = useCallback(
    (v: string) => update({ labelSelector: v, page: '1' }),
    [update],
  )
  const [qInput, setQInput] = useDebouncedInput(q, commitQ)
  const [selectorInput, setSelectorInput] = useDebouncedInput(labelSelector, commitSelector)

  const onSort = (key: string) => {
    if (key === sort) update({ order: order === 'asc' ? 'desc' : 'asc' })
    else update({ sort: key, order: 'asc' })
  }

  const openRow = (row: Row) => {
    const ns = row.namespace ?? '_'
    navigate(`/c/${cluster}/r/${group}/${version}/${resource}/${ns}/${row.name}${
      namespace ? `?namespace=${namespace}` : ''
    }`)
  }

  // Selected keys refer to objects in one specific list; a different cluster,
  // resource or namespace makes them meaningless.
  useEffect(() => {
    setSelected(new Set())
  }, [cluster, group, version, resource, namespace])

  const confirmBulk = async () => {
    if (!pendingBulk || !ref || !cluster) return
    const { action, rows: targets } = pendingBulk
    setBulkRunning(true)

    // Each object stands alone: one 403 or conflict must not abort the rest.
    const results = await Promise.allSettled(
      targets.map((row) =>
        action === 'delete'
          ? api.remove({ ...ref, namespace: row.namespace, name: row.name })
          : api.restart(cluster, {
              group: ref.group,
              version: ref.version,
              resource: ref.resource,
              namespace: row.namespace,
              name: row.name,
            }),
      ),
    )

    const succeeded = targets.filter((_, i) => results[i].status === 'fulfilled')
    const failed = results.flatMap((r, i) =>
      r.status === 'rejected' ? [{ row: targets[i], reason: (r.reason as Error).message }] : [],
    )

    if (succeeded.length > 0) {
      toast.push({
        tone: 'ok',
        title:
          action === 'delete'
            ? `Deleting ${succeeded.length === 1 ? succeeded[0].name : `${succeeded.length} objects`}`
            : `Restarted ${succeeded.length === 1 ? succeeded[0].name : `${succeeded.length} workloads`}`,
        description:
          succeeded.length === 1
            ? action === 'delete'
              ? 'The API server accepted the request.'
              : 'Pods will be replaced according to the update strategy.'
            : nameList(succeeded),
      })
    }
    // One toast per failed object: the reader must see exactly what was
    // missed and why. The Toast stack caps itself, so this cannot wallpaper.
    for (const f of failed) {
      toast.push({
        tone: 'danger',
        title: `${action === 'delete' ? 'Delete' : 'Restart'} failed: ${f.row.name}`,
        description: f.reason,
      })
    }

    setBulkRunning(false)
    setPendingBulk(null)
    setSelected(new Set())
    qc.invalidateQueries({ queryKey: ['list'] })
  }

  // Pods get live CPU/memory columns joined in from metrics-server. The list
  // itself stays cache-served; when metrics are unavailable the columns simply
  // do not appear.
  const isPods = ref?.group === '' && ref?.resource === 'pods'
  const metrics = usePodMetrics(isPods ? cluster : undefined, namespace)

  const { rows, columns } = useMemo(() => {
    const rows = data?.items ?? []
    const columns = data?.columns ?? []
    const m = metrics.data
    if (!isPods || !m?.available || !m.pods?.length) return { rows, columns }

    const usage = new Map(m.pods.map((p) => [`${p.namespace}/${p.name}`, p.usage]))
    const extra: Column[] = [
      { key: 'cpu', label: 'CPU', type: 'text', align: 'right', priority: 1 },
      { key: 'memory', label: 'Memory', type: 'text', align: 'right', priority: 1 },
    ]
    return {
      columns: [...columns, ...extra],
      rows: rows.map((row) => {
        const u = usage.get(`${row.namespace}/${row.name}`)
        return u ? { ...row, cpu: formatCpu(u.cpuMilli), memory: formatMemory(u.memoryMiB) } : row
      }),
    }
  }, [data, metrics.data, isPods])

  // Only rows on the current page are actionable; keys that left the page
  // (filter, pagination, the delete completing) simply stop counting.
  const selectedRows = useMemo(
    () => rows.filter((r) => selected.has(rowKey(r))),
    [rows, selected],
  )
  const bulkEnabled = mayDelete || mayRestart

  if (error) return <ErrorState error={error} retry={refetch} />

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-border bg-surface px-4 py-2.5">
        <h1 className="mr-2 text-sm font-semibold text-ink">
          {meta?.kind ?? resource}
          {meta && meta.group !== '' && (
            <span className="ml-1.5 font-mono text-xs font-normal text-ink-faint">
              {meta.group}/{meta.version}
            </span>
          )}
        </h1>

        <LiveIndicator state={live} />

        {fieldSelector && (
          <Badge tone="info" title="Field selector applied by the page you came from">
            <span className="font-mono">{fieldSelector}</span>
            <button
              aria-label="Clear field selector"
              className="ml-1 hover:text-ink"
              onClick={() => update({ fieldSelector: null, page: '1' })}
            >
              ×
            </button>
          </Badge>
        )}

        {data && (
          <span className="text-xs text-ink-faint tabular-nums">
            {data.total.toLocaleString()} total
          </span>
        )}

        <div className="flex-1" />

        <input
          value={qInput}
          onChange={(e) => setQInput(e.target.value)}
          placeholder="Filter by name"
          aria-label="Filter by name"
          className="w-56 rounded-md bg-surface-2 px-2.5 py-1.5 text-sm text-ink ring-1 ring-border placeholder:text-ink-faint"
        />
        <input
          value={selectorInput}
          onChange={(e) => setSelectorInput(e.target.value)}
          placeholder="Label selector"
          aria-label="Label selector"
          title="Kubernetes label selector syntax, e.g. app=web,tier!=cache"
          className="w-52 rounded-md bg-surface-2 px-2.5 py-1.5 font-mono text-xs text-ink ring-1 ring-border placeholder:text-ink-faint"
        />
        <Button size="sm" onClick={() => refetch()}>
          Refresh
        </Button>
      </div>

      {selectedRows.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 border-b border-border bg-accent-soft/25 px-4 py-2">
          <span className="text-sm font-medium text-ink tabular-nums">
            {selectedRows.length} selected
          </span>
          {mayRestart && (
            <Button
              size="sm"
              onClick={() => setPendingBulk({ action: 'restart', rows: selectedRows })}
            >
              Restart
            </Button>
          )}
          {mayDelete && (
            <Button
              size="sm"
              variant="danger"
              onClick={() => setPendingBulk({ action: 'delete', rows: selectedRows })}
            >
              Delete
            </Button>
          )}
          <div className="flex-1" />
          <Button size="sm" variant="ghost" onClick={() => setSelected(new Set())}>
            Clear selection
          </Button>
        </div>
      )}

      {/* The server tells us when it could only show part of the cluster.
          Surfacing that is the difference between "empty" and "invisible". */}
      {data?.scope && !data.scope.allNamespaces && !data.scope.namespace && (
        <p className="border-b border-border bg-warn/8 px-4 py-1.5 text-xs text-warn">
          You can only list this resource in{' '}
          {data.scope.namespaces?.length ?? 0} namespace(s):{' '}
          <span className="font-mono">{(data.scope.namespaces ?? []).join(', ')}</span>
        </p>
      )}
      {data?.warnings?.map((w) => (
        <p key={w} className="border-b border-border bg-warn/8 px-4 py-1.5 text-xs text-warn">
          {w}
        </p>
      ))}

      <div className="min-h-0 flex-1 overflow-auto">
        {isLoading && rows.length === 0 ? (
          <div className="flex items-center justify-center gap-2 py-24 text-ink-faint">
            <Spinner /> Loading {resource}
          </div>
        ) : (
          <DataTable
            columns={columns}
            rows={rows}
            sort={sort}
            order={order}
            onSort={onSort}
            onRowClick={openRow}
            loading={isLoading}
            emptyTitle={q || labelSelector ? 'No matches' : `No ${meta?.kind ?? resource} found`}
            emptyDescription={
              q || labelSelector
                ? 'Try relaxing the filters.'
                : namespace
                  ? `Nothing in namespace ${namespace}.`
                  : undefined
            }
            selected={bulkEnabled ? selected : undefined}
            onSelectedChange={bulkEnabled ? setSelected : undefined}
            rowActions={
              mayDelete
                ? (row) => (
                    <Button
                      size="sm"
                      variant="ghost"
                      title={`Delete ${row.name}`}
                      onClick={() => setPendingBulk({ action: 'delete', rows: [row] })}
                    >
                      Delete
                    </Button>
                  )
                : undefined
            }
          />
        )}
      </div>

      {/* Keep the controls while page > 1 even when total shrank, or a user
          stranded past the last page has no way back. */}
      {data && (data.total > pageSize || page > 1) && (
        <Pagination
          page={page}
          pageSize={pageSize}
          total={data.total}
          onPage={(p) => update({ page: String(p) })}
          onPageSize={(s) => update({ pageSize: String(s), page: '1' })}
        />
      )}

      <Modal
        open={!!pendingBulk}
        title={
          pendingBulk
            ? `${pendingBulk.action === 'delete' ? 'Delete' : 'Restart'} ${
                pendingBulk.rows.length === 1
                  ? pendingBulk.rows[0].name
                  : `${pendingBulk.rows.length} objects`
              }?`
            : ''
        }
        onClose={() => {
          if (!bulkRunning) setPendingBulk(null)
        }}
        footer={
          <>
            <Button onClick={() => setPendingBulk(null)} disabled={bulkRunning}>
              Cancel
            </Button>
            <Button
              variant={pendingBulk?.action === 'delete' ? 'danger' : 'primary'}
              onClick={confirmBulk}
              disabled={bulkRunning}
            >
              {bulkRunning && <Spinner className="size-3.5" />}
              {pendingBulk?.action === 'delete' ? 'Delete' : 'Restart'}
            </Button>
          </>
        }
      >
        <p className="text-sm text-ink-muted">
          {pendingBulk?.action === 'delete' ? (
            <>
              This deletes {pendingBulk.rows.length === 1 ? 'this' : 'these'}{' '}
              <span className="font-mono text-ink">{meta?.kind}</span>{' '}
              {pendingBulk.rows.length === 1 ? 'object' : 'objects'} with background
              propagation. Dependent objects will be garbage collected.
            </>
          ) : (
            <>
              This performs a rolling restart of{' '}
              {pendingBulk?.rows.length === 1 ? 'this workload' : 'these workloads'} by
              stamping the pod template, the same way{' '}
              <span className="font-mono text-ink">kubectl rollout restart</span> does.
            </>
          )}
        </p>
        <ul className="mt-3 max-h-56 overflow-auto rounded-md bg-surface-2 px-3 py-2 font-mono text-xs text-ink ring-1 ring-border">
          {pendingBulk?.rows.map((row) => (
            <li key={rowKey(row)} className="truncate py-0.5">
              {row.namespace ? `${row.namespace}/` : ''}
              {row.name}
            </li>
          ))}
        </ul>
        {pendingBulk?.action === 'delete' && (
          <p className="mt-3 text-sm text-ink-faint">This cannot be undone.</p>
        )}
      </Modal>
    </div>
  )
}
