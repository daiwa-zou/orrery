import type { MetricsResponse } from '../api/types'

/**
 * Telling "this cluster has no metrics-server" apart from "I never got an
 * answer".
 *
 * The metrics endpoints answer honestly: a cluster without metrics-server comes
 * back `{available: false}` with a plain explanation rather than a 500, exactly
 * so the console can say something true. But the cards were reading only
 * `!data?.available`, and `data` is also undefined when the request failed, was
 * cancelled, or is parked waiting to retry. All three rendered as "Metrics are
 * unavailable."
 *
 * That is not a missing answer, it is a wrong one — a claim about the cluster
 * made on the strength of never having asked it. It is visible on a healthy
 * cluster: one metrics request gets cancelled on mount, its retry parks, and
 * the page says metrics are unavailable directly above a panel listing live
 * per-pod CPU from the same metrics-server.
 */

export type MetricsState =
  /** Still asking. */
  | { kind: 'loading' }
  /** We could not ask, or never heard back. Says so, and can be retried. */
  | { kind: 'unreachable'; reason: string }
  /** The cluster answered, and the answer is that it serves no metrics. */
  | { kind: 'absent'; reason: string }
  /** There is data to draw. */
  | { kind: 'ready' }

/** The subset of a react-query result this needs. */
export interface MetricsQueryLike {
  data?: MetricsResponse
  isLoading: boolean
  isPaused?: boolean
  error?: unknown
  failureReason?: unknown
}

function message(reason: unknown, fallback: string): string {
  if (reason instanceof Error && reason.message) return reason.message
  if (typeof reason === 'string' && reason) return reason
  const withMessage = reason as { message?: unknown } | null | undefined
  if (withMessage && typeof withMessage.message === 'string' && withMessage.message) {
    return withMessage.message
  }
  return fallback
}

export function metricsState(query: MetricsQueryLike): MetricsState {
  // Data first: once there is an answer it is the truth, even while a later
  // refetch is failing. Dropping a drawn chart because a refresh errored is
  // worse than drawing one that is thirty seconds old.
  if (query.data) {
    return query.data.available
      ? { kind: 'ready' }
      : {
          kind: 'absent',
          reason: query.data.reason || 'This cluster does not serve metrics.',
        }
  }
  if (query.error) {
    return { kind: 'unreachable', reason: message(query.error, 'Could not read metrics.') }
  }
  // A parked retry does not resume on its own, so "loading" would be a spinner
  // that spins forever.
  if (query.isPaused) {
    return {
      kind: 'unreachable',
      reason: message(query.failureReason, 'Could not read metrics; the browser appears to be offline.'),
    }
  }
  if (query.isLoading) return { kind: 'loading' }
  // No data, no error, not loading, not paused: nothing was ever asked for.
  return { kind: 'unreachable', reason: 'Metrics were not requested.' }
}
