import type { CountSummary, Overview } from '../api/types'

/**
 * What a cluster's workloads are doing, for the fleet page.
 *
 * The health badge on a cluster card is the probe result: whether the API
 * server answered, how fast, and on what version. That is a real thing to
 * know, and it is not the thing the page appears to say. "2 registered · all
 * healthy" reads as *nothing needs you*, and it will say that over a cluster
 * with two pods in ImagePullBackOff and a Deployment stuck degraded, because
 * the control plane answering the phone is all it ever measured.
 *
 * This is the other half: how many pods are not running, how many workloads
 * are not healthy, and how many warnings have been recorded. It is drawn from
 * the overview endpoint, which computes all three already for the single
 * cluster page.
 */

/** Pod phases that are fine to see. Anything else is worth counting. */
const SETTLED_POD_STATUS = new Set(['Running', 'Succeeded', 'Completed'])

/** Workload rollups that mean "not currently serving what it should". */
const UNHEALTHY_WORKLOAD = new Set(['Degraded', 'Not scheduled'])

export interface FleetHealth {
  /** Pods in a phase that is neither running nor finished. */
  podsNeedingAttention: number
  /** Workloads reported Degraded or unscheduled. */
  workloadsUnhealthy: number
  /** Recent warning events, when they could be read. */
  warnings: number
  /**
   * True when some part of the answer could not be read — forbidden, or the
   * cluster could not produce it. The counts below are then a floor, not a
   * total, and the page must not present them as "all clear".
   */
  partial: boolean
  /** True when nothing at all could be determined. */
  unknown: boolean
}

function countUnsettled(summary: CountSummary | undefined, settled: Set<string>): number {
  if (!summary?.byStatus) return 0
  let n = 0
  for (const [status, count] of Object.entries(summary.byStatus)) {
    if (!settled.has(status)) n += count
  }
  return n
}

function countMatching(summary: CountSummary | undefined, wanted: Set<string>): number {
  if (!summary?.byStatus) return 0
  let n = 0
  for (const [status, count] of Object.entries(summary.byStatus)) {
    if (wanted.has(status)) n += count
  }
  return n
}

function blocked(summary: CountSummary | undefined): boolean {
  return !!summary && (!!summary.forbidden || !!summary.unavailable)
}

/**
 * Reduces an overview to what the fleet page needs.
 *
 * `unknown` when there is no overview at all; `partial` when any single count
 * was refused or unavailable. Both matter more than the numbers: a zero that
 * came from not looking must never render as a zero that came from looking.
 */
export function fleetHealth(overview: Overview | undefined): FleetHealth {
  if (!overview) {
    return { podsNeedingAttention: 0, workloadsUnhealthy: 0, warnings: 0, partial: false, unknown: true }
  }

  const workloads = Object.values(overview.workloads ?? {})
  const partial =
    blocked(overview.pods) ||
    workloads.some(blocked) ||
    !!overview.warningsForbidden ||
    !!overview.warningsUnavailable

  return {
    podsNeedingAttention: countUnsettled(overview.pods, SETTLED_POD_STATUS),
    workloadsUnhealthy: workloads.reduce((n, w) => n + countMatching(w, UNHEALTHY_WORKLOAD), 0),
    warnings: (overview.warnings ?? []).length,
    partial,
    unknown: false,
  }
}

/** True when this cluster has something a person should look at. */
export function needsAttention(h: FleetHealth): boolean {
  return h.podsNeedingAttention > 0 || h.workloadsUnhealthy > 0
}

/**
 * One line for a cluster card.
 *
 * Deliberately says "nothing to report" rather than "all healthy" when the
 * answer was incomplete, and says so out loud when it was.
 */
export function fleetHealthLabel(h: FleetHealth): string {
  if (h.unknown) return 'Workload health unavailable'

  const parts: string[] = []
  if (h.podsNeedingAttention > 0) {
    parts.push(`${h.podsNeedingAttention} pod${h.podsNeedingAttention === 1 ? '' : 's'} not running`)
  }
  if (h.workloadsUnhealthy > 0) {
    parts.push(`${h.workloadsUnhealthy} workload${h.workloadsUnhealthy === 1 ? '' : 's'} degraded`)
  }
  if (parts.length === 0) {
    if (h.partial) return 'Nothing to report from what could be read'
    return h.warnings > 0
      ? `Workloads healthy · ${h.warnings} recent warning${h.warnings === 1 ? '' : 's'}`
      : 'Workloads healthy'
  }
  const line = parts.join(' · ')
  return h.partial ? `${line} · some counts unavailable` : line
}
