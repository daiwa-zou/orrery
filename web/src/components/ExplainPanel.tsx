import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'
import { Badge, ErrorState, Spinner } from './primitives'

/**
 * kubectl explain, as a drill-down panel. Field docs come from the cluster's
 * own OpenAPI document, so they match the server version and cover CRDs.
 */
export function ExplainPanel({
  cluster,
  group,
  version,
  kind,
}: {
  cluster: string
  group: string
  version: string
  kind: string
}) {
  const [fieldPath, setFieldPath] = useState('')

  const query = useQuery({
    queryKey: ['explain', cluster, group, version, kind, fieldPath],
    queryFn: ({ signal }) =>
      api.explain(cluster, { group, version, kind, field: fieldPath || undefined }, signal),
    staleTime: 5 * 60_000,
  })

  const crumbs = fieldPath ? fieldPath.split('.') : []

  return (
    <div className="flex h-full w-96 shrink-0 flex-col border-l border-border bg-surface">
      <div className="border-b border-border px-3 py-2">
        <p className="text-xs font-semibold tracking-wide text-ink-faint uppercase">
          Field reference
        </p>
        <nav className="mt-1 flex flex-wrap items-center gap-1 font-mono text-xs">
          <button
            className="text-accent hover:underline"
            onClick={() => setFieldPath('')}
          >
            {kind}
          </button>
          {crumbs.map((part, i) => (
            <span key={i} className="flex items-center gap-1">
              <span className="text-ink-faint">.</span>
              <button
                className="text-accent hover:underline"
                onClick={() => setFieldPath(crumbs.slice(0, i + 1).join('.'))}
              >
                {part}
              </button>
            </span>
          ))}
        </nav>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto p-3">
        {query.isLoading && (
          <p className="flex items-center gap-2 text-sm text-ink-faint">
            <Spinner className="size-3.5" /> Reading the API schema
          </p>
        )}
        {query.error != null && <ErrorState error={query.error} retry={query.refetch} />}
        {query.data && (
          <>
            {query.data.description && (
              <p className="mb-3 text-xs whitespace-pre-wrap text-ink-muted">
                {query.data.description}
              </p>
            )}
            <ul className="space-y-2">
              {(query.data.fields ?? []).map((f) => (
                <li key={f.name} className="rounded-md bg-surface-2 p-2 ring-1 ring-border">
                  <div className="flex items-baseline gap-2">
                    {f.hasChildren ? (
                      <button
                        className="font-mono text-xs font-semibold text-accent hover:underline"
                        onClick={() =>
                          setFieldPath(fieldPath ? `${fieldPath}.${f.name}` : f.name)
                        }
                      >
                        {f.name} ›
                      </button>
                    ) : (
                      <span className="font-mono text-xs font-semibold text-ink">{f.name}</span>
                    )}
                    <span className="font-mono text-[11px] text-ink-faint">{f.type}</span>
                    {f.required && <Badge tone="warn">required</Badge>}
                  </div>
                  {f.description && (
                    <p className="mt-1 line-clamp-4 text-[11px] whitespace-pre-wrap text-ink-muted">
                      {f.description}
                    </p>
                  )}
                </li>
              ))}
            </ul>
            {(query.data.fields ?? []).length === 0 && (
              <p className="text-sm text-ink-faint">
                A scalar {query.data.type || 'value'} — nothing to drill into.
              </p>
            )}
          </>
        )}
      </div>
    </div>
  )
}
