import clsx from 'clsx'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { wsURL } from '../api/client'
import type { LogMessage } from '../api/types'
import { DownloadIcon, FollowIcon } from './icons'
import { Badge, Button, Spinner } from './primitives'

/** How many lines to keep in the DOM. Beyond this the browser, not the
 *  cluster, becomes the bottleneck. */
const MAX_LINES = 5000

interface LogViewerProps {
  cluster: string
  namespace: string
  pod: string
  containers: string[]
  /** Pre-selects a container, e.g. the row clicked in the containers table. */
  initialContainer?: string
}

/** How long incoming lines are coalesced before one state update. */
const FLUSH_MS = 80

export function LogViewer({
  cluster,
  namespace,
  pod,
  containers,
  initialContainer,
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
        pod,
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
        pending.push(...msg.lines)
        if (flushTimer === undefined) flushTimer = window.setTimeout(flush, FLUSH_MS)
      } else if (msg.type === 'EOF') {
        setStatus('ended')
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
  }, [cluster, namespace, pod, container, previous, timestamps])

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
    a.download = `${pod}-${container}.log`
    document.body.appendChild(a)
    a.click()
    a.remove()
    // Revoking synchronously can abort the download before it starts.
    window.setTimeout(() => URL.revokeObjectURL(url), 10_000)
  }

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2">
        {containers.length > 1 && (
          <select
            value={container}
            onChange={(e) => setContainer(e.target.value)}
            aria-label="Container"
            className="rounded bg-surface-2 px-2 py-1 text-xs text-ink ring-1 ring-border"
          >
            {containers.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
        )}

        <Badge tone={status === 'streaming' ? 'ok' : status === 'error' ? 'danger' : 'idle'}>
          {status}
        </Badge>

        <input
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="Filter lines"
          aria-label="Filter log lines"
          className="w-48 rounded bg-surface-2 px-2 py-1 text-xs text-ink ring-1 ring-border placeholder:text-ink-faint"
        />

        <label className="flex items-center gap-1 text-xs text-ink-muted">
          <input type="checkbox" checked={wrap} onChange={(e) => setWrap(e.target.checked)} />
          Wrap
        </label>
        <label className="flex items-center gap-1 text-xs text-ink-muted">
          <input
            type="checkbox"
            checked={timestamps}
            onChange={(e) => setTimestamps(e.target.checked)}
          />
          Timestamps
        </label>
        <label
          className="flex items-center gap-1 text-xs text-ink-muted"
          title="Show logs from the previous container instance — the only way to read why a crashed container died"
        >
          <input
            type="checkbox"
            checked={previous}
            onChange={(e) => setPrevious(e.target.checked)}
          />
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

      <div
        ref={scrollRef}
        onScroll={onScroll}
        className="min-h-0 flex-1 overflow-auto bg-canvas px-3 py-2 font-mono text-xs leading-[1.5]"
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
            className={clsx('text-ink-muted', wrap ? 'break-all whitespace-pre-wrap' : 'whitespace-pre')}
          >
            {line}
          </div>
        ))}
      </div>
    </div>
  )
}
