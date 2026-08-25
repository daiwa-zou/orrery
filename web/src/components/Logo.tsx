import clsx from 'clsx'

/**
 * The Orrery mark: concentric orbits with a planet on the outer ring. The
 * login page uses the large variant, which adds a dashed middle orbit.
 */
export function LogoMark({ className, large }: { className?: string; large?: boolean }) {
  return (
    <svg viewBox="0 0 36 36" className={className} aria-hidden>
      <circle
        cx="18"
        cy="18"
        r="15"
        fill="none"
        stroke="var(--color-accent)"
        strokeWidth={large ? 1.4 : 2}
      />
      {large && (
        <circle
          cx="18"
          cy="18"
          r="9.5"
          fill="none"
          stroke="rgba(148,188,227,.5)"
          strokeWidth="1"
          strokeDasharray="3 3"
        />
      )}
      <circle cx="18" cy="18" r={large ? 2.6 : 2.8} fill="var(--color-accent-text)" />
      {large ? (
        <circle cx="27.5" cy="18" r="1.8" fill="var(--color-accent)" />
      ) : (
        <circle cx="30" cy="18" r="2" fill="var(--color-accent)" />
      )}
    </svg>
  )
}

export function Wordmark({ className }: { className?: string }) {
  return (
    <span className={clsx('font-condensed font-semibold text-ink', className ?? 'text-[15px] tracking-[.12em]')}>
      ORRERY
    </span>
  )
}
