import { useMemo } from 'react'

import { useLiveList } from '../api/hooks'
import type { KubeObject } from '../api/types'
import { chooseMergedPods, MAX_MERGED_PODS } from '../lib/mergedPods'
import { containerNamesOf, defaultContainerOf } from '../lib/podTemplate'
import { LogViewer } from './LogViewer'
import { EmptyState, ErrorState, Loading } from './primitives'

interface WorkloadLogsProps {
  cluster: string
  namespace: string
  /** The workload, for its container names and its display name. */
  workload: KubeObject
  /** The label selector that finds its pods; never empty when this renders. */
  selector: string
}

/**
 * Streams every pod of a workload as one feed.
 *
 * The backend has merged many pods into one stream since the log endpoint was
 * written, and the viewer has known how to render it — each line tagged with
 * the pod it came from — but nothing ever asked for more than one pod, because
 * only a Pod page offered a Logs tab. During an incident the question is
 * almost never "what is this one replica saying"; it is "what are all twelve
 * saying", and answering it meant opening twelve tabs.
 *
 * Pod membership comes from a live list, so a rollout's new pods join the feed
 * as they are created. The viewer keys its socket on the *contents* of that
 * list, so a re-render that yields an equal list does not reconnect.
 */
export function WorkloadLogs({ cluster, namespace, workload, selector }: WorkloadLogsProps) {
  const ref = useMemo(
    () => ({ cluster, group: '', version: 'v1', resource: 'pods' }),
    [cluster],
  )
  const params = useMemo(
    () => ({ namespace, labelSelector: selector, pageSize: 100, sort: 'name' as const }),
    [namespace, selector],
  )
  const { data, isLoading, error, refetch } = useLiveList(ref, params)

  const names = useMemo(() => {
    const rows = data?.items ?? []
    return rows
      .map((row) => row.name)
      .filter((n): n is string => typeof n === 'string' && n !== '')
  }, [data])

  const { pods, dropped } = useMemo(() => chooseMergedPods(names), [names])

  const containers = useMemo(() => containerNamesOf(workload), [workload])
  const initial = useMemo(() => defaultContainerOf(workload), [workload])

  if (isLoading) return <Loading label="Finding pods" />
  if (error) return <ErrorState error={error} retry={refetch} />

  if (pods.length === 0) {
    return (
      <EmptyState
        title="No pods"
        description={
          <>
            Nothing matches <code className="text-ink-muted">{selector}</code> in this namespace.
            A workload scaled to zero, or one whose pods have not been created yet, has no logs
            to show.
          </>
        }
      />
    )
  }

  return (
    <div className="flex h-full flex-col">
      {dropped > 0 && (
        <p className="border-b border-border bg-surface-2 px-3 py-2 text-xs text-warn">
          Showing the first {MAX_MERGED_PODS} pods by name; {dropped} more are not in this feed.
          Open a single pod to read one of them on its own.
        </p>
      )}
      <div className="min-h-0 flex-1">
        <LogViewer
          cluster={cluster}
          namespace={namespace}
          pod={pods}
          containers={containers}
          initialContainer={initial}
          label={workload.metadata.name}
        />
      </div>
    </div>
  )
}
