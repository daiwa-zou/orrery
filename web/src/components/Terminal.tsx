import { useEffect, useRef, useState } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal as XTerm } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { wsURL } from '../api/client'
import { Badge } from './primitives'

interface TerminalProps {
  cluster: string
  namespace: string
  pod: string
  container: string
}

/**
 * An interactive shell in a container.
 *
 * The exec runs under the viewer's own identity server-side, so whatever they
 * type is attributable to them in the cluster's audit log rather than to the
 * dashboard's service account.
 */
export function Terminal({ cluster, namespace, pod, container }: TerminalProps) {
  const hostRef = useRef<HTMLDivElement>(null)
  const [status, setStatus] = useState<'connecting' | 'open' | 'closed' | 'error'>('connecting')
  const [message, setMessage] = useState<string>()

  useEffect(() => {
    const host = hostRef.current
    if (!host) return

    const term = new XTerm({
      fontFamily: 'ui-monospace, "SF Mono", Menlo, monospace',
      fontSize: 12,
      cursorBlink: true,
      convertEol: true,
      theme: {
        background: '#141821',
        foreground: '#e6e9ef',
        cursor: '#4f8ef7',
        selectionBackground: '#2b3550',
      },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(host)

    // The container needs the real geometry before the shell draws a prompt.
    const sendResize = (socket: WebSocket) => {
      try {
        fit.fit()
      } catch {
        // The host may not be laid out yet on the first tick.
      }
      if (socket.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
      }
    }

    const socket = new WebSocket(
      wsURL(`/clusters/${cluster}/ws/exec`, { namespace, pod, container }),
    )

    socket.onopen = () => {
      setStatus('open')
      sendResize(socket)
      term.focus()
    }

    socket.onmessage = (event) => {
      let msg: { type: string; data?: string; message?: string }
      try {
        msg = JSON.parse(event.data)
      } catch {
        return
      }
      switch (msg.type) {
        case 'stdout':
          term.write(msg.data ?? '')
          break
        case 'error':
          setStatus('error')
          setMessage(msg.message)
          term.write(`\r\n\x1b[31m${msg.message ?? 'stream error'}\x1b[0m\r\n`)
          break
        case 'exit':
          setStatus('closed')
          term.write('\r\n\x1b[90m— session ended —\x1b[0m\r\n')
          break
      }
    }

    socket.onerror = () => setStatus('error')
    socket.onclose = () => setStatus((s) => (s === 'error' ? s : 'closed'))

    const dataSub = term.onData((data) => {
      if (socket.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify({ type: 'stdin', data }))
      }
    })

    const observer = new ResizeObserver(() => sendResize(socket))
    observer.observe(host)

    return () => {
      observer.disconnect()
      dataSub.dispose()
      socket.close()
      term.dispose()
    }
  }, [cluster, namespace, pod, container])

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center gap-2 border-b border-border px-3 py-2">
        <Badge tone={status === 'open' ? 'ok' : status === 'error' ? 'danger' : 'idle'}>
          {status}
        </Badge>
        <span className="font-mono text-xs text-ink-faint">
          {namespace}/{pod}
          {container && ` · ${container}`}
        </span>
        {message && <span className="text-xs text-danger">{message}</span>}
      </div>
      <div ref={hostRef} className="min-h-0 flex-1 bg-[#141821] p-2" />
    </div>
  )
}
