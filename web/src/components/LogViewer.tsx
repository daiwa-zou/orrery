import clsx from 'clsx'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { wsURL } from '../api/client'
import type { LogMessage } from '../api/types'
import { Badge, Button, Spinner } from './primitives'

/** How many lines to keep in the DOM. Beyond this the browser, not the
 *  cluster, becomes the bottleneck. */
const MAX_LINES = 5000

interface LogViewerProps {
  cluster: string
  namespace: string
  pod: string
  containers: string[]
}

export function LogViewer({ cluster, namespace, pod, containers }: LogViewerProps) {
  const [container, setContainer] = useState(containers[0] ?? '')
  const [lines, setLines] = useState<string[]>([])
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

    setLines([])
    setError(undefined)
    setStatus('connecting')

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

    socket.onopen = () => setStatus('streaming')

    socket.onmessage = (event) => {
      let msg: LogMessage
      try {
        msg = JSON.parse(event.data)
      } catch {
        return
      }
      if (msg.type === 'LOG') {
        setLines((prev) => {
          const next = prev.concat(msg.lines)
          // Trim from the front so memory stays bounded during a log storm.
          return next.length > MAX_LINES ? next.slice(next.length - MAX_LINES) : next
        })
      } else if (msg.type === 'EOF') {
        setStatus('ended')
      } else if (msg.type === 'ERROR') {
        setError(msg.message)
        setStatus('error')
      }
    }

    socket.onerror = () => setStatus('error')
    socket.onclose = () => setStatus((s) => (s === 'error' ? s : 'ended'))

    return () => socket.close()
  }, [cluster, namespace, pod, container, previous, timestamps])

  // Autoscroll only while the reader is at the bottom; yanking the viewport
  // away from someone reading history is the classic log-viewer sin.
  useEffect(() => {
    if (!followRef.current) return
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [lines])

  const onScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40
    setFollow(atBottom)
  }, [])

  const visible = useMemo(() => {
    if (!filter.trim()) return lines
    const needle = filter.toLowerCase()
    return lines.filter((l) => l.toLowerCase().includes(needle))
  }, [lines, filter])

  const download = () => {
    const blob = new Blob([lines.join('\n')], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${pod}-${container}.log`
    a.click()
    URL.revokeObjectURL(url)
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
          {filter && ` / ${lines.length.toLocaleString()}`} lines
        </span>

        {!follow && (
          <Button
            size="sm"
            onClick={() => {
              setFollow(true)
              const el = scrollRef.current
              if (el) el.scrollTop = el.scrollHeight
            }}
          >
            Follow
          </Button>
        )}
        <Button size="sm" onClick={download} disabled={lines.length === 0}>
          Download
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
        {status === 'connecting' && lines.length === 0 && (
          <p className="flex items-center gap-2 text-ink-faint">
            <Spinner className="size-3" /> Attaching to {pod}
          </p>
        )}
        {status !== 'connecting' && visible.length === 0 && (
          <p className="text-ink-faint">
            {filter ? 'No lines match the filter.' : 'No output yet.'}
          </p>
        )}
        {visible.map((line, i) => (
          <div
            key={i}
            className={clsx('text-ink-muted', wrap ? 'break-all whitespace-pre-wrap' : 'whitespace-pre')}
          >
            {line}
          </div>
        ))}
      </div>
    </div>
  )
}
