/** The state of a log stream, as LogViewer tracks it. */
export type LogStreamStatus = 'connecting' | 'streaming' | 'ended' | 'error'

/**
 * Why the log pane is empty, which is not always a fact about the pod.
 *
 * "No output yet" was shown for every state but `connecting`, and it is a
 * claim: the container is running and has not said anything. It is true while
 * streaming. It is false once the stream has ended — there is no "yet" — and it
 * is false when the stream failed, where the honest answer is that nothing was
 * read rather than that nothing was written.
 *
 * The failing case is not exotic. A socket refused at the handshake — an origin
 * the server does not list, which the dev config warns about by name — never
 * delivers an ERROR frame, so the viewer's `error` stays unset, no banner
 * renders, and the only other signal is a one-word badge in the toolbar. The
 * body was the part making a confident statement, and it was the part that was
 * wrong.
 *
 * `received` is how many lines arrived before filtering, and it is asked about
 * first: a stream that failed before delivering any is not a filter that
 * matched none.
 */
export function logEmptyReason(status: LogStreamStatus, filter: string, received: number): string {
  if (received > 0) {
    return filter ? 'No lines match the filter.' : 'No output.'
  }
  switch (status) {
    case 'error':
      return 'The log stream failed before any output arrived, so this is not a sign the container is quiet.'
    case 'ended':
      return 'The log stream ended without any output.'
    default:
      return 'No output yet.'
  }
}
