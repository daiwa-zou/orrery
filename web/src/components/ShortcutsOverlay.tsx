import { useEffect } from 'react'
import { Corners } from './primitives'

const SHORTCUTS: { what: string; key: string }[] = [
  { what: 'Open the command palette', key: '⌘K' },
  { what: 'Focus the search bar', key: '/' },
  { what: 'Keyboard shortcuts', key: '?' },
  { what: 'Close dialogs and menus', key: 'esc' },
  { what: 'Navigate lists and results', key: '↑↓' },
  { what: 'Open the selection', key: '⏎' },
]

/** The small "?" overlay listing global shortcuts. */
export function ShortcutsOverlay({ open, onClose }: { open: boolean; onClose: () => void }) {
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  if (!open) return null

  return (
    <div
      className="fixed inset-0 z-[70] grid place-items-center bg-black/60 p-4"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Keyboard shortcuts"
        className="blueprint animate-in w-full max-w-[420px] bg-raised p-[18px] shadow-[0_16px_40px_rgba(0,0,0,.6)]"
        onClick={(e) => e.stopPropagation()}
      >
        <Corners />
        <h2 className="mb-3 font-condensed text-lg font-semibold text-ink">Keyboard shortcuts</h2>
        {SHORTCUTS.map((s) => (
          <div
            key={s.key}
            className="flex items-center justify-between border-b border-ink/7 py-1.5 text-[13px]"
          >
            <span className="text-ink-muted">{s.what}</span>
            <kbd className="border border-ink/20 bg-canvas px-1.5 py-px font-mono text-[11px] text-ink">
              {s.key}
            </kbd>
          </div>
        ))}
        <p className="mt-3 text-[11.5px] text-ink-faint">
          Inside a terminal session keystrokes belong to the shell — Ctrl+K is readline&apos;s
          kill-to-end-of-line, not the palette.
        </p>
      </div>
    </div>
  )
}
