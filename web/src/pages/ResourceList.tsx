import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { api, apiGroup, type ResourceRef } from '../api/client'
import { useAccess, useFacets, useLiveList, usePodMetrics } from '../api/hooks'
import type { AccessCheck, Column, Row } from '../api/types'
import { cpu as formatCpu, memory as formatMemory, RESTARTABLE_RESOURCES } from '../lib/format'
import { toggleSelectorTerm } from '../lib/labels'
import { queryTerms, removeQueryTerm, type SearchQuery } from '../lib/searchQuery'

/** The unfiltered scope, stable so it never re-keys the facets query. */
const EMPTY_QUERY: SearchQuery = { q: '', labelSelector: '', fieldSelector: '', where: [] }
import { rowKey } from '../lib/selection'
import { DataTable, Pagination } from '../components/DataTable'
import { RefreshIcon, TagIcon, TrashIcon, ColumnsIcon} from '../components/icons'
import { SearchBar } from '../components/SearchBar'
import {
  addColumn,
  addSaved,
  COLUMNS_KEY,
  columnsIn,
  describeSaved,
  isSavedIn,
  removeColumn,
  removeSaved,
  SAVED_KEY,
  savedIn,
  savedLabel,
  type SavedSearch,
} from '../lib/storage'
import { useStoredRaw } from '../lib/useStored'
import {
  Badge,
  Button,
  ErrorState,
  Eyebrow,
  Field,
  GatedButton,
  Loading,
  Modal,
  Spinner,
  TextInput,
} from '../components/primitives'
import { useToast } from '../components/Toast'

type BulkAction = 'delete' | 'restart'


function nameList(rows: Row[]): string {
  const names = rows.map((r) => r.name)
  return names.length <= 6
    ? names.join(', ')
    : `${names.slice(0, 6).join(', ')} +${names.length - 6} more`
}

/** Explains the live-update state in the header, honestly. */
function ColumnPicker({
  chosen,
  available,
  onAdd,
  onRemove,
}: {
  chosen: string[]
  available: string[]
  onAdd: (key: string) => void
  onRemove: (key: string) => void
}) {
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState('')

  // Suggest the label keys actually present on these objects, which the search
  // bar's facets already fetched. Typing a key that is not suggested still
  // works — a label may exist on objects outside the current page.
  const suggestions = available.filter((k) => !chosen.includes(k)).slice(0, 12)

  const submit = () => {
    onAdd(draft)
    setDraft('')
  }

  return (
    <div className="relative">
      <Button
        size="sm"
        icon
        variant={chosen.length > 0 ? 'primary' : 'default'}
        aria-expanded={open}
        aria-label="Choose label columns"
        title="Add a label as its own column"
        onClick={() => setOpen((v) => !v)}
      >
        <ColumnsIcon />
      </Button>

      {open && (
        <div className="absolute right-0 z-20 mt-1 w-72 border border-border bg-raised p-2.5 shadow-lg">
          <Eyebrow as="p" className="mb-2">Label columns</Eyebrow>

          {chosen.length > 0 && (
            <ul className="mb-2 flex flex-wrap gap-1">
              {chosen.map((k) => (
                <li key={k}>
                  <button
                    onClick={() => onRemove(k)}
                    title={`Remove the ${k} column`}
                    className="border border-border px-1.5 py-0.5 font-mono text-[11px] text-ink-muted hover:border-danger hover:text-danger"
                  >
                    {k} ×
                  </button>
                </li>
              ))}
            </ul>
          )}

          <form
            onSubmit={(e) => {
              e.preventDefault()
              submit()
            }}
            className="flex gap-1"
          >
            <TextInput
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              placeholder="label key, e.g. team"
              aria-label="Label key to add as a column"
              className="flex-1 font-mono"
            />
            <Button size="sm" type="submit" disabled={!draft.trim()}>
              Add
            </Button>
          </form>

          {suggestions.length > 0 && (
            <ul className="mt-2 flex flex-wrap gap-1">
              {suggestions.map((k) => (
                <li key={k}>
                  <button
                    onClick={() => onAdd(k)}
                    className="border border-border px-1.5 py-0.5 font-mono text-[11px] text-ink-faint hover:border-accent hover:text-accent-text"
                  >
                    + {k}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  )
}

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
    <>
      <Badge tone={tone} title={title}>
        {state === 'live' && <span className="size-1.5 animate-pulse rounded-full bg-ok" />}
        <span aria-hidden="true">{label}</span>
      </Badge>
      {/* A reader who cannot see the badge change colour still needs to know
          the page stopped being live. Announced politely, and only when the
          connection state actually moves — never per row. */}
      <span role="status" aria-live="polite" className="sr-only">
        {state === 'live'
          ? 'Live: updates are streaming from the cluster.'
          : state === 'polling'
            ? 'Live stream unavailable. Refreshing every 15 seconds instead.'
            : state === 'connecting'
              ? 'Connecting to the live stream.'
              : 'Static list. Not receiving live updates.'}
      </span>
    </>
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
  // Repeated rather than comma-separated, because a pattern may contain a
  // comma. Joined only to key the memo below on the values themselves.
  const whereTerms = params.getAll('where')
  const whereKey = whereTerms.join('\u0000')
  const showLabels = params.get('labels') === '1'

  const [selected, setSelected] = useState<ReadonlySet<string>>(() => new Set())
  const [pendingBulk, setPendingBulk] = useState<{ action: BulkAction; rows: Row[] } | null>(null)
  const [bulkRunning, setBulkRunning] = useState(false)

  const ref: ResourceRef | null =
    cluster && group && version && resource
      ? { cluster, group: apiGroup(group), version, resource }
      : null

  const listParams = useMemo(
    () => ({
      namespace,
      q,
      sort,
      order,
      page,
      pageSize,
      labelSelector,
      fieldSelector,
      where: whereTerms,
    }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [namespace, q, sort, order, page, pageSize, labelSelector, fieldSelector, whereKey],
  )

  const { data, isLoading, error, stalled, live, refetch } = useLiveList(ref, listParams)

  const meta = data?.resource
  const canDelete = meta?.verbs.includes('delete') ?? false
  // Restart is authorized server-side as a "patch" on the workload itself.
  const canRestart =
    !!meta &&
    meta.group === 'apps' &&
    RESTARTABLE_RESOURCES.has(meta.name) &&
    meta.verbs.includes('patch')

  const canCreate = meta?.verbs.includes('create') ?? false

  // Ask once whether this user may act in this scope, so actions are only
  // offered when they would actually work. Keyed, not positional: each key
  // lives beside its check, so adding one cannot silently gate the wrong
  // button — the same rule the detail page follows.
  const checkList = useMemo(() => {
    if (!meta) return []
    const base = {
      group: meta.group,
      version: meta.version,
      resource: meta.name,
      namespace: namespace || undefined,
    }
    const list: { key: string; check: AccessCheck }[] = []
    if (canDelete) list.push({ key: 'delete', check: { ...base, verb: 'delete' } })
    if (canRestart) list.push({ key: 'restart', check: { ...base, verb: 'patch' } })
    if (canCreate) list.push({ key: 'create', check: { ...base, verb: 'create' } })
    return list
  }, [meta, namespace, canDelete, canRestart, canCreate])
  const checks = useMemo(() => checkList.map((c) => c.check), [checkList])
  const access = useAccess(cluster, checks)
  const may = (key: string) => {
    const i = checkList.findIndex((c) => c.key === key)
    return i >= 0 ? (access.data?.[i]?.allowed ?? false) : false
  }
  const mayDelete = canDelete && may('delete')
  const mayRestart = canRestart && may('restart')
  const mayCreate = canCreate && may('create')

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

  const searchQuery = useMemo<SearchQuery>(
    () => ({ q, labelSelector, fieldSelector, where: whereTerms }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [q, labelSelector, fieldSelector, whereKey],
  )

  // `where` is the one repeated parameter, so it is set here rather than
  // through update()'s one-value-per-key patch.
  const commitSearch = useCallback(
    (next: SearchQuery) => {
      const params_ = new URLSearchParams(params)
      for (const [k, v] of Object.entries({
        q: next.q,
        labelSelector: next.labelSelector,
        fieldSelector: next.fieldSelector,
        page: '1',
      })) {
        if (v === null || v === '') params_.delete(k)
        else params_.set(k, v)
      }
      params_.delete('where')
      for (const term of next.where ?? []) params_.append('where', term)
      setParams(params_, { replace: true })
    },
    [params, setParams],
  )
  // Facets are only fetched once the user reaches for the search bar, and
  // then only for what is already filtering — the search bar reports that
  // scope, since only it knows which term the cursor is inside.
  const [searchActive, setSearchActive] = useState(false)
  const activateSearch = useCallback(() => setSearchActive(true), [])
  const [facetScope, setFacetScope] = useState<SearchQuery>(EMPTY_QUERY)
  const facets = useFacets(ref, namespace, searchActive, facetScope)

  // A starred view is the resource plus the narrowing that made it useful —
  // "the failing pods in staging" rather than just "pods". That narrowing is
  // the selectors, which this had been leaving behind.
  const savedView = useMemo<SavedSearch | null>(
    () =>
      meta
        ? {
            cluster: cluster!,
            group: group!,
            version: version!,
            resource: resource!,
            kind: meta.kind,
            namespaced: meta.namespaced,
            namespace,
            q,
            labelSelector,
            fieldSelector,
            where: whereTerms,
            name: '',
          }
        : null,
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [meta, cluster, group, version, resource, namespace, q, labelSelector, fieldSelector, whereKey],
  )

  // Both of these are read from localStorage during render rather than copied
  // into state by an effect, because an effect runs after paint and the copy
  // would show one frame of the wrong thing every time the view changes. The
  // raw string is what the hook returns, so these memos have a stable key.
  const columnsRaw = useStoredRaw(COLUMNS_KEY)
  const labelColumns = useMemo(
    () => (cluster && resource ? columnsIn(columnsRaw, cluster, resource) : []),
    [columnsRaw, cluster, resource],
  )

  const addLabelColumn = (key: string) => {
    if (!cluster || !resource) return
    addColumn(cluster, resource, key)
  }
  const dropLabelColumn = (key: string) => {
    if (!cluster || !resource) return
    removeColumn(cluster, resource, key)
  }

  // The star belongs to this resource-plus-query, not to the page.
  const savedRaw = useStoredRaw(SAVED_KEY)
  const starred = useMemo(
    () => (savedView ? isSavedIn(savedRaw, savedView) : false),
    [savedRaw, savedView],
  )

  // Saving asks for a name; unsaving does not ask anything. Naming is what
  // makes a list of saved views readable — "Failing web pods" rather than six
  // entries all called Pods — and the field is prefilled with what the view
  // selects, so it stays one keystroke for anyone who does not care.
  const [naming, setNaming] = useState<string | null>(null)
  const toggleStar = () => {
    if (!savedView) return
    if (starred) {
      // The name lives in the stored copy, not in the one built from this
      // page, so say what the reader called it rather than re-describing it.
      const stored = savedIn(savedRaw, savedView)
      removeSaved(savedView)
      toast.push({ tone: 'ok', title: `Removed “${savedLabel(stored ?? savedView)}”` })
      return
    }
    setNaming(describeSaved(savedView))
  }

  const confirmSave = () => {
    if (!savedView || naming === null) return
    const named = { ...savedView, name: naming.trim() }
    addSaved(named)
    setNaming(null)
    toast.push({
      tone: 'ok',
      title: `Saved “${savedLabel(named)}”`,
      description: 'Find it in the command palette with ⌘K.',
    })
  }

  const onLabelClick = useCallback(
    (k: string, v: string) =>
      update({ labelSelector: toggleSelectorTerm(labelSelector, k, v), page: '1' }),
    [labelSelector, update],
  )

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
    let rows = data?.items ?? []
    let columns = data?.columns ?? []

    const m = metrics.data
    if (isPods && m?.available && m.pods?.length) {
      const usage = new Map(m.pods.map((p) => [`${p.namespace}/${p.name}`, p]))
      const extra: Column[] = [
        { key: 'cpu', label: 'CPU', type: 'bar', align: 'right', priority: 1 },
        { key: 'memory', label: 'Memory', type: 'bar', align: 'right', priority: 1 },
      ]
      columns = [...columns, ...extra]
      rows = rows.map((row) => {
        const p = usage.get(`${row.namespace}/${row.name}`)
        if (!p) return row
        // The bar fills against the container limits — the ceiling the kubelet
        // enforces. Without a declared limit there is no honest denominator,
        // so the bar stays empty and only the reading shows.
        const pct = (used: number, limit?: number) =>
          limit && limit > 0 ? Math.min(100, (used / limit) * 100) : 0
        return {
          ...row,
          cpu: { text: formatCpu(p.usage.cpuMilli), percent: pct(p.usage.cpuMilli, p.limits?.cpuMilli) },
          memory: {
            text: formatMemory(p.usage.memoryMiB),
            percent: pct(p.usage.memoryMiB, p.limits?.memoryMiB),
          },
          _cpuMilli: p.usage.cpuMilli,
          _memoryMiB: p.usage.memoryMiB,
        }
      })
      // These columns exist only client-side, so the server sorted by name;
      // order the fetched page here or the header arrow would lie. Pods
      // without metrics sort below any measured value.
      if (sort === 'cpu' || sort === 'memory') {
        const field = sort === 'cpu' ? '_cpuMilli' : '_memoryMiB'
        rows = [...rows].sort((a, b) => {
          const av = (a[field] as number | undefined) ?? -1
          const bv = (b[field] as number | undefined) ?? -1
          return order === 'desc' ? bv - av : av - bv
        })
      }
    }

    // Chosen label columns read straight off the row's labels, which the list
    // already carries — so this costs nothing on the wire and needs no
    // server-side projection.
    if (labelColumns.length > 0) {
      columns = [
        ...columns,
        ...labelColumns.map(
          (key): Column => ({ key: `label:${key}`, label: key, type: 'text', priority: 1 }),
        ),
      ]
      rows = rows.map((row) => {
        const next = { ...row }
        for (const key of labelColumns) {
          next[`label:${key}`] = row._labels?.[key] ?? '—'
        }
        return next
      })
    }

    if (showLabels) {
      columns = [...columns, { key: '_labels', label: 'Labels', type: 'labels', priority: 1 }]
    }
    return { rows, columns }
  }, [data, metrics.data, isPods, showLabels, sort, order, labelColumns])

  // Only rows on the current page are actionable; keys that left the page
  // (filter, pagination, the delete completing) simply stop counting.
  const selectedRows = useMemo(
    () => rows.filter((r) => selected.has(rowKey(r))),
    [rows, selected],
  )
  // Checkboxes follow what the resource supports; the action buttons dim by
  // permission, so a read-only viewer sees the affordance and why it is inert.
  const bulkEnabled = canDelete || canRestart

  // The bar is capped at max-w-md, so a committed multi-term query scrolls
  // out of a box whose end the reader cannot see. Listing the terms outside
  // it is what makes an active filter legible; the cross is what makes one
  // undoable without retyping the others.
  // Filter terms only. Free text stays live in the search box, and repeating
  // it here would be the duplication this row exists to avoid.
  const activeTerms = useMemo(
    () => queryTerms(searchQuery).filter((t) => t.kind !== 'text'),
    [searchQuery],
  )

  // A page past the end is not an empty collection. "No Pod found" there is
  // the same lie the rest of this codebase works to avoid: there are pods,
  // this page is simply not where they are.
  const lastPage = data ? Math.max(1, Math.ceil(data.total / pageSize)) : 1
  const pastEnd = !!data && data.total > 0 && rows.length === 0 && page > lastPage

  if (error) return <ErrorState error={error} retry={refetch} />
  // A parked retry with nothing to show would otherwise fall through to the
  // table's empty state, which asserts that the namespace is empty. Saying
  // "nothing here" when the truth is "I could not ask" is the one answer worse
  // than saying nothing.
  if (stalled) return <ErrorState error={stalled} retry={refetch} />

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-border bg-surface px-4 py-2">
        <h1 className="mr-2 font-condensed text-[17px] font-semibold tracking-[.02em] text-ink">
          {meta?.kind ?? resource}
          {meta && meta.group !== '' && (
            <span className="ml-1.5 font-mono text-[11px] font-normal tracking-normal text-ink-faint">
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

        <SearchBar
          query={searchQuery}
          onCommit={commitSearch}
          facets={facets.data}
          columns={data?.columns}
          onActivate={activateSearch}
          onScopeChange={setFacetScope}
          placeholder="Search, or add a filter — app=web"
        />
        {savedView && (
          <Button
            size="sm"
            icon
            variant={starred ? 'primary' : 'default'}
            onClick={toggleStar}
            aria-pressed={starred}
            title={
              starred
                ? 'Remove this view from your saved views'
                : 'Save this view — resource, namespace and every filter — to the command palette'
            }
            aria-label={starred ? 'Remove this saved view' : 'Save this view'}
          >
            {starred ? '★' : '☆'}
          </Button>
        )}
        <ColumnPicker
          chosen={labelColumns}
          available={(facets.data?.labels ?? []).map((f) => f.key)}
          onAdd={addLabelColumn}
          onRemove={dropLabelColumn}
        />
        <Button
          size="sm"
          icon
          variant={showLabels ? 'primary' : 'default'}
          aria-pressed={showLabels}
          aria-label="Toggle labels column"
          title={
            showLabels
              ? 'Hide the labels column'
              : 'Show a labels column; click a chip to filter by it'
          }
          onClick={() => update({ labels: showLabels ? null : '1' })}
        >
          <TagIcon />
        </Button>
        <Button
          size="sm"
          icon
          aria-label="Refresh"
          title="Refresh this list now"
          onClick={() => refetch()}
        >
          <RefreshIcon />
        </Button>
        {canCreate && (
          <GatedButton
            allowed={mayCreate}
            deniedTitle={`Requires create on ${resource}`}
            size="sm"
            variant="primary"
            title={`Create a ${meta?.kind ?? 'resource'} from YAML`}
            onClick={() =>
              navigate(
                `/c/${cluster}/r/${group}/${version}/${resource}/create${
                  namespace ? `?namespace=${namespace}` : ''
                }`,
              )
            }
          >
            + New
          </GatedButton>
        )}
      </div>

      {activeTerms.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5 border-b border-border bg-surface/60 px-4 py-1.5">
          <Eyebrow as="span" className="mr-0.5">
            Filtering
          </Eyebrow>
          {activeTerms.map((t) => (
            <button
              key={`${t.kind}:${t.term}`}
              type="button"
              onClick={() => commitSearch(removeQueryTerm(searchQuery, t))}
              title={`Remove ${t.term} from the search`}
              aria-label={`Remove ${t.term} from the search`}
              // Rounded, unlike the square controls around it: these are not
              // controls but the values themselves, and the shape is what
              // says so at a glance.
              className="group inline-flex h-7 items-center gap-1.5 rounded-full bg-accent/15 px-3 font-mono text-xs text-ink-muted ring-1 ring-accent/45 transition-colors hover:bg-accent/25 hover:text-ink"
            >
              {t.term}
              <span aria-hidden className="text-ink-faint group-hover:text-danger">
                ×
              </span>
            </button>
          ))}
          {/* Worth offering only when it does more than the cross beside it:
              more than one chip, or a chip plus free text in the box. */}
          {(activeTerms.length > 1 || (activeTerms.length > 0 && q !== '')) && (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => commitSearch(EMPTY_QUERY)}
            >
              Clear all
            </Button>
          )}
        </div>
      )}

      {selectedRows.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 border-b border-border bg-accent-soft/25 px-4 py-2">
          <span className="text-sm font-medium text-ink tabular-nums">
            {selectedRows.length} selected
          </span>
          {canRestart && (
            <GatedButton
              allowed={mayRestart}
              deniedTitle={`Requires patch on ${resource}`}
              size="sm"
              onClick={() => setPendingBulk({ action: 'restart', rows: selectedRows })}
            >
              Restart
            </GatedButton>
          )}
          <GatedButton
            allowed={mayDelete}
            deniedTitle={`Requires delete on ${resource}`}
            size="sm"
            variant="danger"
            onClick={() => setPendingBulk({ action: 'delete', rows: selectedRows })}
          >
            Delete
          </GatedButton>
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
          <Loading label={`Loading ${resource}`} />
        ) : (
          <DataTable
            columns={columns}
            rows={rows}
            sort={sort}
            order={order}
            onSort={onSort}
            onRowClick={openRow}
            onLabelClick={showLabels ? onLabelClick : undefined}
            loading={isLoading}
            emptyTitle={
              pastEnd
                ? `Nothing on page ${page.toLocaleString()}`
                : q || labelSelector || fieldSelector
                  ? 'No matches'
                  : `No ${meta?.kind ?? resource} found`
            }
            emptyDescription={
              pastEnd ? (
                <>
                  There {data!.total === 1 ? 'is' : 'are'}{' '}
                  {data!.total.toLocaleString()}{' '}
                  {/* `resource` is the API's own plural — "pods", but also
                      "ingresses" and "networkpolicies", which appending an
                      "s" to the kind would get wrong. */}
                  {data!.total === 1 ? (meta?.kind ?? resource) : resource}, on{' '}
                  {lastPage.toLocaleString()} {lastPage === 1 ? 'page' : 'pages'}.{' '}
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => update({ page: '1' })}
                  >
                    Back to the first page
                  </Button>
                </>
              ) : q || labelSelector || fieldSelector ? (
                'Try relaxing the filters.'
              ) : namespace ? (
                `Nothing in namespace ${namespace}.`
              ) : undefined
            }
            selected={bulkEnabled ? selected : undefined}
            onSelectedChange={bulkEnabled ? setSelected : undefined}
            rowActions={
              canDelete
                ? (row) => (
                    <GatedButton
                      allowed={mayDelete}
                      deniedTitle={`Requires delete on ${resource}`}
                      size="sm"
                      icon
                      variant="ghost"
                      aria-label={`Delete ${row.name}`}
                      title={`Delete ${row.name}`}
                      onClick={() => setPendingBulk({ action: 'delete', rows: [row] })}
                    >
                      <TrashIcon className="size-3.5" />
                    </GatedButton>
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
        open={naming !== null}
        title="Save this view"
        onClose={() => setNaming(null)}
        footer={
          <>
            <Button size="sm" onClick={() => setNaming(null)}>
              Cancel
            </Button>
            <Button size="sm" variant="primary" onClick={confirmSave}>
              Save view
            </Button>
          </>
        }
      >
        <form
          onSubmit={(e) => {
            e.preventDefault()
            confirmSave()
          }}
        >
          <label className="block text-[13px] text-ink-muted">
            Name
            <TextInput
              size="md"
              autoFocus
              value={naming ?? ''}
              onChange={(e) => setNaming(e.target.value)}
              className="mt-1.5 w-full"
              aria-label="Name for this saved view"
            />
          </label>
        </form>

        {/* Showing what is captured is the point: the filters were being
            dropped before, and a reader has no other way to tell whether the
            star kept them. */}
        <Eyebrow as="p" className="mt-4 mb-1.5">
          What gets saved
        </Eyebrow>
        <dl className="text-[13px]">
          <Field label="Resource">{meta?.kind ?? resource}</Field>
          <Field label="Namespace">
            {namespace || <span className="text-ink-faint">All namespaces</span>}
          </Field>
          <Field label="Filters">
            {activeTerms.length === 0 ? (
              <span className="text-ink-faint">None</span>
            ) : (
              <span className="flex flex-wrap gap-1.5">
                {activeTerms.map((t) => (
                  <span
                    key={`${t.kind}:${t.term}`}
                    className="inline-flex items-center rounded-full bg-accent/15 px-2.5 py-0.5 font-mono text-[11px] text-ink-muted ring-1 ring-accent/45"
                  >
                    {t.term}
                  </span>
                ))}
              </span>
            )}
          </Field>
          <Field label="Search text">{q || <span className="text-ink-faint">None</span>}</Field>
        </dl>
      </Modal>

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
        <ul className="mt-3 max-h-56 overflow-auto bg-canvas px-3 py-2 font-mono text-xs text-ink ring-1 ring-border">
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
