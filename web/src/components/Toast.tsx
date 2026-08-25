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

/** A 2px rule in the tone colour along the left edge carries the severity;
 *  the panel itself stays a quiet raised surface. */
const TONE_CLASS: Record<Tone, string> = {
  ok: 'border-l-ok',
  warn: 'border-l-warn',
  danger: 'border-l-danger',
  info: 'border-l-info',
  idle: 'border-l-idle',
}

/** Stacked toasts beyond this collapse from the oldest: a bulk operation that
 *  fails N times must not wallpaper the screen. */
const MAX_TOASTS = 4

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])

  const push = useCallback((toast: Omit<Toast, 'id'>) => {
    const id = Date.now() + Math.random()
    setToasts((prev) => [...prev, { ...toast, id }].slice(-MAX_TOASTS))
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
        className="pointer-events-none fixed right-[18px] bottom-[18px] z-[80] flex w-[300px] max-w-[calc(100vw-2rem)] flex-col gap-2"
        role="status"
        aria-live="polite"
      >
        {toasts.map((t) => (
          <div
            key={t.id}
            // A failed delete must interrupt a screen reader; a success no.
            role={t.tone === 'danger' ? 'alert' : undefined}
            className={clsx(
              'animate-in pointer-events-auto border border-border-strong border-l-2 bg-raised px-3 py-2.5 shadow-[0_16px_40px_rgba(0,0,0,.6)]',
              TONE_CLASS[t.tone],
            )}
          >
            <div className="flex items-start gap-2">
              <p className="flex-1 text-[13px] font-medium text-ink">{t.title}</p>
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
