import clsx from 'clsx'
import {
  useEffect,
  useId,
  useRef,
  useState,
  useSyncExternalStore,
  type ComponentPropsWithRef,
  type ReactNode,
  type RefObject,
} from 'react'
import { age as formatAge, toneFor, type Tone } from '../lib/format'
import { CloseIcon, SearchIcon } from './icons'

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
  className,
}: {
  children: ReactNode
  tone?: Tone
  title?: string
  /** For a badge that has to line up with others — a fixed column width,
   *  typically. Layout only; the skin is not a caller's business. */
  className?: string
}) {
  return (
    <span
      title={title}
      className={clsx(
        'inline-flex items-center gap-1.5 px-2 py-0.5 text-[11px] ring-1 ring-inset whitespace-nowrap',
        TONE_CLASS[tone],
        className,
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

const LIVE_STATES = {
  connecting: { tone: 'idle', label: 'connecting' },
  live: { tone: 'ok', label: 'live' },
  polling: { tone: 'warn', label: 'polling' },
  paused: { tone: 'idle', label: 'paused' },
  off: { tone: 'idle', label: 'static' },
} as const

export type LiveState = keyof typeof LIVE_STATES

const LIVE_SPOKEN: Record<LiveState, string> = {
  connecting: 'Connecting to the live stream.',
  live: 'Live: updates are streaming from the cluster.',
  polling: 'Live stream unavailable. Refreshing every 15 seconds instead.',
  paused: 'Paused. The list is being held still and is not updating.',
  off: 'Static list. Not receiving live updates.',
}

/**
 * Explains the live-update state in a page header, honestly.
 *
 * Every list in this console updates itself by some means, and none of them
 * look any different while doing it. This badge is where a page says which it
 * is — and, when a page can hold itself still, it is also the control that
 * does so, because a reader who has just paused a feed looks for the state at
 * the place they changed it.
 *
 * The one thing it must never do is imply currency it does not have: a held
 * feed carries `detail` saying how old what you are reading is.
 */
export function LiveIndicator({
  state,
  detail,
  onToggle,
  title,
}: {
  state: LiveState
  /** Rendered after the label — the age of a held feed, typically. */
  detail?: ReactNode
  /** Supply one to make the indicator the control that changes the state. */
  onToggle?: () => void
  title?: string
}) {
  const { tone, label } = LIVE_STATES[state]
  const tip =
    title ??
    (state === 'live'
      ? 'Streaming changes from the cluster watch'
      : state === 'polling'
        ? 'The live stream is unavailable; refreshing every 15 seconds instead'
        : undefined)

  const badge = (
    <Badge tone={tone} title={onToggle ? undefined : tip}>
      {state === 'live' && <span className="size-1.5 animate-pulse rounded-full bg-ok" />}
      <span aria-hidden="true">{label}</span>
      {detail !== undefined && <span className="text-ink-faint tabular-nums">{detail}</span>}
    </Badge>
  )

  return (
    <>
      {onToggle ? (
        <button
          type="button"
          onClick={onToggle}
          title={tip}
          aria-pressed={state === 'live'}
          aria-label={state === 'live' ? 'Pause live updates' : 'Resume live updates'}
          className="inline-flex transition-opacity hover:opacity-80"
        >
          {badge}
        </button>
      ) : (
        badge
      )}
      {/* A reader who cannot see the badge change colour still needs to know
          the page stopped being live. Announced politely, and only when the
          state actually moves — never per row. */}
      <span role="status" aria-live="polite" className="sr-only">
        {LIVE_SPOKEN[state]}
      </span>
    </>
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
 * "/" focuses a filter box, the convention everywhere a list can be filtered.
 *
 * The listener is shared rather than per-instance: two filter boxes on one
 * page would otherwise both answer, and whichever mounted last would win by
 * accident. First in document order wins instead, which is the one the
 * reader is looking at.
 */
const filterFields = new Set<HTMLInputElement>()
let slashListener: ((e: KeyboardEvent) => void) | undefined

function focusFirstFilterField() {
  let first: HTMLInputElement | undefined
  for (const el of filterFields) {
    if (!el.isConnected) continue
    if (
      !first ||
      first.compareDocumentPosition(el) & Node.DOCUMENT_POSITION_PRECEDING
    ) {
      first = el
    }
  }
  first?.focus()
}

function registerFilterField(el: HTMLInputElement) {
  filterFields.add(el)
  if (!slashListener) {
    slashListener = (e: KeyboardEvent) => {
      if (e.key !== '/' || e.metaKey || e.ctrlKey || e.altKey) return
      const target = e.target as HTMLElement | null
      // "/" is a character long before it is a shortcut; never take it from
      // someone who is typing one.
      if (
        target &&
        (target.tagName === 'INPUT' ||
          target.tagName === 'TEXTAREA' ||
          target.isContentEditable)
      ) {
        return
      }
      e.preventDefault()
      focusFirstFilterField()
    }
    window.addEventListener('keydown', slashListener)
  }
  return () => {
    filterFields.delete(el)
    if (filterFields.size === 0 && slashListener) {
      window.removeEventListener('keydown', slashListener)
      slashListener = undefined
    }
  }
}

/**
 * A filter box: a field with a search icon and a button to empty it.
 *
 * There are three of these — the resource search, the events filter, the log
 * filter — doing the same job on three pages, and they had each grown their
 * own chrome. Only one could be cleared without selecting the text and
 * deleting it, and only one answered "/". Being the same control, they are
 * one component.
 *
 * `className` sits on the positioned wrapper, so callers pass the width
 * there; the field itself always fills it. `children` render inside that
 * wrapper, for a caller hanging a suggestion list or a validation panel
 * beneath the field.
 */
export function FilterInput({
  value,
  onValueChange,
  onClear,
  inputRef,
  invalid,
  size = 'sm',
  className,
  children,
  ...rest
}: {
  value: string
  onValueChange: (next: string) => void
  /** Runs after the text is emptied, for callers that must also commit it. */
  onClear?: () => void
  /** Supply one to drive focus from outside; otherwise an internal ref is used. */
  inputRef?: RefObject<HTMLInputElement | null>
  /** Draws the danger ring and sets aria-invalid. */
  invalid?: boolean
  size?: ControlSize
  className?: string
  children?: ReactNode
} & Omit<
  ComponentPropsWithRef<'input'>,
  'size' | 'value' | 'onChange' | 'className' | 'ref' | 'children'
>) {
  const localRef = useRef<HTMLInputElement>(null)
  const ref = inputRef ?? localRef

  useEffect(() => {
    const el = ref.current
    if (!el) return
    return registerFilterField(el)
  }, [ref])

  return (
    <div className={clsx('relative', className)}>
      <SearchIcon
        className={clsx(
          'pointer-events-none absolute left-2.5 size-3.5 text-ink-faint',
          size === 'sm' ? 'top-2' : 'top-2.5',
        )}
      />
      <input
        {...rest}
        ref={ref}
        value={value}
        onChange={(e) => onValueChange(e.target.value)}
        aria-invalid={invalid}
        className={clsx(
          CONTROL_BOX[size],
          FIELD_SKIN,
          'w-full pl-7',
          value === '' ? 'pr-2.5' : 'pr-7',
          invalid && 'ring-danger',
        )}
      />
      {value !== '' && (
        <button
          type="button"
          aria-label="Clear the filter"
          title="Clear the filter"
          // Mousedown would blur the field first, and the click would land on
          // whatever moved under the cursor.
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => {
            onValueChange('')
            onClear?.()
            ref.current?.focus()
          }}
          className={clsx(
            'absolute top-0 right-0 grid place-items-center text-ink-faint transition-colors hover:text-ink',
            size === 'sm' ? 'size-7' : 'size-8',
          )}
        >
          <CloseIcon className="size-3.5" />
        </button>
      )}
      {children}
    </div>
  )
}

/** One row of a Listbox. */
export interface ListboxItem {
  /** Reported on selection, and the row's key. */
  value: string
  /** What type-ahead matches and what a screen reader reads. */
  label: string
  /** A richer row than its label, for a list that shows more than a name. */
  content?: ReactNode
}

/** How long a type-ahead burst stays one word: long enough to spell a
 *  namespace, short enough that the next search starts fresh. */
const TYPE_AHEAD_MS = 700

/**
 * A dropdown that is drawn by this application rather than by the platform.
 *
 * The native `<select>` below is still the right control for a short list
 * inside a form. It is the wrong one for the console's own chrome: the browser
 * paints that menu in the operating system's colours and typeface, so the two
 * pickers in the sidebar looked like they came from different programs — one
 * of them from a different decade.
 *
 * What native gives away for free is the part that has to be re-earned here,
 * and is: focus moves to the listbox with the options pointed at by
 * aria-activedescendant, arrows and Home/End move, Enter and Space choose,
 * Escape closes and returns focus to the trigger, Tab leaves without a stale
 * popup behind it, and typing jumps — which is how anyone picks one namespace
 * out of two hundred without reaching for the mouse.
 */
export function Listbox({
  items,
  value,
  onSelect,
  ariaLabel,
  labelledBy,
  children,
}: {
  items: ListboxItem[]
  /** The selected value; '' when the list has a neutral first row. */
  value: string
  onSelect: (value: string) => void
  ariaLabel?: string
  /** Id of an existing label, for a control that already has one on screen. */
  labelledBy?: string
  /** The closed control's contents; the frame and the caret are drawn here. */
  children: ReactNode
}) {
  const [open, setOpen] = useState(false)
  const [activeIndex, setActiveIndex] = useState(0)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const listRef = useRef<HTMLUListElement>(null)
  const listboxId = useId()
  const typed = useRef({ text: '', at: 0 })

  // Focus lives on the listbox itself; options are pointed at with
  // aria-activedescendant so a screen reader tracks the arrow keys.
  useEffect(() => {
    if (open) listRef.current?.focus()
  }, [open])

  useEffect(() => {
    if (!open) return
    listRef.current?.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [open, activeIndex])

  const openList = () => {
    const at = items.findIndex((i) => i.value === value)
    setActiveIndex(at >= 0 ? at : 0)
    setOpen(true)
  }

  const close = () => {
    setOpen(false)
    triggerRef.current?.focus()
  }

  const choose = (item: ListboxItem) => {
    close()
    onSelect(item.value)
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        setActiveIndex((i) => Math.min(i + 1, items.length - 1))
        return
      case 'ArrowUp':
        e.preventDefault()
        setActiveIndex((i) => Math.max(i - 1, 0))
        return
      case 'Home':
        e.preventDefault()
        setActiveIndex(0)
        return
      case 'End':
        e.preventDefault()
        setActiveIndex(items.length - 1)
        return
      case 'Enter':
      case ' ':
        e.preventDefault()
        if (items[activeIndex]) choose(items[activeIndex])
        return
      case 'Escape':
        e.preventDefault()
        close()
        return
      case 'Tab':
        // Let focus move on naturally, but not with a stale popup behind it.
        setOpen(false)
        return
    }

    // Type-ahead. A burst of letters spells one prefix; a pause starts a new
    // one, except that repeating a single letter cycles the matches for it,
    // which is what every platform list does and what fingers expect.
    if (e.key.length !== 1 || e.metaKey || e.ctrlKey || e.altKey) return
    const now = Date.now()
    const fresh = now - typed.current.at > TYPE_AHEAD_MS
    const repeat = !fresh && typed.current.text === e.key
    const text = fresh || repeat ? e.key : typed.current.text + e.key
    typed.current = { text, at: now }

    const matches = (i: number) => items[i].label.toLowerCase().startsWith(text.toLowerCase())
    const from = repeat || text.length === 1 ? activeIndex + 1 : activeIndex
    for (let n = 0; n < items.length; n++) {
      const i = (from + n) % items.length
      if (matches(i)) {
        e.preventDefault()
        setActiveIndex(i)
        return
      }
    }
  }

  return (
    <div className="relative">
      <button
        ref={triggerRef}
        type="button"
        onClick={() => (open ? setOpen(false) : openList())}
        className="flex w-full items-center gap-2 bg-surface-2 px-2.5 py-2 text-left ring-1 ring-border transition-colors hover:ring-border-strong"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={ariaLabel}
        aria-labelledby={labelledBy}
        aria-controls={open ? listboxId : undefined}
      >
        <span className="min-w-0 flex-1">{children}</span>
        <span aria-hidden className="text-ink-faint">
          ▾
        </span>
      </button>

      {open && (
        <>
          {/* Catches the click that dismisses the popup, so choosing "somewhere
              else" does not also press whatever was underneath. */}
          <div className="fixed inset-0 z-30" onClick={() => setOpen(false)} />
          <ul
            ref={listRef}
            id={listboxId}
            role="listbox"
            aria-label={ariaLabel}
            aria-labelledby={labelledBy}
            tabIndex={-1}
            aria-activedescendant={items[activeIndex] ? `${listboxId}-${activeIndex}` : undefined}
            onKeyDown={onKeyDown}
            className="animate-in absolute z-40 mt-1 max-h-96 w-full overflow-auto bg-raised py-1 shadow-[0_16px_40px_rgba(0,0,0,.6)] ring-1 ring-border-strong outline-none"
          >
            {items.map((item, i) => (
              <li
                key={item.value}
                id={`${listboxId}-${i}`}
                role="option"
                aria-selected={item.value === value}
                data-active={i === activeIndex}
                onMouseMove={() => setActiveIndex(i)}
                onClick={() => choose(item)}
                className={clsx(
                  'flex w-full cursor-pointer items-start gap-2 px-3 py-2 text-left',
                  i === activeIndex && 'bg-surface-2',
                  item.value === value && 'bg-accent-soft',
                )}
              >
                {item.content ?? <span className="truncate text-[13px] text-ink">{item.label}</span>}
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  )
}

/**
 * A native select on the same scale, for the short lists inside forms and
 * dialogs. The console's own chrome uses Listbox above: a menu the platform
 * paints in its own colours reads as another application's, which is exactly
 * what the sidebar's two pickers looked like side by side.
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

/**
 * The small uppercase label that titles a panel or a tile.
 *
 * It had been written out by hand at ten call sites, and had drifted: three
 * letter-spacings (.06em, .08em, .1em), two weights, and one label at 10px
 * instead of 11px. Each difference is small on its own, but together they
 * read as ten slightly different kinds of heading rather than one heading
 * used ten times.
 *
 * Layout stays with the caller — the gap under a heading depends on what it
 * sits above — so this fixes the type and nothing else.
 */
export function Eyebrow({
  as: Tag = 'h2',
  className,
  children,
  id,
}: {
  as?: 'h2' | 'p' | 'span'
  className?: string
  children: ReactNode
  /** For an eyebrow that names a control beside it, via aria-labelledby. */
  id?: string
}) {
  return (
    <Tag
      id={id}
      className={clsx(
        'text-[11px] font-semibold tracking-[.1em] text-ink-faint uppercase',
        className,
      )}
    >
      {children}
    </Tag>
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
