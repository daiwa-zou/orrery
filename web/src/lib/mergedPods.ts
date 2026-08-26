/**
 * How many pods a merged log feed carries, and which ones.
 *
 * The server refuses more than twenty pods on one request, and it is right to:
 * each is a stream held open against the API server for as long as someone is
 * watching. Matching the ceiling on this side means a caller sees a clear
 * notice rather than a 400 from a request the UI should never have made.
 */
export const MAX_MERGED_PODS = 20

export interface MergedPodChoice {
  pods: string[]
  /** How many matching pods were left out, so the UI can say so out loud. */
  dropped: number
}

/**
 * Picks the pods a merged feed should carry.
 *
 * Sorted by name and then truncated, rather than taken in arrival order. The
 * list comes from a shared cache in whatever order it yields, so an arbitrary
 * twenty would be a *different* arbitrary twenty after the next update — and a
 * feed that silently swaps which replicas it shows is worse than one that
 * shows fewer and admits it. Sorting also makes the resulting stream key
 * stable, so an equal list does not reconnect the socket.
 */
export function chooseMergedPods(names: string[]): MergedPodChoice {
  const sorted = [...names].sort()
  if (sorted.length <= MAX_MERGED_PODS) return { pods: sorted, dropped: 0 }
  return {
    pods: sorted.slice(0, MAX_MERGED_PODS),
    dropped: sorted.length - MAX_MERGED_PODS,
  }
}
