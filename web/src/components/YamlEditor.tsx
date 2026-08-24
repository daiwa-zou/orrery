import CodeMirror from '@uiw/react-codemirror'
import { yaml } from '@codemirror/lang-yaml'
import { oneDark } from '@codemirror/theme-one-dark'
import { useEffect, useState } from 'react'
import { Button, Spinner } from './primitives'

interface YamlEditorProps {
  value: string
  readOnly?: boolean
  onSave?: (next: string) => Promise<void>
  /** Shown above the editor when the object cannot be edited. */
  notice?: string
}

/**
 * A YAML view that doubles as an editor.
 *
 * Save is only offered once the text actually differs, and the buffer resets
 * whenever the underlying object is refetched — so a background refresh can
 * never quietly discard something the reader typed, and an unedited pane
 * always shows the truth.
 */
export function YamlEditor({ value, readOnly, onSave, notice }: YamlEditorProps) {
  const [draft, setDraft] = useState(value)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string>()
  const [dirty, setDirty] = useState(false)

  useEffect(() => {
    // Only adopt a new server value when the reader has not started editing.
    if (!dirty) setDraft(value)
  }, [value, dirty])

  const save = async () => {
    if (!onSave) return
    setSaving(true)
    setError(undefined)
    try {
      await onSave(draft)
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

  return (
    <div className="flex h-full flex-col">
      {(notice || onSave) && (
        <div className="flex items-center gap-2 border-b border-border px-3 py-2">
          {notice && <span className="text-xs text-ink-faint">{notice}</span>}
          <div className="flex-1" />
          {dirty && <span className="text-xs text-warn">unsaved changes</span>}
          {onSave && !readOnly && (
            <>
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
          extensions={[yaml()]}
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
