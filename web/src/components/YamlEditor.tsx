import CodeMirror from '@uiw/react-codemirror'
import { yaml } from '@codemirror/lang-yaml'
import { unifiedMergeView } from '@codemirror/merge'
import { oneDark } from '@codemirror/theme-one-dark'
import { useEffect, useMemo, useRef, useState } from 'react'
import { Button, Spinner } from './primitives'

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

  // kubectl diff, inline: deleted server lines appear struck through above
  // the draft's replacements, so Apply is never a leap of faith.
  const diffExtensions = useMemo(
    () =>
      showDiff
        ? [yaml(), unifiedMergeView({ original: value, mergeControls: false })]
        : [yaml()],
    [showDiff, value],
  )

  return (
    <div className="flex h-full flex-col">
      {(notice || onSave) && (
        <div className="flex items-center gap-2 border-b border-border px-3 py-2">
          {notice && <span className="text-xs text-ink-faint">{notice}</span>}
          <div className="flex-1" />
          {dirty && <span className="text-xs text-warn">unsaved changes</span>}
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
                Revert
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
          theme={oneDark}
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
