import { describe, expect, it } from 'vitest'

import type { KubeObject } from '../api/types'
import {
  ATTACH_RETRIES,
  debugContainerState,
  debugStillStarting,
  shouldRetryAttach,
} from './debugContainer'

function pod(status: unknown): KubeObject {
  return { kind: 'Pod', metadata: { name: 'p' }, status } as KubeObject
}

describe('debugContainerState', () => {
  // The case this exists for. UpdateEphemeralContainers has returned, so the
  // container is in the spec, but the kubelet has not reported it yet — and
  // an exec sent now fails with `container not found`.
  it('reports absent when the node has not acknowledged the container', () => {
    expect(debugContainerState(pod({ containerStatuses: [] }), 'debugger-4cbfk')).toEqual({
      phase: 'absent',
    })
    expect(debugContainerState(undefined, 'debugger-4cbfk')).toEqual({ phase: 'absent' })
  })

  it('reports running once there is a process to attach to', () => {
    const state = debugContainerState(
      pod({
        ephemeralContainerStatuses: [
          { name: 'debugger-4cbfk', state: { running: { startedAt: '2026-08-27T00:00:00Z' } } },
        ],
      }),
      'debugger-4cbfk',
    )
    expect(state).toEqual({ phase: 'running' })
  })

  it('carries the waiting reason so a slow pull explains itself', () => {
    const state = debugContainerState(
      pod({
        ephemeralContainerStatuses: [
          {
            name: 'debugger-4cbfk',
            state: { waiting: { reason: 'ImagePullBackOff', message: 'Back-off pulling image' } },
          },
        ],
      }),
      'debugger-4cbfk',
    )
    expect(state.phase).toBe('starting')
    expect(state.detail).toBe('ImagePullBackOff — Back-off pulling image')
  })

  it('reports a debug image that exited instead of waiting forever', () => {
    const state = debugContainerState(
      pod({
        ephemeralContainerStatuses: [
          { name: 'debugger-4cbfk', state: { terminated: { exitCode: 1, reason: 'Error' } } },
        ],
      }),
      'debugger-4cbfk',
    )
    expect(state.phase).toBe('terminated')
    expect(state.detail).toBe('exit 1 — Error')
  })

  // Two debug attempts on one pod: ephemeral containers cannot be removed, so
  // the statuses accumulate and the wrong one is easy to read.
  it('picks the named container out of several', () => {
    const status = {
      ephemeralContainerStatuses: [
        { name: 'debugger-aaaaa', state: { terminated: { exitCode: 0 } } },
        { name: 'debugger-4cbfk', state: { running: {} } },
      ],
    }
    expect(debugContainerState(pod(status), 'debugger-4cbfk').phase).toBe('running')
    expect(debugContainerState(pod(status), 'debugger-aaaaa').phase).toBe('terminated')
  })

  it('treats an empty state object as not yet started', () => {
    const state = debugContainerState(
      pod({ ephemeralContainerStatuses: [{ name: 'debugger-4cbfk', state: {} }] }),
      'debugger-4cbfk',
    )
    expect(state.phase).toBe('absent')
  })
})

describe('debugStillStarting', () => {
  it('keeps waiting through a backing-off pull, but not past an exit', () => {
    expect(debugStillStarting({ phase: 'absent' })).toBe(true)
    expect(debugStillStarting({ phase: 'starting', detail: 'ImagePullBackOff' })).toBe(true)
    expect(debugStillStarting({ phase: 'running' })).toBe(false)
    expect(debugStillStarting({ phase: 'terminated' })).toBe(false)
  })
})

describe('shouldRetryAttach', () => {
  // The exact message the API server sends while the kubelet is still
  // starting the container, quoted from a live failure.
  const notFound =
    'Internal error occurred: unable to upgrade connection: container not found ("debugger-4cbfk")'

  it('retries the container-not-found window', () => {
    expect(shouldRetryAttach(notFound, 0)).toBe(true)
    expect(shouldRetryAttach(notFound, ATTACH_RETRIES - 1)).toBe(true)
  })

  it('gives up rather than looping forever', () => {
    expect(shouldRetryAttach(notFound, ATTACH_RETRIES)).toBe(false)
  })

  // The failure that must never loop: retrying a denial re-asks a question
  // the cluster has already answered, and buries the answer under reconnects.
  it('does not retry a permission error', () => {
    expect(
      shouldRetryAttach('pods "kube-proxy-kmc7l" is forbidden: User cannot create pods/exec', 0),
    ).toBe(false)
    expect(shouldRetryAttach('stream error', 0)).toBe(false)
  })
})

describe('a pod that has finished', () => {
  // The hang this fixes: an ephemeral container added to a Succeeded pod never
  // appears in the status, because the kubelet is done with the pod. That is
  // indistinguishable from "the node has not got to it yet", so the terminal
  // waited for a start that was never coming.
  const finishedPod = (phase: string) =>
    ({ metadata: { name: 'p' }, status: { phase } }) as unknown as KubeObject

  it('reports a debugger that will never start, rather than one still starting', () => {
    for (const phase of ['Succeeded', 'Failed']) {
      const state = debugContainerState(finishedPod(phase), 'debugger-abc')
      expect(state.phase).toBe('unstartable')
      expect(state.detail).toContain(phase)
      expect(debugStillStarting(state)).toBe(false)
    }
  })

  it('ends the wait even when the container is sitting in Waiting', () => {
    const pod = {
      metadata: { name: 'p' },
      status: {
        phase: 'Succeeded',
        ephemeralContainerStatuses: [
          { name: 'debugger-abc', state: { waiting: { reason: 'ContainerCreating' } } },
        ],
      },
    } as unknown as KubeObject
    expect(debugContainerState(pod, 'debugger-abc').phase).toBe('unstartable')
  })

  it('still prefers what the container itself said when it ran and exited', () => {
    // An exit code is more specific than "the pod is over", and it is the
    // thing that explains why the shell never appeared.
    const pod = {
      metadata: { name: 'p' },
      status: {
        phase: 'Failed',
        ephemeralContainerStatuses: [
          { name: 'debugger-abc', state: { terminated: { exitCode: 127, reason: 'Error' } } },
        ],
      },
    } as unknown as KubeObject
    const state = debugContainerState(pod, 'debugger-abc')
    expect(state.phase).toBe('terminated')
    expect(state.detail).toContain('exit 127')
  })

  it('leaves a running pod alone', () => {
    const pod = { metadata: { name: 'p' }, status: { phase: 'Running' } } as unknown as KubeObject
    const state = debugContainerState(pod, 'debugger-abc')
    expect(state.phase).toBe('absent')
    expect(debugStillStarting(state)).toBe(true)
  })
})

describe('a wait the kubelet will not resolve', () => {
  const waiting = (reason: string, message?: string) =>
    ({
      metadata: { name: 'p' },
      status: {
        phase: 'Running',
        ephemeralContainerStatuses: [{ name: 'debugger-abc', state: { waiting: { reason, message } } }],
      },
    }) as unknown as KubeObject

  it('keeps waiting through the reasons that are still trying', () => {
    for (const reason of ['ContainerCreating', 'ImagePullBackOff', 'ErrImagePull', 'PodInitializing']) {
      const state = debugContainerState(waiting(reason), 'debugger-abc')
      expect(state.phase).toBe('starting')
      expect(debugStillStarting(state)).toBe(true)
    }
  })

  it('stops on a config error, which is what a missing target container is', () => {
    // The hang: a debugger aimed at a container that is not running gets
    // CreateContainerConfigError and sits in Waiting for the life of the pod,
    // because an ephemeral container can be neither edited nor removed.
    const state = debugContainerState(
      waiting('CreateContainerConfigError', 'unable to find target container nginx'),
      'debugger-abc',
    )
    expect(state.phase).toBe('unstartable')
    expect(debugStillStarting(state)).toBe(false)
    expect(state.detail).toContain('unable to find target container nginx')
  })

  it('stops on the other reasons that describe the spec rather than the moment', () => {
    for (const reason of ['CreateContainerError', 'InvalidImageName', 'RunContainerError']) {
      expect(debugContainerState(waiting(reason), 'debugger-abc').phase).toBe('unstartable')
    }
  })
})
