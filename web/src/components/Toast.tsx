import clsx from 'clsx'
import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import type { Tone } from '../lib/format'

interface Toast {
  id: number
  tone: Tone
  title: string
  description?: string
}

interface ToastApi {
  push: (toast: Omit<Toast, 'id'>) => void
}

const ToastContext = createContext<ToastApi>({ push: () => {} })

export function useToast(): ToastApi {
  return useContext(ToastContext)
}

const TONE_CLASS: Record<Tone, string> = {
  ok: 'ring-ok/30 bg-ok/10',
  warn: 'ring-warn/30 bg-warn/10',
  danger: 'ring-danger/30 bg-danger/10',
  info: 'ring-info/30 bg-info/10',
  idle: 'ring-border bg-surface-2',
}

const TITLE_CLASS: Record<Tone, string> = {
  ok: 'text-ok',
  warn: 'text-warn',
  danger: 'text-danger',
  info: 'text-info',
  idle: 'text-ink',
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])

  const push = useCallback((toast: Omit<Toast, 'id'>) => {
    const id = Date.now() + Math.random()
    setToasts((prev) => [...prev, { ...toast, id }])
    // Errors stay longer: they are usually something the reader must act on.
    const ttl = toast.tone === 'danger' ? 9000 : 5000
    window.setTimeout(() => {
      setToasts((prev) => prev.filter((t) => t.id !== id))
    }, ttl)
  }, [])

  const api = useMemo(() => ({ push }), [push])

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div
        className="pointer-events-none fixed right-4 bottom-4 z-[60] flex w-96 max-w-[calc(100vw-2rem)] flex-col gap-2"
        role="status"
        aria-live="polite"
      >
        {toasts.map((t) => (
          <div
            key={t.id}
            className={clsx(
              'animate-in pointer-events-auto rounded-lg px-3 py-2.5 shadow-xl ring-1 backdrop-blur',
              TONE_CLASS[t.tone],
            )}
          >
            <div className="flex items-start gap-2">
              <p className={clsx('flex-1 text-sm font-medium', TITLE_CLASS[t.tone])}>{t.title}</p>
              <button
                onClick={() => setToasts((prev) => prev.filter((x) => x.id !== t.id))}
                className="text-ink-faint hover:text-ink"
                aria-label="Dismiss"
              >
                ×
              </button>
            </div>
            {t.description && (
              <p className="mt-0.5 text-xs break-words text-ink-muted">{t.description}</p>
            )}
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  )
}
