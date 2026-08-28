import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { stalledReason, useDiscovery, useEvents } from '../api/hooks'
import type { Row } from '../api/types'
import { objectPath, resourcesByKind } from '../components/nav'
import { DataTable } from '../components/DataTable'
import { EventSearchBar } from '../components/EventSearchBar'
import { RefreshIcon } from '../components/icons'
import {
  Age,
  Badge,
  Button,
  ErrorState,
  Eyebrow,
  LiveIndicator,
  Loading,
  Spinner,
} from '../components/primitives'
import {
  addWhereTerm,
  removeWhereTerm,
  summarizeEvents,
  valueTerm,
  type EventQuery,
} from '../lib/eventQuery'

/** "Only the things that went wrong", as a term the reader can also remove. */
const WARNINGS_TERM = valueTerm('type', 'Warning')

/**
 * Cluster-wide event feed. The per-object feed lives on the detail page; this
 * page answers the broader "what just happened in this cluster?" question,
 * which is usually where an incident starts.
 */
export function Events() {
  const { cluster } = useParams<{ cluster: string }>()
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  // Auto-refresh is what this page is for, right up until the reader is
  // reading it — an incident is investigated by comparing rows, and rows that
  // reorder mid-comparison cost more than the freshness gains.
  const [live, setLive] = useState(true)

  const namespace = params.get('namespace') ?? ''
  // The search lives in the URL like every other list filter, so a narrowed
  // event view can be shared or revisited; it is applied server-side, before
  // the limit, so matches beyond the newest 500 events still surface.
  const q = params.get('q') ?? ''
  const whereTerms = params.getAll('where')
  const whereKey = whereTerms.join('\u0000')
  const query = useMemo<EventQuery>(
    () => ({ q, where: whereTerms }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [q, whereKey],
  )

  const commit = useCallback(
    (next: EventQuery) => {
      const params_ = new URLSearchParams(params)
      if (next.q === '') params_.delete('q')
      else params_.set('q', next.q)
      // `where` is the one repeated parameter, so it is rewritten wholesale
      // rather than set.
      params_.delete('where')
      for (const term of next.where) params_.append('where', term)
      setParams(params_, { replace: true })
    },
    [params, setParams],
  )

  // A link written before the type filter existed still means "warnings
  // only". It is rewritten into the term the page now speaks, so the filter it
  // turns on is one the reader can see in the chips and take off again.
  useEffect(() => {
    if (params.get('warnings') !== '1') return
    const params_ = new URLSearchParams(params)
    params_.delete('warnings')
    if (!params_.getAll('where').includes(WARNINGS_TERM)) {
      params_.append('where', WARNINGS_TERM)
    }
    setParams(params_, { replace: true })
  }, [params, setParams])

  const events = useEvents(
    cluster,
    {
      namespace: namespace || undefined,
      q: q || undefined,
      where: whereTerms.length ? whereTerms : undefined,
      limit: 500,
    },
    live,
  )
  const { data, isLoading, error, refetch, isFetching, dataUpdatedAt } = events
  const { data: discovery } = useDiscovery(cluster)

  // Maps an involvedObject kind to the resource that serves it, so a row click
  // can land on the object the event is about.
  const byKind = useMemo(() => resourcesByKind(discovery), [discovery])

  // The event's generated name is noise and its creation age repeats lastSeen;
  // kubectl get events shows neither, and neither do we.
  const columns = useMemo(
    () => (data?.columns ?? []).filter((c) => c.key !== 'name' && c.key !== 'age'),
    [data],
  )

  // Memoised because it feeds two memos and the search bar's suggestion
  // vocabulary; a fresh [] every render would recompute all three.
  const rows = useMemo(() => data?.items ?? [], [data])
  const summary = useMemo(() => summarizeEvents(rows), [rows])
  const warningsOnly = whereTerms.includes(WARNINGS_TERM)
  const filtering = q !== '' || whereTerms.length > 0

  const toggleWarnings = () =>
    commit(warningsOnly ? removeWhereTerm(query, WARNINGS_TERM) : addWhereTerm(query, WARNINGS_TERM))

  const openInvolved = (row: Row) => {
    const path = objectPath(cluster!, byKind, String(row.object ?? ''), row.namespace)
    if (path) navigate(path)
  }

  if (error) return <ErrorState error={error} retry={refetch} />
  // A parked retry would otherwise fall through to the table's empty state and
  // report "No recent events" — which reads as a quiet cluster rather than as a
  // question that was never answered.
  const stalled = stalledReason(events)
  if (stalled) return <ErrorState error={stalled} retry={refetch} />

  // The server now reports how many matched rather than how many fitted, so
  // the two disagreeing is exactly the signal that the feed was truncated.
  const capped = !!data && data.total > rows.length

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-border bg-surface px-4 py-2">
        <h1 className="mr-2 font-condensed text-[17px] font-semibold tracking-[.02em] text-ink">
          Events
        </h1>

        {/* The same badge, in the same place, as every resource list — except
            that this one is also the control, because this feed refreshes on a
            timer the reader is allowed to stop. Held, it carries the age of
            what is on screen: a paused feed mistaken for a current one is the
            whole risk of offering the button at all. */}
        <LiveIndicator
          state={live ? 'live' : 'paused'}
          detail={
            live ? undefined : (
              <Age timestamp={dataUpdatedAt ? new Date(dataUpdatedAt).toISOString() : undefined} />
            )
          }
          onToggle={() => setLive((on) => !on)}
          title={
            live
              ? 'Refreshing every 15 seconds. Pause to hold the rows still while you read.'
              : 'Paused. Nothing moves until you refresh or resume.'
          }
        />

        {data && (
          <span
            className="text-xs text-ink-faint tabular-nums"
            title={
              capped
                ? `${data.total.toLocaleString()} events match. The feed is capped at ${rows.length.toLocaleString()}; narrow the filter to reach the older ones.`
                : undefined
            }
          >
            {/* Saying only "500 shown" of a capped feed lets it read as the
                whole answer. The rows are newest-first, so what was dropped is
                precisely what the reader would have scrolled to find. */}
            {capped ? (
              <>
                {rows.length.toLocaleString()} of {data.total.toLocaleString()} shown, newest
                first
              </>
            ) : (
              <>{rows.length.toLocaleString()} shown</>
            )}
            {namespace && <> in {namespace}</>}
          </span>
        )}
        {isFetching && !isLoading && <Spinner className="size-3.5" />}

        <div className="flex-1" />

        <EventSearchBar query={query} onCommit={commit} columns={columns} rows={rows} />

        {/* Primary-when-on, beside the search box: the same shape every list
            toggle in this console has. */}
        <Button
          size="sm"
          variant={warningsOnly ? 'primary' : 'default'}
          aria-pressed={warningsOnly}
          title={
            warningsOnly
              ? 'Showing warnings only; click to include Normal events'
              : 'Show only Warning events'
          }
          onClick={toggleWarnings}
        >
          Warnings
        </Button>

        <Button
          size="sm"
          icon
          aria-label="Refresh"
          title="Refresh events now"
          onClick={() => refetch()}
        >
          <RefreshIcon />
        </Button>
      </div>

      {whereTerms.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5 border-b border-border bg-surface/60 px-4 py-1.5">
          <Eyebrow as="span" className="mr-0.5">
            Filtering
          </Eyebrow>
          {whereTerms.map((term) => (
            <button
              key={term}
              type="button"
              onClick={() => commit(removeWhereTerm(query, term))}
              title={`Remove ${term} from the search`}
              aria-label={`Remove ${term} from the search`}
              // Rounded, unlike the square controls around it: these are not
              // controls but the values themselves.
              className="group inline-flex h-7 items-center gap-1.5 rounded-full bg-accent/15 px-3 font-mono text-xs text-ink-muted ring-1 ring-accent/45 transition-colors hover:bg-accent/25 hover:text-ink"
            >
              {term}
              <span aria-hidden className="text-ink-faint group-hover:text-danger">
                ×
              </span>
            </button>
          ))}
          {(whereTerms.length > 1 || q !== '') && (
            <Button size="sm" variant="ghost" onClick={() => commit({ q: '', where: [] })}>
              Clear all
            </Button>
          )}
        </div>
      )}

      {/* What the feed is made of. An event stream is repetitive — one
          crash-looping pod writes the same reason a hundred times — so the
          length of the page says little about what is happening, and the
          reasons behind it say most of it. Each is also the filter term for
          itself, which is how anyone discovers the vocabulary without already
          knowing it. */}
      {summary.reasons.length > 0 && (
        <div className="flex items-center gap-2 overflow-x-auto border-b border-border bg-surface/40 px-4 py-1.5 text-xs">
          <Eyebrow
            as="span"
            className="shrink-0"
            // The tally is over the rows on screen, and on a capped feed that
            // is not the same set as the matches. Saying which it is costs one
            // tooltip; getting it wrong costs the reader their conclusion.
          >
            {capped ? `In the ${rows.length.toLocaleString()} shown` : 'In view'}
          </Eyebrow>
          <span className="flex shrink-0 items-center gap-1.5 tabular-nums">
            {summary.warnings > 0 && (
              <Badge tone="warn" title="Warning events among the rows shown">
                {summary.warnings.toLocaleString()} warning
              </Badge>
            )}
            {summary.normal > 0 && (
              <span className="text-ink-faint">{summary.normal.toLocaleString()} normal</span>
            )}
          </span>
          <span aria-hidden className="h-4 w-px shrink-0 bg-border" />
          {summary.reasons.map((tally) => {
            const term = valueTerm('reason', tally.reason)
            const applied = whereTerms.includes(term)
            return (
              <button
                key={tally.reason}
                type="button"
                aria-pressed={applied}
                title={
                  applied
                    ? `Stop filtering by ${tally.reason}`
                    : `Show only ${tally.reason} events`
                }
                onClick={() =>
                  commit(applied ? removeWhereTerm(query, term) : addWhereTerm(query, term))
                }
                className={reasonChipClass(applied, tally.warnings > 0)}
              >
                {tally.reason}
                <span className="text-ink-faint tabular-nums">{tally.count}</span>
              </button>
            )
          })}
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-auto">
        {isLoading ? (
          <Loading label="Loading events" />
        ) : (
          <DataTable
            columns={columns}
            rows={rows}
            onRowClick={openInvolved}
            emptyTitle={filtering ? 'No matches' : 'No recent events'}
            emptyDescription={
              filtering ? (
                <>
                  Nothing in this cluster's events matches the search.{' '}
                  <button
                    type="button"
                    className="text-accent-text underline underline-offset-2 hover:text-accent-text-hover"
                    onClick={() => commit({ q: '', where: [] })}
                  >
                    Clear the search
                  </button>{' '}
                  to see the feed again.
                </>
              ) : (
                'Events expire after about an hour, so quiet is normal.'
              )
            }
          />
        )}
      </div>
    </div>
  )
}

/** A reason chip, toned by whether that reason is carrying warnings. */
function reasonChipClass(applied: boolean, warning: boolean): string {
  const base =
    'inline-flex h-6 shrink-0 items-center gap-1.5 rounded-full px-2.5 font-mono text-[11px] ring-1 transition-colors'
  if (applied) return `${base} bg-accent/20 text-ink ring-accent/45`
  if (warning) return `${base} bg-warn/10 text-warn ring-warn/25 hover:bg-warn/20`
  return `${base} bg-canvas text-ink-muted ring-border hover:text-ink hover:ring-border-strong`
}
