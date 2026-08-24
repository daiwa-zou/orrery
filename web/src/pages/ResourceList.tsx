import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { api, type ResourceRef } from '../api/client'
import { useAccess, useLiveList, usePodMetrics } from '../api/hooks'
import type { Column, Row } from '../api/types'
import { cpu as formatCpu, memory as formatMemory } from '../lib/format'
import { DataTable, Pagination } from '../components/DataTable'
import { Badge, Button, ErrorState, Modal, Spinner } from '../components/primitives'
import { useToast } from '../components/Toast'

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

  const [pendingDelete, setPendingDelete] = useState<Row | null>(null)

  const ref: ResourceRef | null =
    cluster && group && version && resource
      ? { cluster, group: group === 'core' ? '' : group, version, resource }
      : null

  const listParams = useMemo(
    () => ({ namespace, q, sort, order, page, pageSize, labelSelector }),
    [namespace, q, sort, order, page, pageSize, labelSelector],
  )

  const { data, isLoading, error, live, refetch } = useLiveList(ref, listParams)

  const meta = data?.resource
  const canDelete = meta?.verbs.includes('delete') ?? false

  // Ask once whether this user may delete in this scope, so the row action is
  // only offered when it would actually work.
  const access = useAccess(
    cluster,
    canDelete && meta
      ? [
          {
            verb: 'delete',
            group: meta.group,
            version: meta.version,
            resource: meta.name,
            namespace: namespace || undefined,
          },
        ]
      : [],
  )
  const mayDelete = canDelete && (access.data?.[0]?.allowed ?? false)

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

  const confirmDelete = async () => {
    if (!pendingDelete || !ref) return
    try {
      await api.remove({ ...ref, namespace: pendingDelete.namespace, name: pendingDelete.name })
      toast.push({
        tone: 'ok',
        title: `Deleting ${pendingDelete.name}`,
        description: 'The API server accepted the request.',
      })
      qc.invalidateQueries({ queryKey: ['list'] })
    } catch (e) {
      toast.push({ tone: 'danger', title: 'Delete failed', description: (e as Error).message })
    } finally {
      setPendingDelete(null)
    }
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
            rowActions={
              mayDelete
                ? (row) => (
                    <Button
                      size="sm"
                      variant="ghost"
                      title={`Delete ${row.name}`}
                      onClick={() => setPendingDelete(row)}
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
        open={!!pendingDelete}
        title={`Delete ${pendingDelete?.name ?? ''}?`}
        onClose={() => setPendingDelete(null)}
        footer={
          <>
            <Button onClick={() => setPendingDelete(null)}>Cancel</Button>
            <Button variant="danger" onClick={confirmDelete}>
              Delete
            </Button>
          </>
        }
      >
        <p className="text-sm text-ink-muted">
          This deletes{' '}
          <span className="font-mono text-ink">
            {meta?.kind} {pendingDelete?.namespace ? `${pendingDelete.namespace}/` : ''}
            {pendingDelete?.name}
          </span>{' '}
          with background propagation. Dependent objects will be garbage collected.
        </p>
        <p className="mt-3 text-sm text-ink-faint">This cannot be undone.</p>
      </Modal>
    </div>
  )
}
