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
  /**
   * The cluster answered, and the answer is that this viewer may not read
   * metrics.
   *
   * Distinct from `unreachable`, which is where a 403 used to land — so a
   * permission the viewer will never hold was drawn as a transient failure
   * with a "Try again" link beside it. That is visible on any cluster in
   * impersonation mode where the user is not bound to metrics.k8s.io: the
   * panel reads "you are not allowed to list nodes cluster-wide · Try again",
   * and trying again does the same thing forever. The tiles either side of it
   * get this right — countSummary has carried Forbidden separately from
   * Unavailable since the beginning — and this was the panel the distinction
   * had not reached.
   */
  | { kind: 'forbidden'; reason: string }
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

/**
 * Recognises the API client's 403 without importing it.
 *
 * Read structurally, like `message` below, so MetricsQueryLike stays the plain
 * shape it advertises and a test can hand this a bare `{ status: 403 }`.
 */
function isForbidden(reason: unknown): boolean {
  const withStatus = reason as { status?: unknown } | null | undefined
  return !!withStatus && withStatus.status === 403
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
    if (isForbidden(query.error)) {
      return { kind: 'forbidden', reason: message(query.error, 'You may not read metrics on this cluster.') }
    }
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
