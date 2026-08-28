import clsx from 'clsx'

/**
 * The app's icon set: inline stroke SVGs (heroicons outlines), sized for
 * icon-only buttons. Icons are decorative — the button carries the
 * aria-label and title — so every svg is aria-hidden.
 */
function Icon({ d, className, extra }: { d: string; className?: string; extra?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className={clsx('shrink-0', className ?? 'size-4')}
    >
      <path d={d} />
      {extra && <path d={extra} />}
    </svg>
  )
}

export function RefreshIcon({ className }: { className?: string }) {
  return (
    <Icon
      className={className}
      d="M16.023 9.348h4.992v-.001M2.985 19.644v-4.992m0 0h4.992m-4.993 0 3.181 3.183a8.25 8.25 0 0 0 13.803-3.7M4.031 9.865a8.25 8.25 0 0 1 13.803-3.7l3.181 3.182m0-4.991v4.99"
    />
  )
}

export function ColumnsIcon({ className }: { className?: string }) {
  return (
    <Icon
      className={className}
      d="M3.75 4.5h16.5v15H3.75v-15Z"
      extra="M9.25 4.5v15M14.75 4.5v15"
    />
  )
}

export function TagIcon({ className }: { className?: string }) {
  return (
    <Icon
      className={className}
      d="M9.568 3H5.25A2.25 2.25 0 0 0 3 5.25v4.318c0 .597.237 1.17.659 1.591l9.581 9.581c.699.699 1.78.872 2.607.33a18.095 18.095 0 0 0 5.223-5.223c.542-.827.369-1.908-.33-2.607L11.16 3.66A2.25 2.25 0 0 0 9.568 3Z"
      extra="M6 6h.008v.008H6V6Z"
    />
  )
}

export function TrashIcon({ className }: { className?: string }) {
  return (
    <Icon
      className={className}
      d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0"
    />
  )
}

export function ChevronLeftIcon({ className }: { className?: string }) {
  return <Icon className={className} d="M15.75 19.5 8.25 12l7.5-7.5" />
}

export function ChevronRightIcon({ className }: { className?: string }) {
  return <Icon className={className} d="m8.25 4.5 7.5 7.5-7.5 7.5" />
}

export function DownloadIcon({ className }: { className?: string }) {
  return (
    <Icon
      className={className}
      d="M3 16.5v2.25A2.25 2.25 0 0 0 5.25 21h13.5A2.25 2.25 0 0 0 21 18.75V16.5M16.5 12 12 16.5m0 0L7.5 12m4.5 4.5V3"
    />
  )
}

/** "Jump to the tail and keep following" — an arrow onto a baseline. */
export function FollowIcon({ className }: { className?: string }) {
  return (
    <Icon className={className} d="M12 3v12m0 0 5.25-5.25M12 15l-5.25-5.25" extra="M4.5 20.25h15" />
  )
}

export function SearchIcon({ className }: { className?: string }) {
  return (
    <Icon
      className={className}
      d="m21 21-5.197-5.197m0 0A7.5 7.5 0 1 0 5.196 5.196a7.5 7.5 0 0 0 10.607 10.607Z"
    />
  )
}

export function CloseIcon({ className }: { className?: string }) {
  return <Icon className={className} d="M6 18 18 6M6 6l12 12" />
}
