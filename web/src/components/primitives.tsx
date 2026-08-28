import clsx from 'clsx'
import {
  useEffect,
  useId,
  useRef,
  useSyncExternalStore,
  type ComponentPropsWithRef,
  type ReactNode,
} from 'react'
import { age as formatAge, toneFor, type Tone } from '../lib/format'

/* Small, shared building blocks. Keeping them in one file makes the visual
   language easy to keep consistent — every badge, button and empty state in
   the app is defined here. */

/**
 * One control-size scale for the whole app.
 *
 * The height is stated outright rather than left to fall out of padding,
 * because a padded control's height also depends on the line-height it
 * inherits — and an input inherits a different one from the buttons beside
 * it. That is how the resource toolbar came to render a 24px button next to a
 * 28px icon button next to a 30.75px search field, all nominally "small".
 * With a fixed height the three agree by construction, and a change to the
 * scale moves every control at once.
 *
 * `sm` (28px) is the working size — toolbars, dense forms, anything inline.
 * `md` (32px) is for controls that stand alone and want the extra weight.
 */
export type ControlSize = 'sm' | 'md'

const CONTROL_BOX: Record<ControlSize, string> = {
  sm: 'h-7 text-xs',
  md: 'h-8 text-sm',
}

/** Horizontal padding for controls carrying text. */
const CONTROL_PAD: Record<ControlSize, string> = {
  sm: 'px-2.5',
  md: 'px-3',
}

/** Icon-only controls are square, so they keep the same footprint. */
const CONTROL_SQUARE: Record<ControlSize, string> = {
  sm: 'w-7',
  md: 'w-8',
}

/**
 * The shared field skin. Inputs and selects are outlined with a ring rather
 * than a border so the drawn edge costs no layout: a ringed field and a plain
 * one of the same size line up exactly, which a 1px border would spoil.
 */
const FIELD_SKIN =
  'min-w-0 bg-surface-2 text-ink ring-1 ring-border placeholder:text-ink-faint disabled:cursor-not-allowed disabled:opacity-45'

/** The four "+" registration marks of a `.blueprint` frame. */
export function Corners() {
  return (
    <>
      <i aria-hidden className="corner tl" />
      <i aria-hidden className="corner tr" />
      <i aria-hidden className="corner bl" />
      <i aria-hidden className="corner br" />
    </>
  )
}

const TONE_CLASS: Record<Tone, string> = {
  ok: 'bg-ok/12 text-ok ring-ok/30',
  warn: 'bg-warn/12 text-warn ring-warn/30',
  danger: 'bg-danger/13 text-danger ring-danger/32',
  info: 'bg-info/12 text-info ring-info/28',
  idle: 'bg-idle/12 text-idle ring-idle/28',
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
        'inline-flex items-center gap-1.5 px-2 py-0.5 text-[11px] ring-1 ring-inset whitespace-nowrap',
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
  className,
  'aria-label': ariaLabel,
  'aria-pressed': ariaPressed,
}: {
  children: ReactNode
  onClick?: () => void
  variant?: 'default' | 'primary' | 'danger' | 'ghost'
  size?: ControlSize
  disabled?: boolean
  title?: string
  type?: 'button' | 'submit'
  /** Icon-only: square padding; pass aria-label and title, the icon is mute. */
  icon?: boolean
  className?: string
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
        'inline-flex items-center justify-center gap-1.5 font-condensed font-semibold whitespace-nowrap transition-colors',
        'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent',
        'disabled:cursor-not-allowed disabled:opacity-45',
        CONTROL_BOX[size],
        icon ? CONTROL_SQUARE[size] : CONTROL_PAD[size],
        {
          'bg-transparent text-ink ring-1 ring-border hover:bg-ink/7': variant === 'default',
          'bg-accent text-canvas ring-1 ring-accent hover:brightness-110': variant === 'primary',
          'bg-danger/12 text-danger ring-1 ring-danger/32 hover:bg-danger/20': variant === 'danger',
          'text-accent-text hover:bg-accent/10 hover:text-accent-text-hover': variant === 'ghost',
        },
        className,
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

/**
 * A text field on the shared control scale. Width is the caller's business —
 * a filter box and a namespace picker want different ones — but height, type
 * size and skin are not, so a field always matches the buttons beside it.
 */
export function TextInput({
  size = 'sm',
  className,
  ...rest
}: { size?: ControlSize } & Omit<ComponentPropsWithRef<'input'>, 'size'>) {
  return (
    <input
      {...rest}
      className={clsx(CONTROL_BOX[size], CONTROL_PAD[size], FIELD_SKIN, className)}
    />
  )
}

/**
 * A native select on the same scale. Native is deliberate: it keeps the
 * platform's own keyboard handling and its long-list behaviour, which a
 * hand-rolled listbox would have to reimplement badly.
 */
export function Select({
  size = 'sm',
  className,
  children,
  ...rest
}: { size?: ControlSize } & Omit<ComponentPropsWithRef<'select'>, 'size'>) {
  return (
    <select
      {...rest}
      className={clsx(CONTROL_BOX[size], CONTROL_PAD[size], FIELD_SKIN, className)}
    >
      {children}
    </select>
  )
}

/**
 * A checkbox at one fixed size. The unstyled default is drawn by the platform
 * and comes out a different size on each, which is enough to knock a row of
 * toolbar labels out of alignment.
 */
export function Checkbox({
  tone = 'accent',
  className,
  ...rest
}: { tone?: 'accent' | 'warn' } & ComponentPropsWithRef<'input'>) {
  return (
    <input
      type="checkbox"
      {...rest}
      className={clsx(
        'block size-3.5 shrink-0',
        tone === 'warn' ? 'accent-warn' : 'accent-accent',
        className,
      )}
    />
  )
}

/**
 * The centered spinner-with-label block every loading state renders. The
 * className replaces the default vertical padding for hosts that need to fill
 * their box instead (e.g. "h-full").
 */
export function Loading({ label, className }: { label: ReactNode; className?: string }) {
  return (
    <div
      className={clsx(
        'flex items-center justify-center gap-2 text-ink-faint',
        className ?? 'py-24',
      )}
    >
      <Spinner /> {label}
    </div>
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
}: {
  title: string
  description?: ReactNode
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 px-6 py-16 text-center">
      <svg viewBox="0 0 48 48" width="44" height="44" aria-hidden>
        <circle
          cx="24"
          cy="24"
          r="19"
          fill="none"
          stroke="rgba(231,234,238,.18)"
          strokeWidth="1"
          strokeDasharray="3 4"
        />
        <circle cx="24" cy="24" r="2.5" fill="rgba(148,188,227,.5)" />
        <circle cx="41" cy="24" r="1.6" fill="rgba(231,234,238,.3)" />
      </svg>
      <p className="text-[13.5px] font-medium text-ink">{title}</p>
      {description && <p className="max-w-md text-[13px] text-ink-faint">{description}</p>}
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
    <div className="grid grid-cols-[150px_1fr] gap-3 border-b border-ink/6 py-1.5 text-[13px] last:border-b-0">
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
          className="bg-canvas px-1.5 py-0.5 font-mono text-[11px] text-ink-muted ring-1 ring-border"
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
          'animate-in w-full bg-raised ring-1 ring-border-strong shadow-[0_16px_40px_rgba(0,0,0,.6)] outline-none',
          wide ? 'max-w-4xl' : 'max-w-lg',
        )}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="border-b border-border px-4 py-3">
          <h2 id={titleId} className="font-condensed text-base font-semibold text-ink">
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
