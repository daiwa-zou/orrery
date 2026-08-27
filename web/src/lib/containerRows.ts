import type { KubeObject } from '../api/types'

/**
 * The container table on a pod page, derived from status rather than spec.
 *
 * Pure enough to live away from the component that draws it, which is the
 * point: "why is this pod restarting?" is answered by three facts buried in
 * the status JSON — the current state, the restart count and how the previous
 * instance exited — and getting them out of there is worth testing on its own.
 */

export interface ContainerRow {
  name: string
  image: string
  ready: boolean
  restarts: number
  state: string
  stateDetail?: string
  lastExit?: string
  init: boolean
}

/**
 * Flattens containerStatuses into the table that answers "why is this pod
 * restarting?" — the state, the restart count and the last exit code are the
 * three facts otherwise buried in the status JSON.
 */
export function containerRows(obj?: KubeObject): ContainerRow[] {
  if (!obj) return []
  const status = obj.status as {
    containerStatuses?: Record<string, unknown>[]
    initContainerStatuses?: Record<string, unknown>[]
  }

  const toRow = (s: Record<string, unknown>, init: boolean): ContainerRow => {
    const state = (s.state ?? {}) as Record<string, Record<string, unknown> | undefined>
    let name = 'Unknown'
    let detail: string | undefined
    if (state.running) {
      name = 'Running'
    } else if (state.waiting) {
      name = String(state.waiting.reason ?? 'Waiting')
      detail = state.waiting.message as string | undefined
    } else if (state.terminated) {
      name = String(state.terminated.reason ?? 'Terminated')
      detail = state.terminated.message as string | undefined
    }

    const last = (s.lastState as Record<string, Record<string, unknown>> | undefined)?.terminated
    let lastExit: string | undefined
    if (last) {
      lastExit = `exit ${last.exitCode}`
      if (last.reason) lastExit += ` (${last.reason})`
    }

    return {
      name: String(s.name ?? ''),
      image: String(s.image ?? ''),
      ready: s.ready === true,
      restarts: Number(s.restartCount ?? 0),
      state: name,
      stateDetail: detail,
      lastExit,
      init,
    }
  }

  return [
    ...(status?.initContainerStatuses ?? []).map((s) => toRow(s, true)),
    ...(status?.containerStatuses ?? []).map((s) => toRow(s, false)),
  ]
}
