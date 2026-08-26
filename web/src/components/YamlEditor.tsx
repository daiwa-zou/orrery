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
  /**
   * Sends the draft to the API server as a dry run and resolves with the YAML
   * the server would actually store. Rejecting means the edit is invalid, and
   * the reason is the server's own. When supplied, Apply runs this first.
   */
  onCheck?: (next: string) => Promise<string>
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
export function YamlEditor({
  value,
  readOnly,
  onSave,
  onCheck,
  notice,
  onDirtyChange,
}: YamlEditorProps) {
  const [draft, setDraft] = useState(value)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string>()
  const [dirty, setDirty] = useState(false)
  const [showDiff, setShowDiff] = useState(false)
  const [checking, setChecking] = useState(false)
  // What the last dry run reported: the server accepted it, and whether it
  // would change anything beyond what the author typed.
  const [checked, setChecked] = useState<{ serverEdits: number } | undefined>()
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

  // A verdict only describes the text it was run against.
  useEffect(() => {
    setChecked(undefined)
  }, [draft])

  // Losing a half-written manifest to a stray ⌘W is not acceptable.
  useEffect(() => {
    if (!dirty) return
    const warn = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty])

  /**
   * Ask the server what this edit would do. Returns the would-be YAML, or
   * undefined when the server rejected it (the reason is put on screen).
   */
  const check = async (): Promise<string | undefined> => {
    if (!onCheck) return undefined
    setChecking(true)
    setError(undefined)
    try {
      const serverYaml = await onCheck(draft)
      setChecked({ serverEdits: presentableDiff(draft, serverYaml).length })
      return serverYaml
    } catch (e) {
      setChecked(undefined)
      setError((e as Error).message)
      return undefined
    } finally {
      setChecking(false)
    }
  }

  const save = async () => {
    if (!onSave) return
    // Validate against the cluster before writing to it. A rejecting webhook
    // or an immutable field fails here, with the object untouched, instead of
    // half-applying and leaving the reader to work out what happened.
    if (onCheck && !checked) {
      setSaving(true)
      const ok = await check()
      setSaving(false)
      if (ok === undefined) return
      return
    }
    setSaving(true)
    setError(undefined)
    try {
      await onSave(draft)
      staleAfterSave.current = value
      setDirty(false)
      setChecked(undefined)
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
    setChecked(undefined)
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
          {checked && (
            <Badge tone={checked.serverEdits > 0 ? 'warn' : 'ok'}>
              {checked.serverEdits > 0
                ? `server accepts, and would set ${checked.serverEdits} more`
                : 'server accepts as written'}
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
              {onCheck && (
                <Button
                  size="sm"
                  onClick={check}
                  disabled={!dirty || saving || checking}
                  title="Send this to the API server as a dry run: admission, webhooks and defaulting run, nothing is written"
                >
                  {checking && <Spinner className="mr-1.5" />}
                  Check
                </Button>
              )}
              <Button size="sm" onClick={revert} disabled={!dirty || saving}>
                Discard
              </Button>
              <Button size="sm" variant="primary" onClick={save} disabled={!dirty || saving}>
                {saving ? <Spinner className="size-3" /> : null}
                {onCheck && !checked ? 'Check & apply' : 'Apply'}
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
