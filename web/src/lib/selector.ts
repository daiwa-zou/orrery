/**
 * Turning a workload's `spec.selector` into the selector string the API takes.
 *
 * This is the edge between "a Deployment" and "its pods", and it is walked by
 * anything that wants the second given the first — the jump to a filtered pod
 * list, the merged log feed. Kubernetes spells the relationship two ways and a
 * half: modern controllers carry a `LabelSelector` with `matchLabels` and
 * `matchExpressions`, ReplicationController predates that type and carries a
 * bare map, and Service carries a bare map by design rather than by age.
 */

/** The kinds whose `spec.selector` selects pods. */
export const POD_OWNERS = new Set([
  'Deployment',
  'StatefulSet',
  'DaemonSet',
  'ReplicaSet',
  'ReplicationController',
  'Job',
])

interface MatchExpression {
  key?: unknown
  operator?: unknown
  values?: unknown
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

/**
 * Renders one `matchExpressions` entry, or undefined when it cannot be
 * expressed.
 *
 * `In` and `NotIn` with no values are the interesting case: the API server
 * rejects them, and `key in ()` is not parseable either, so there is no string
 * that means what the object says. Returning undefined lets the caller decline
 * to answer rather than emit a selector that means something else.
 */
function renderExpression(expr: MatchExpression): string | undefined {
  const key = typeof expr.key === 'string' ? expr.key : ''
  if (!key) return undefined
  const operator = typeof expr.operator === 'string' ? expr.operator : ''
  const values = Array.isArray(expr.values)
    ? expr.values.filter((v): v is string => typeof v === 'string')
    : []

  switch (operator) {
    case 'In':
      return values.length > 0 ? `${key} in (${values.join(',')})` : undefined
    case 'NotIn':
      return values.length > 0 ? `${key} notin (${values.join(',')})` : undefined
    case 'Exists':
      return key
    case 'DoesNotExist':
      return `!${key}`
    default:
      return undefined
  }
}

/**
 * Renders a selector as the string the list and watch endpoints accept.
 *
 * Returns '' when the object selects nothing this can express — no selector at
 * all, or an expression with no legal spelling. Callers must treat '' as "do
 * not offer the jump" rather than as "no filter": an empty `labelSelector` is
 * dropped from the query string, so passing it on would quietly widen the
 * question from *this workload's pods* to *every pod in the namespace*.
 */
export function renderSelector(selector: unknown): string {
  if (!isRecord(selector)) return ''

  const hasLabelSelectorShape =
    'matchLabels' in selector || 'matchExpressions' in selector

  // ReplicationController and Service carry the labels directly.
  const labels = hasLabelSelectorShape
    ? isRecord(selector.matchLabels)
      ? selector.matchLabels
      : {}
    : selector

  const terms: string[] = []
  // Sorted so the same selector renders identically every time: this string
  // ends up in a query key and a WebSocket URL, and an unstable one would
  // refetch and reconnect for no reason.
  for (const key of Object.keys(labels).sort()) {
    const value = labels[key]
    if (typeof value !== 'string') continue
    terms.push(`${key}=${value}`)
  }

  if (hasLabelSelectorShape && Array.isArray(selector.matchExpressions)) {
    for (const raw of selector.matchExpressions) {
      if (!isRecord(raw)) continue
      const rendered = renderExpression(raw as MatchExpression)
      // One unexpressible requirement makes the whole selector unexpressible.
      // Dropping it would return a *wider* selector than the object means,
      // which is the one failure mode worth refusing outright.
      if (rendered === undefined) return ''
      terms.push(rendered)
    }
  }

  return terms.join(',')
}

/**
 * The selector a workload uses to own its pods, or '' when the kind does not
 * own pods or its selector cannot be expressed.
 */
export function podSelectorOf(kind: string, spec: unknown): string {
  if (!POD_OWNERS.has(kind)) return ''
  if (!isRecord(spec)) return ''
  return renderSelector(spec.selector)
}
