import CodeMirror from '@uiw/react-codemirror'
import { yaml } from '@codemirror/lang-yaml'
import { syntaxHighlighting } from '@codemirror/language'
import { presentableDiff, unifiedMergeView } from '@codemirror/merge'
import { oneDarkHighlightStyle } from '@codemirror/theme-one-dark'
import { EditorView, gutter, GutterMarker } from '@codemirror/view'
import { useEffect, useMemo, useRef, useState } from 'react'
import { Badge, Button, Spinner } from './primitives'

/** oneDark's syntax colours on the blueprint code-pane ground. Passed as an
 *  extension with theme="none", so no stock background competes with it. */
export const codeTheme = [
  EditorView.theme(
    {
      '&': { backgroundColor: '#10141a', color: '#c8d0da', fontSize: '12.5px' },
      '.cm-content': { caretColor: '#94bce3' },
      '.cm-cursor, .cm-dropCursor': { borderLeftColor: '#94bce3' },
      '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, ::selection': {
        backgroundColor: 'rgba(89,128,166,.35)',
      },
      '.cm-gutters': {
        backgroundColor: '#10141a',
        color: '#4a5361',
        borderRight: '1px solid rgba(231,234,238,.08)',
      },
      '.cm-activeLine': { backgroundColor: 'rgba(231,234,238,.04)' },
      '.cm-activeLineGutter': { backgroundColor: 'rgba(231,234,238,.04)' },
      '.cm-staged-gutter': { width: '10px' },
      '.cm-staged-gutter .cm-staged-dot': {
        color: '#94bce3',
        fontSize: '8px',
        lineHeight: '1.6',
      },
    },
    { dark: true },
  ),
  syntaxHighlighting(oneDarkHighlightStyle),
]

class StagedDot extends GutterMarker {
  toDOM() {
    const el = document.createElement('span')
    el.className = 'cm-staged-dot'
    el.textContent = '●'
    el.title = 'Staged change'
    return el
  }
}
const stagedDot = new StagedDot()

/** A gutter dot on every line the draft changes relative to the server. */
function stagedGutter(changedLines: ReadonlySet<number>) {
  return gutter({
    class: 'cm-staged-gutter',
    lineMarker(view, line) {
      return changedLines.has(view.state.doc.lineAt(line.from).number) ? stagedDot : null
    },
    lineMarkerChange: () => true,
  })
}

interface YamlEditorProps {
  value: string
  readOnly?: boolean
  onSave?: (next: string) => Promise<void>
  /** Shown above the editor when the object cannot be edited. */
  notice?: string
  /** Lets the parent guard against discarding unsaved edits. */
  onDirtyChange?: (dirty: boolean) => void
}

/**
 * A YAML view that doubles as an editor.
 *
 * Save is only offered once the text actually differs, and the buffer resets
 * whenever the underlying object is refetched — so a background refresh can
 * never quietly discard something the reader typed, and an unedited pane
 * always shows the truth.
 */
export function YamlEditor({ value, readOnly, onSave, notice, onDirtyChange }: YamlEditorProps) {
  const [draft, setDraft] = useState(value)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string>()
  const [dirty, setDirty] = useState(false)
  const [showDiff, setShowDiff] = useState(false)
  // The value that was on screen when a save succeeded. Until the refetch
  // lands, the prop still holds this pre-save text; adopting it would make the
  // editor visibly revert the change that was just applied.
  const staleAfterSave = useRef<string>(undefined)

  useEffect(() => {
    // Only adopt a new server value when the reader has not started editing.
    if (!dirty && value !== staleAfterSave.current) {
      staleAfterSave.current = undefined
      setDraft(value)
    }
  }, [value, dirty])

  useEffect(() => {
    onDirtyChange?.(dirty)
  }, [dirty, onDirtyChange])

  // Losing a half-written manifest to a stray ⌘W is not acceptable.
  useEffect(() => {
    if (!dirty) return
    const warn = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty])

  const save = async () => {
    if (!onSave) return
    setSaving(true)
    setError(undefined)
    try {
      await onSave(draft)
      staleAfterSave.current = value
      setDirty(false)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  const revert = () => {
    setDraft(value)
    setDirty(false)
    setError(undefined)
  }

  // One line diff feeds both the "n staged changes" chip and the gutter dots.
  const { hunks, changedLines } = useMemo(() => {
    if (!dirty) return { hunks: 0, changedLines: new Set<number>() }
    const changes = presentableDiff(value, draft)
    const lines = new Set<number>()
    for (const c of changes) {
      const start = draft.slice(0, c.fromB).split('\n').length
      const end = draft.slice(0, c.toB).split('\n').length
      for (let l = start; l <= end; l++) lines.add(l)
    }
    return { hunks: changes.length, changedLines: lines }
  }, [value, draft, dirty])

  // kubectl diff, inline: deleted server lines appear struck through above
  // the draft's replacements, so Apply is never a leap of faith.
  const diffExtensions = useMemo(
    () =>
      showDiff
        ? [yaml(), codeTheme, unifiedMergeView({ original: value, mergeControls: false })]
        : [yaml(), codeTheme, stagedGutter(changedLines)],
    [showDiff, value, changedLines],
  )

  return (
    <div className="flex h-full flex-col">
      {(notice || onSave) && (
        <div className="flex items-center gap-2.5 border-b border-border bg-surface px-3 py-[7px]">
          {dirty && (
            <Badge tone="info">
              {hunks} staged change{hunks === 1 ? '' : 's'}
            </Badge>
          )}
          {notice && <span className="text-xs text-ink-faint">{notice}</span>}
          <div className="flex-1" />
          {onSave && !readOnly && (
            <>
              <Button
                size="sm"
                onClick={() => setShowDiff((v) => !v)}
                disabled={!dirty && !showDiff}
                title="Show what Apply would change, kubectl diff style"
              >
                {showDiff ? 'Hide diff' : 'Diff'}
              </Button>
              <Button size="sm" onClick={revert} disabled={!dirty || saving}>
                Discard
              </Button>
              <Button size="sm" variant="primary" onClick={save} disabled={!dirty || saving}>
                {saving ? <Spinner className="size-3" /> : null}
                Apply
              </Button>
            </>
          )}
        </div>
      )}

      {error && (
        <p className="border-b border-border bg-danger/10 px-3 py-2 text-xs break-words text-danger">
          {error}
        </p>
      )}

      <div className="min-h-0 flex-1 overflow-auto">
        <CodeMirror
          value={draft}
          height="100%"
          theme="none"
          extensions={diffExtensions}
          editable={!readOnly}
          onChange={(next) => {
            setDraft(next)
            setDirty(next !== value)
          }}
          basicSetup={{
            lineNumbers: true,
            foldGutter: true,
            highlightActiveLine: !readOnly,
            autocompletion: false,
          }}
        />
      </div>
    </div>
  )
}
