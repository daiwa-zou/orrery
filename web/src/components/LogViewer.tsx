import clsx from 'clsx'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { wsURL } from '../api/client'
import type { LogMessage } from '../api/types'
import { DownloadIcon, FollowIcon } from './icons'
import { Badge, Button, Checkbox, FilterInput, Select, Spinner } from './primitives'

/** How many lines to keep in the DOM. Beyond this the browser, not the
 *  cluster, becomes the bottleneck. */
const MAX_LINES = 5000

interface LogViewerProps {
  cluster: string
  namespace: string
  /**
   * One pod, or several to merge into one feed. Merged lines are prefixed with
   * the pod they came from, which is the only way a combined view stays
   * readable when twelve replicas are all talking.
   */
  pod: string | string[]
  containers: string[]
  /** Pre-selects a container, e.g. the row clicked in the containers table. */
  initialContainer?: string
  /** Last exit per container ("exit 137 (OOMKilled)"), for the Previous banner. */
  lastExits?: Record<string, string>
  /**
   * Names the subject in the download filename. A merged feed is nobody's pod,
   * so "web-abc123-def.log" would be a lie about which twelve replicas it holds.
   */
  label?: string
}

/**
 * Colours a line by its apparent severity. Kubernetes does not tag lines with
 * their stream, so this is a heuristic over the conventional level markers —
 * cheap, and wrong only about logs that lie about themselves.
 */
function lineTone(line: string): string {
  // Level markers, plus klog's single-letter severity prefix (E0824 12:00:00).
  if (/\b(ERROR|FATAL|panic|OOMKilled)\b/.test(line) || /^[EF]\d{4} /.test(line)) {
    return 'text-danger'
  }
  if (/\bWARN(ING)?\b/.test(line) || /^W\d{4} /.test(line)) return 'text-warn'
  return 'text-ink-muted'
}

/** How long incoming lines are coalesced before one state update. */
const FLUSH_MS = 80

export function LogViewer({
  cluster,
  namespace,
  pod,
  containers,
  initialContainer,
  lastExits,
  label,
}: LogViewerProps) {
  const [container, setContainer] = useState(
    initialContainer && containers.includes(initialContainer)
      ? initialContainer
      : (containers[0] ?? ''),
  )
  // start is how many lines have been trimmed off the front; start + index is
  // a stable key, so a trim does not re-key (and re-render) every line below.
  const [buf, setBuf] = useState<{ start: number; items: string[] }>({ start: 0, items: [] })

  useEffect(() => {
    if (initialContainer && containers.includes(initialContainer)) {
      setContainer(initialContainer)
    }
  }, [initialContainer, containers])
  // Pod names are DNS labels, so a comma cannot occur in one: joining them
  // yields a key that changes when the *set* changes and not when a caller
  // happens to rebuild an array with the same contents. Depending on the array
  // itself would tear down and reopen the socket on every render of the parent
  // — losing the buffer each time — which is exactly what a workload page
  // deriving its pod list from a live query does.
  const podKey = useMemo(() => (Array.isArray(pod) ? pod : [pod]).join(','), [pod])
  const pods = useMemo(() => podKey.split(','), [podKey])
  const aggregated = pods.length > 1
  const [status, setStatus] = useState<'connecting' | 'streaming' | 'ended' | 'error'>('connecting')
  const [error, setError] = useState<string>()
  const [follow, setFollow] = useState(true)
  const [wrap, setWrap] = useState(false)
  const [previous, setPrevious] = useState(false)
  const [timestamps, setTimestamps] = useState(false)
  const [filter, setFilter] = useState('')

  const scrollRef = useRef<HTMLDivElement>(null)
  const followRef = useRef(follow)
  followRef.current = follow

  useEffect(() => {
    if (containers.length > 0 && !containers.includes(container)) {
      setContainer(containers[0])
    }
  }, [containers, container])

  useEffect(() => {
    if (!container) return

    setBuf({ start: 0, items: [] })
    setError(undefined)
    setStatus('connecting')

    // Coalesce incoming lines: a pod logging thousands of lines a second must
    // not become thousands of React renders a second.
    let pending: string[] = []
    let flushTimer: number | undefined
    const flush = () => {
      flushTimer = undefined
      const add = pending
      pending = []
      setBuf((prev) => {
        const items = prev.items.concat(add)
        const drop = items.length - MAX_LINES
        // Trim from the front so memory stays bounded during a log storm.
        if (drop > 0) return { start: prev.start + drop, items: items.slice(drop) }
        return { start: prev.start, items }
      })
    }

    // The old socket's close event arrives *after* the effect re-runs, so
    // without this guard a container switch briefly shows "ended" while the
    // new stream is still connecting.
    let stale = false

    const socket = new WebSocket(
      wsURL(`/clusters/${cluster}/ws/logs`, {
        namespace,
        pod: pods,
        container,
        previous,
        timestamps,
        tailLines: 1000,
      }),
    )

    socket.onopen = () => {
      if (!stale) setStatus('streaming')
    }

    socket.onmessage = (event) => {
      if (stale) return
      let msg: LogMessage
      try {
        msg = JSON.parse(event.data)
      } catch {
        return
      }
      if (msg.type === 'LOG') {
        // The server batches per pod, so one prefix covers the whole frame.
        pending.push(...(msg.pod ? msg.lines.map((l) => `[${msg.pod}] ${l}`) : msg.lines))
        if (flushTimer === undefined) flushTimer = window.setTimeout(flush, FLUSH_MS)
      } else if (msg.type === 'EOF') {
        setStatus('ended')
      } else if (msg.type === 'STREAM_ERROR') {
        // One pod of a merged feed failed; the rest keep streaming.
        pending.push(`[${msg.pod}] --- log stream unavailable: ${msg.reason} ---`)
      } else if (msg.type === 'ERROR') {
        setError(msg.message)
        setStatus('error')
      }
    }

    socket.onerror = () => {
      if (!stale) setStatus('error')
    }
    socket.onclose = () => {
      if (!stale) setStatus((s) => (s === 'error' ? s : 'ended'))
    }

    return () => {
      stale = true
      window.clearTimeout(flushTimer)
      socket.close()
    }
  }, [cluster, namespace, pods, container, previous, timestamps])

  // Autoscroll only while the reader is at the bottom; yanking the viewport
  // away from someone reading history is the classic log-viewer sin.
  useEffect(() => {
    if (!followRef.current) return
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [buf])

  const onScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40
    setFollow(atBottom)
  }, [])

  const visible = useMemo(() => {
    const all = buf.items.map((line, i) => ({ line, key: buf.start + i }))
    if (!filter.trim()) return all
    const needle = filter.toLowerCase()
    return all.filter(({ line }) => line.toLowerCase().includes(needle))
  }, [buf, filter])

  // Downloads what is on screen: the filtered lines when a filter is active.
  const download = () => {
    const blob = new Blob([visible.map((v) => v.line).join('\n')], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${label ?? (aggregated ? `${pods.length}-pods` : pods[0])}-${container}.log`
    document.body.appendChild(a)
    a.click()
    a.remove()
    // Revoking synchronously can abort the download before it starts.
    window.setTimeout(() => URL.revokeObjectURL(url), 10_000)
  }

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-border bg-surface px-3 py-[7px]">
        {containers.length > 1 && (
          <Select
            value={container}
            onChange={(e) => setContainer(e.target.value)}
            aria-label="Container"
          >
            {containers.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </Select>
        )}

        <Badge tone={status === 'streaming' ? 'ok' : status === 'error' ? 'danger' : 'idle'}>
          <span aria-hidden="true">{status}</span>
        </Badge>

        {aggregated && (
          <Badge tone="info" title={pods.join('\n')}>
            {pods.length} pods merged
          </Badge>
        )}
        {/* Deliberately announces the stream's state and not its contents:
            reading out every log line as it arrives would make the page
            unusable with a screen reader. */}
        <span role="status" aria-live="polite" className="sr-only">
          {status === 'streaming'
            ? `Streaming logs from ${aggregated ? `${pods.length} pods` : container || pods[0]}.`
            : status === 'connecting'
              ? 'Connecting to the log stream.'
              : status === 'ended'
                ? 'Log stream ended.'
                : 'Log stream failed.'}
        </span>

        <FilterInput
          value={filter}
          onValueChange={setFilter}
          placeholder="Filter lines"
          aria-label="Filter log lines"
          className="w-44"
        />

        <label className="flex items-center gap-1.5 text-xs text-ink-muted">
          <Checkbox checked={wrap} onChange={(e) => setWrap(e.target.checked)} />
          Wrap
        </label>
        <label className="flex items-center gap-1.5 text-xs text-ink-muted">
          <Checkbox checked={timestamps} onChange={(e) => setTimestamps(e.target.checked)} />
          Timestamps
        </label>
        <label
          className="flex items-center gap-1.5 text-xs text-ink-muted"
          title="Show logs from the previous container instance — the only way to read why a crashed container died"
        >
          <Checkbox checked={previous} onChange={(e) => setPrevious(e.target.checked)} />
          Previous
        </label>

        <div className="flex-1" />

        <span className="text-xs tabular-nums text-ink-faint">
          {visible.length.toLocaleString()}
          {filter && ` / ${buf.items.length.toLocaleString()}`} lines
        </span>

        {!follow && (
          <Button
            size="sm"
            icon
            aria-label="Follow"
            title="Jump to the newest lines and keep following"
            onClick={() => {
              setFollow(true)
              const el = scrollRef.current
              if (el) el.scrollTop = el.scrollHeight
            }}
          >
            <FollowIcon />
          </Button>
        )}
        <Button
          size="sm"
          icon
          aria-label="Download logs"
          title="Download the visible log lines"
          onClick={download}
          disabled={buf.items.length === 0}
        >
          <DownloadIcon />
        </Button>
      </div>

      {error && (
        <p className="border-b border-border bg-danger/10 px-3 py-1.5 text-xs text-danger">
          {error}
        </p>
      )}

      {previous && !error && (
        <p className="shrink-0 border-b border-border bg-info/7 px-3 py-[5px] text-xs text-info">
          Showing the previous container instance
          {lastExits?.[container]
            ? ` — this is the run that ended with ${lastExits[container]}.`
            : ' — the run before the current one.'}
        </p>
      )}

      <div
        ref={scrollRef}
        onScroll={onScroll}
        className="min-h-0 flex-1 overflow-auto bg-code px-3 py-2 font-mono text-xs leading-[1.55]"
      >
        {status === 'connecting' && buf.items.length === 0 && (
          <p className="flex items-center gap-2 text-ink-faint">
            <Spinner className="size-3" /> Attaching to {pod}
          </p>
        )}
        {status !== 'connecting' && visible.length === 0 && (
          <p className="text-ink-faint">
            {filter ? 'No lines match the filter.' : 'No output yet.'}
          </p>
        )}
        {visible.map(({ line, key }) => (
          <div
            key={key}
            className={clsx(lineTone(line), wrap ? 'break-all whitespace-pre-wrap' : 'whitespace-pre')}
          >
            {line}
          </div>
        ))}
      </div>
    </div>
  )
}
