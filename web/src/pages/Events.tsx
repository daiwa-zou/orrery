import { useCallback, useMemo } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useDiscovery, useEvents } from '../api/hooks'
import type { APIResource, Row } from '../api/types'
import { isCustomGroup } from '../components/nav'
import { DataTable } from '../components/DataTable'
import { Button, ErrorState, Spinner } from '../components/primitives'
import { useDebouncedInput } from '../lib/useDebouncedInput'

/**
 * Cluster-wide event feed. The per-object feed lives on the detail page; this
 * page answers the broader "what just happened in this cluster?" question,
 * which is usually where an incident starts.
 */
export function Events() {
  const { cluster } = useParams<{ cluster: string }>()
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()

  const namespace = params.get('namespace') ?? ''
  const warningsOnly = params.get('warnings') === '1'
  // The filter lives in the URL like every other list filter, so a filtered
  // event view can be shared or revisited; it is applied server-side, before
  // the limit, so matches beyond the newest 500 events still surface.
  const q = params.get('q') ?? ''
  const commitQ = useCallback(
    (v: string) => {
      const next = new URLSearchParams(params)
      if (v === '') next.delete('q')
      else next.set('q', v)
      setParams(next, { replace: true })
    },
    [params, setParams],
  )
  const [qInput, setQInput] = useDebouncedInput(q, commitQ)

  const { data, isLoading, error, refetch, isFetching } = useEvents(cluster, {
    namespace: namespace || undefined,
    q: q || undefined,
    warningsOnly,
    limit: 500,
  })
  const { data: discovery } = useDiscovery(cluster)

  // Maps an involvedObject kind to the resource that serves it, so a row click
  // can land on the object the event is about.
  const kindToResource = useMemo(() => {
    const map = new Map<string, APIResource>()
    for (const group of discovery?.groups ?? []) {
      for (const res of group.resources) {
        const key = res.kind.toLowerCase()
        const existing = map.get(key)
        // Built-in groups win over CRDs that shadow a well-known kind (an
        // operator's "Service" must not capture core/v1 Service events), then
        // preferred versions win within a group.
        const better =
          !existing ||
          (isCustomGroup(existing.group) && !isCustomGroup(res.group)) ||
          (isCustomGroup(existing.group) === isCustomGroup(res.group) &&
            res.preferred &&
            !existing.preferred)
        if (better) map.set(key, res)
      }
    }
    return map
  }, [discovery])

  // The event's generated name is noise and its creation age repeats lastSeen;
  // kubectl get events shows neither, and neither do we.
  const columns = useMemo(
    () => (data?.columns ?? []).filter((c) => c.key !== 'name' && c.key !== 'age'),
    [data],
  )

  const rows = data?.items ?? []

  const toggleWarnings = () => {
    const next = new URLSearchParams(params)
    if (warningsOnly) next.delete('warnings')
    else next.set('warnings', '1')
    setParams(next, { replace: true })
  }

  const openInvolved = (row: Row) => {
    const [kind, name] = String(row.object ?? '').split('/')
    if (!kind || !name) return
    const res = kindToResource.get(kind.toLowerCase())
    if (!res) return
    const groupSeg = res.group === '' ? 'core' : res.group
    const ns = res.namespaced ? (row.namespace ?? '_') : '_'
    navigate(`/c/${cluster}/r/${groupSeg}/${res.version}/${res.name}/${ns}/${name}`)
  }

  if (error) return <ErrorState error={error} retry={refetch} />

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-border bg-surface px-4 py-2.5">
        <h1 className="mr-2 text-sm font-semibold text-ink">Events</h1>

        {data && (
          <span className="text-xs text-ink-faint tabular-nums">
            {rows.length.toLocaleString()} shown
            {namespace && <> in {namespace}</>}
          </span>
        )}
        {isFetching && !isLoading && <Spinner className="size-3.5" />}

        <div className="flex-1" />

        <label className="flex items-center gap-1.5 text-sm text-ink-muted">
          <input
            type="checkbox"
            checked={warningsOnly}
            onChange={toggleWarnings}
            className="accent-warn"
          />
          Warnings only
        </label>

        <input
          value={qInput}
          onChange={(e) => setQInput(e.target.value)}
          placeholder="Filter events"
          aria-label="Filter events"
          title="Matches object, reason, message or namespace"
          className="w-56 rounded-md bg-surface-2 px-2.5 py-1.5 text-sm text-ink ring-1 ring-border placeholder:text-ink-faint"
        />
        <Button size="sm" onClick={() => refetch()}>
          Refresh
        </Button>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        {isLoading ? (
          <div className="flex items-center justify-center gap-2 py-24 text-ink-faint">
            <Spinner /> Loading events
          </div>
        ) : (
          <DataTable
            columns={columns}
            rows={rows}
            onRowClick={openInvolved}
            emptyTitle={
              q
                ? 'No matches'
                : warningsOnly
                  ? 'No warning events'
                  : 'No recent events'
            }
            emptyDescription={
              q
                ? 'Try relaxing the filter.'
                : 'Events expire after about an hour, so quiet is normal.'
            }
          />
        )}
      </div>
    </div>
  )
}
