import clsx from 'clsx'
import { useEffect, useId, useRef, useSyncExternalStore, type ReactNode } from 'react'
import { age as formatAge, toneFor, type Tone } from '../lib/format'

/* Small, shared building blocks. Keeping them in one file makes the visual
   language easy to keep consistent — every badge, button and empty state in
   the app is defined here. */

const TONE_CLASS: Record<Tone, string> = {
  ok: 'bg-ok/12 text-ok ring-ok/25',
  warn: 'bg-warn/12 text-warn ring-warn/25',
  danger: 'bg-danger/12 text-danger ring-danger/25',
  info: 'bg-info/12 text-info ring-info/25',
  idle: 'bg-idle/12 text-ink-faint ring-border',
}

export function Badge({
  children,
  tone = 'idle',
  title,
}: {
  children: ReactNode
  tone?: Tone
  title?: string
}) {
  return (
    <span
      title={title}
      className={clsx(
        'inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset whitespace-nowrap',
        TONE_CLASS[tone],
      )}
    >
      {children}
    </span>
  )
}

export function StatusBadge({ value, title }: { value: string; title?: string }) {
  if (!value) return <span className="text-ink-faint">—</span>
  const tone = toneFor(value)
  return (
    <Badge tone={tone} title={title ?? value}>
      <span
        aria-hidden
        className={clsx('size-1.5 rounded-full', {
          'bg-ok': tone === 'ok',
          'bg-warn': tone === 'warn',
          'bg-danger': tone === 'danger',
          'bg-info': tone === 'info',
          'bg-idle': tone === 'idle',
        })}
      />
      {value}
    </Badge>
  )
}

// One shared 10s ticker for every Age cell. A 250-row table with two time
// columns would otherwise run 500 unsynchronised intervals, each triggering
// its own isolated re-render.
const tickListeners = new Set<() => void>()
let tickTimer: number | undefined
let tickValue = 0

function subscribeTick(cb: () => void) {
  tickListeners.add(cb)
  if (tickListeners.size === 1) {
    tickTimer = window.setInterval(() => {
      tickValue += 1
      tickListeners.forEach((l) => l())
    }, 10_000)
  }
  return () => {
    tickListeners.delete(cb)
    if (tickListeners.size === 0) window.clearInterval(tickTimer)
  }
}

/**
 * Age re-renders on a timer rather than being formatted server-side, so the
 * value stays correct without refetching and without trusting clock skew
 * between the browser and the cluster.
 */
export function Age({ timestamp }: { timestamp?: string | null }) {
  useSyncExternalStore(subscribeTick, () => tickValue)
  return (
    <span title={timestamp ?? undefined} className="tabular-nums">
      {formatAge(timestamp)}
    </span>
  )
}

export function Button({
  children,
  onClick,
  variant = 'default',
  size = 'md',
  disabled,
  title,
  type = 'button',
  icon,
  'aria-label': ariaLabel,
  'aria-pressed': ariaPressed,
}: {
  children: ReactNode
  onClick?: () => void
  variant?: 'default' | 'primary' | 'danger' | 'ghost'
  size?: 'sm' | 'md'
  disabled?: boolean
  title?: string
  type?: 'button' | 'submit'
  /** Icon-only: square padding; pass aria-label and title, the icon is mute. */
  icon?: boolean
  'aria-label'?: string
  'aria-pressed'?: boolean
}) {
  return (
    <button
      type={type}
      title={title}
      aria-label={ariaLabel}
      aria-pressed={ariaPressed}
      onClick={onClick}
      disabled={disabled}
      className={clsx(
        'inline-flex items-center gap-1.5 rounded-md font-medium transition-colors',
        'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent',
        'disabled:cursor-not-allowed disabled:opacity-45',
        icon
          ? size === 'sm'
            ? 'p-1.5'
            : 'p-2'
          : size === 'sm'
            ? 'px-2 py-1 text-xs'
            : 'px-3 py-1.5 text-sm',
        {
          'bg-surface-2 text-ink ring-1 ring-border hover:bg-border/60': variant === 'default',
          'bg-accent text-canvas hover:brightness-110': variant === 'primary',
          'bg-danger/12 text-danger ring-1 ring-danger/30 hover:bg-danger/20': variant === 'danger',
          'text-ink-muted hover:bg-surface-2 hover:text-ink': variant === 'ghost',
        },
      )}
    >
      {children}
    </button>
  )
}

/**
 * A Button gated by a permission check. Denied renders dimmed and inert with
 * a tooltip naming the missing permission — visible but honest, so the UI
 * teaches RBAC instead of silently rearranging itself per viewer.
 *
 * The tooltip lives on a wrapper span because browsers are inconsistent about
 * showing title on a disabled control.
 */
export function GatedButton({
  allowed,
  deniedTitle,
  title,
  disabled,
  ...rest
}: {
  allowed: boolean
  /** Shown while hovering a denied button, e.g. "Requires patch on deployments". */
  deniedTitle: string
} & Parameters<typeof Button>[0]) {
  return (
    <span className="inline-flex" title={allowed ? title : deniedTitle}>
      <Button {...rest} disabled={disabled || !allowed} />
    </span>
  )
}

export function Spinner({ className }: { className?: string }) {
  return (
    <svg
      className={clsx('animate-spin', className ?? 'size-4')}
      viewBox="0 0 24 24"
      fill="none"
      aria-hidden
    >
      <circle cx="12" cy="12" r="9" stroke="currentColor" strokeWidth="2.5" opacity="0.2" />
      <path
        d="M21 12a9 9 0 0 0-9-9"
        stroke="currentColor"
        strokeWidth="2.5"
        strokeLinecap="round"
      />
    </svg>
  )
}

export function EmptyState({
  title,
  description,
  action,
}: {
  title: string
  description?: ReactNode
  action?: ReactNode
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 px-6 py-16 text-center">
      <p className="text-sm font-medium text-ink">{title}</p>
      {description && <p className="max-w-md text-sm text-ink-faint">{description}</p>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  )
}

/**
 * ErrorState distinguishes "you may not see this" from "something broke",
 * because the two need very different responses from the reader.
 */
export function ErrorState({ error, retry }: { error: unknown; retry?: () => void }) {
  const err = error as { status?: number; kind?: string; message?: string }
  const forbidden = err?.status === 403

  return (
    <div className="flex flex-col items-center justify-center gap-3 px-6 py-16 text-center">
      <Badge tone={forbidden ? 'warn' : 'danger'}>
        {forbidden ? 'Not permitted' : (err?.kind ?? 'Error')}
      </Badge>
      <p className="max-w-xl text-sm text-ink-muted">
        {err?.message ?? 'Something went wrong.'}
      </p>
      {retry && !forbidden && (
        <Button size="sm" onClick={retry}>
          Try again
        </Button>
      )}
    </div>
  )
}

/** A labelled key/value row, used throughout the detail pane. */
export function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[10rem_1fr] gap-3 py-1.5 text-sm">
      <dt className="text-ink-faint">{label}</dt>
      <dd className="min-w-0 break-words text-ink">{children}</dd>
    </div>
  )
}

export function LabelChips({ labels }: { labels?: Record<string, string> }) {
  const entries = Object.entries(labels ?? {})
  if (entries.length === 0) return <span className="text-ink-faint">—</span>
  return (
    <div className="flex flex-wrap gap-1">
      {entries.map(([k, v]) => (
        <span
          key={k}
          className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] text-ink-muted ring-1 ring-border"
          title={`${k}=${v}`}
        >
          {k}
          <span className="text-ink-faint">=</span>
          {v}
        </span>
      ))}
    </div>
  )
}

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

/** A modal used for destructive confirmations. */
export function Modal({
  open,
  title,
  onClose,
  children,
  footer,
  wide,
}: {
  open: boolean
  title: string
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
  wide?: boolean
}) {
  const panelRef = useRef<HTMLDivElement>(null)
  const titleId = useId()

  // Focus lands on the panel itself rather than its first focusable control:
  // these are destructive confirmations, and initial focus on the confirm
  // button turns a stray Enter into a delete.
  useEffect(() => {
    if (!open) return
    const opener = document.activeElement as HTMLElement | null
    panelRef.current?.focus()
    return () => opener?.focus()
  }, [open])

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        onClose()
        return
      }
      if (e.key !== 'Tab') return
      // The backdrop makes the page behind inert to the pointer; Tab has to be
      // trapped so it is inert to the keyboard too.
      const panel = panelRef.current
      if (!panel) return
      const focusables = Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE))
      if (focusables.length === 0) {
        e.preventDefault()
        panel.focus()
        return
      }
      const first = focusables[0]
      const last = focusables[focusables.length - 1]
      const current = document.activeElement
      if (e.shiftKey) {
        if (current === first || !panel.contains(current)) {
          e.preventDefault()
          last.focus()
        }
      } else if (current === last || !panel.contains(current)) {
        e.preventDefault()
        first.focus()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  if (!open) return null

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
      onClick={onClose}
    >
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        className={clsx(
          'animate-in w-full rounded-lg bg-surface ring-1 ring-border shadow-2xl outline-none',
          wide ? 'max-w-4xl' : 'max-w-lg',
        )}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="border-b border-border px-4 py-3">
          <h2 id={titleId} className="text-sm font-semibold text-ink">
            {title}
          </h2>
        </header>
        <div className="max-h-[70vh] overflow-auto px-4 py-4">{children}</div>
        {footer && (
          <footer className="flex justify-end gap-2 border-t border-border px-4 py-3">
            {footer}
          </footer>
        )}
      </div>
    </div>
  )
}
