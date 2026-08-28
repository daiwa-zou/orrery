import type { KubeObject } from '../api/types'

/**
 * Where an ephemeral debug container is in its short life.
 *
 * `absent` and `starting` mean the same thing to the kubelet — no process to
 * attach to yet — but they are different to the person waiting. Absent is the
 * node not having acknowledged the container at all; starting is the node
 * working on it, and carrying the reason why it is taking a moment.
 *
 * `unstartable` is the one that is not about the container at all: the pod has
 * finished, so nothing in it will start again. It looks exactly like `absent`
 * in the pod's status — which is why waiting on it used to go on forever.
 */
export type DebugPhase = 'absent' | 'starting' | 'running' | 'terminated' | 'unstartable'

export interface DebugContainerState {
  phase: DebugPhase
  /** The reason behind the phase, when the kubelet gave one. */
  detail?: string
}

/**
 * Reads one ephemeral container's state out of a pod's status.
 *
 * This is the fact the terminal has to wait for. Adding an ephemeral
 * container only writes it into the pod's spec; the kubelet still has to pull
 * the image and start it, and an exec that arrives first fails with
 * `container not found` — a dead end in the UI, because the attach is not
 * retried. The pod watch already delivers status updates to the detail page,
 * so waiting is a matter of reading them rather than polling.
 */
export function debugContainerState(
  obj: KubeObject | undefined,
  name: string,
): DebugContainerState {
  const status = obj?.status as
    | { ephemeralContainerStatuses?: Record<string, unknown>[] }
    | undefined
  const found = (status?.ephemeralContainerStatuses ?? []).find((s) => s.name === name)
  if (!found) return finished(obj) ?? { phase: 'absent' }

  const state = (found.state ?? {}) as Record<string, Record<string, unknown> | undefined>

  if (state.running) return { phase: 'running' }

  if (state.terminated) {
    const t = state.terminated
    // Exit code first: a debug image that exits immediately is the common
    // failure, and the code says more about it than the reason word does.
    const parts = [`exit ${t.exitCode ?? '?'}`]
    if (t.reason) parts.push(String(t.reason))
    if (t.message) parts.push(String(t.message))
    return { phase: 'terminated', detail: parts.join(' — ') }
  }

  // A pod that has finished has a kubelet that has stopped acting on it, so a
  // container still waiting there is waiting for nothing.
  const done = finished(obj)
  if (done) return done

  if (state.waiting) {
    const w = state.waiting
    const reason = w.reason ? String(w.reason) : undefined
    const message = w.message ? String(w.message) : undefined
    return {
      phase: 'starting',
      detail: reason && message ? `${reason} — ${message}` : (reason ?? message),
    }
  }

  return { phase: 'absent' }
}

/**
 * The state of a debug container in a pod that has finished, or undefined if
 * the pod is still running.
 *
 * Succeeded and Failed are both terminal: the kubelet will not pull an image
 * or start a container for a pod it has finished with. Nothing in the pod's
 * status says so about the container itself — it simply never appears, or
 * never leaves Waiting — so the pod's own phase is the only thing that can
 * end the wait.
 */
function finished(obj: KubeObject | undefined): DebugContainerState | undefined {
  const phase = (obj?.status as { phase?: string } | undefined)?.phase
  if (phase !== 'Succeeded' && phase !== 'Failed') return undefined
  return {
    phase: 'unstartable',
    detail:
      `This pod has finished (${phase}), so the node will not start a debug ` +
      'container in it. Debug a running pod, or start a new one from the same workload.',
  }
}

/**
 * Whether a phase can still become `running` on its own.
 *
 * A pull that is backing off is still waiting as far as the kubelet is
 * concerned, so this deliberately does not treat a slow start as a failure —
 * the reason is shown instead, and leaving is the viewer's call. A pod that
 * has finished is a different matter: there is no start to wait for, and
 * saying "waiting for the node" about it is untrue.
 */
export function debugStillStarting(state: DebugContainerState): boolean {
  return state.phase === 'absent' || state.phase === 'starting'
}

/** How many times an attach may be retried, and how long between attempts. */
export const ATTACH_RETRIES = 5
export const ATTACH_RETRY_MS = 1000

/**
 * Whether an exec error is the transient one worth reconnecting for.
 *
 * A container can be in the pod spec before the kubelet has started it — a
 * debug container between creation and its image pull, or any container
 * mid-restart — and exec answers that window with `container not found`.
 * Everything else, a denied permission above all, must fail on the first
 * attempt rather than loop.
 */
export function shouldRetryAttach(message: string, attempts: number): boolean {
  return attempts < ATTACH_RETRIES && /container not found/i.test(message)
}
