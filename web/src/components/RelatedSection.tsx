import { Link } from 'react-router-dom'

import type { ResourceRef } from '../api/client'
import { useRelated } from '../api/hooks'
import type { ObjectRef } from '../api/types'
import { groupRelations, relatedHref } from '../lib/related'
import { Corners, Eyebrow, Spinner, StatusBadge } from './primitives'

/**
 * Everything attached to the object on screen, in one place.
 *
 * The overview already listed `metadata.ownerReferences`, which answers a
 * quarter of the question and guesses at the answer: a link built from a kind
 * has to pluralise it, and a CRD may spell its plural however it likes. The
 * server walks the same edges for its own tables and rollout history, so it
 * can answer properly — owners all the way up, children all the way down, the
 * node a pod runs on, the services that select it, the ConfigMaps and Secrets
 * its spec names — with each resource resolved through discovery rather than
 * guessed.
 *
 * A scan the caller may not run comes back as a warning rather than an absence,
 * and those are shown: "no pods" and "I could not look" are different answers,
 * and only one of them means the workload is idle.
 */
export function RelatedSection({
  subject,
  enabled,
  fallbackOwners,
}: {
  // Not named `ref`: React reserves that prop, and a component that only works
  // because the runtime happens to forward it today is a trap for the next
  // person to touch it.
  subject: ResourceRef
  enabled: boolean
  /**
   * The object's own `metadata.ownerReferences`, already in hand. Shown while
   * the walk is in flight and kept if it fails, so this section is never worse
   * than the plain owner list it replaces — the links are the guessed kind
   * plural in that state, which is exactly what they were before.
   */
  fallbackOwners?: ObjectRef[]
}) {
  const { data, isLoading } = useRelated(subject, enabled)

  if (!enabled) return null

  // A neighbourhood is a supplement, never the reason the page exists: a failed
  // walk falls back rather than raising an error state over someone's shoulder,
  // since the object itself is perfectly readable above.
  const groups = groupRelations(data?.related ?? fallbackOwners)
  const warnings = data?.warnings ?? []

  if (!isLoading && groups.length === 0 && warnings.length === 0) return null

  return (
    <section className="blueprint bg-surface p-3.5">
      <Corners />
      <Eyebrow className="mb-2 flex items-center gap-2">
        Related
        {isLoading && <Spinner className="h-3 w-3" />}
      </Eyebrow>

      {groups.length > 0 && (
        <dl className="space-y-1">
          {groups.map((group) => (
            <div key={group.relation} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 py-1">
              <dt className="w-32 shrink-0 text-xs text-ink-faint">{group.label}</dt>
              <dd className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
                {group.refs.map((r) => (
                  <RelatedLink key={`${r.kind}/${r.namespace ?? ''}/${r.name}`} cluster={subject.cluster} item={r} />
                ))}
              </dd>
            </div>
          ))}
        </dl>
      )}

      {warnings.length > 0 && (
        <ul className="mt-2 space-y-0.5 border-t border-border pt-2">
          {warnings.map((w) => (
            <li key={w} className="text-xs text-warn">
              {w}
            </li>
          ))}
        </ul>
      )}

      {data?.truncated && (
        <p className="mt-2 text-xs text-ink-faint">
          Only the first objects are listed; this one has more neighbours than fit here.
        </p>
      )}
    </section>
  )
}

function RelatedLink({ cluster, item }: { cluster: string; item: ObjectRef }) {
  const href = relatedHref(cluster, item)
  const label = `${item.kind}/${item.name}`

  // No route means the server could not resolve the resource — a kind this
  // cluster does not serve, most often. Naming it is still the useful answer,
  // since an owner that no longer exists is usually why someone is looking.
  const body = href ? (
    <Link to={href} className="text-accent-text hover:text-accent-text-hover hover:underline">
      {label}
    </Link>
  ) : (
    <span className="text-ink-muted" title={item.note}>
      {label}
    </span>
  )

  return (
    <span className="inline-flex items-center gap-1.5" title={item.note}>
      {body}
      {item.status && <StatusBadge value={item.status} />}
    </span>
  )
}
