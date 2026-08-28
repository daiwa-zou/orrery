import { useState } from 'react'
import { metaChanges, validateLabelValue, validateMetaKey } from '../lib/labels'
import { Button, GatedButton, LabelChips, TextInput } from './primitives'

/**
 * Inline editing of metadata.labels / metadata.annotations — the UI half of
 * kubectl label/annotate. Read mode shows the usual chips; edit mode turns
 * them into removable entries plus an add field, and Save hands the
 * accumulated diff to the page as one merge patch.
 */
export function MetadataEditor({
  field,
  values,
  canEdit,
  onSave,
}: {
  field: 'labels' | 'annotations'
  values?: Record<string, string>
  canEdit: boolean
  /** Receives only the changed keys; null means delete. Throws on failure. */
  onSave: (changes: Record<string, string | null>) => Promise<void>
}) {
  const [editing, setEditing] = useState(false)
  // Snapshot of the entries when editing began. The diff is taken against
  // this, not the live values — the object refetches while the editor is
  // open, and a key added remotely mid-edit must not read as a removal.
  const [base, setBase] = useState<Record<string, string>>({})
  const [draft, setDraft] = useState<Record<string, string>>({})
  const [newKey, setNewKey] = useState('')
  const [newValue, setNewValue] = useState('')
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)

  if (!editing) {
    return (
      <div className="flex items-start gap-2">
        <LabelChips labels={values} />
        <GatedButton
          allowed={canEdit}
          deniedTitle="Requires patch on this resource"
          size="sm"
          variant="ghost"
          title={`Edit ${field}`}
          onClick={() => {
            setBase({ ...(values ?? {}) })
            setDraft({ ...(values ?? {}) })
            setNewKey('')
            setNewValue('')
            setError(undefined)
            setEditing(true)
          }}
        >
          Edit
        </GatedButton>
      </div>
    )
  }

  // Folds a typed-but-not-yet-added entry into the draft, so Save never
  // silently drops what is sitting in the add field. Returns undefined when
  // the pending entry is invalid.
  const withPending = (): Record<string, string> | undefined => {
    if (newKey === '' && newValue === '') return draft
    const invalid =
      validateMetaKey(newKey) ?? (field === 'labels' ? validateLabelValue(newValue) : undefined)
    if (invalid) {
      setError(invalid)
      return undefined
    }
    return { ...draft, [newKey]: newValue }
  }

  const add = () => {
    const next = withPending()
    if (!next) return
    setDraft(next)
    setNewKey('')
    setNewValue('')
    setError(undefined)
  }

  const save = async () => {
    const next = withPending()
    if (!next) return
    const changes = metaChanges(base, next)
    if (Object.keys(changes).length === 0) {
      setEditing(false)
      return
    }
    setBusy(true)
    try {
      await onSave(changes)
      setEditing(false)
    } catch {
      // The page already toasted; stay in edit mode so the draft survives.
    } finally {
      setBusy(false)
    }
  }

  const entries = Object.entries(draft)

  return (
    <div className="space-y-2">
      {entries.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {entries.map(([k, v]) => (
            <span
              key={k}
              className="inline-flex items-center gap-1 bg-canvas px-1.5 py-0.5 font-mono text-[11px] text-ink-muted ring-1 ring-border"
              title={`${k}=${v}`}
            >
              {k}
              <span className="text-ink-faint">=</span>
              {v}
              <button
                type="button"
                aria-label={`Remove ${k}`}
                disabled={busy}
                onClick={() =>
                  setDraft(Object.fromEntries(entries.filter(([key]) => key !== k)))
                }
                className="ml-0.5 px-0.5 text-ink-faint transition-colors hover:bg-danger/15 hover:text-danger"
              >
                ×
              </button>
            </span>
          ))}
        </div>
      )}

      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          add()
        }}
      >
        <TextInput
          value={newKey}
          onChange={(e) => setNewKey(e.target.value)}
          placeholder="key"
          disabled={busy}
          spellCheck={false}
          className="w-44 font-mono"
        />
        <span className="text-ink-faint">=</span>
        <TextInput
          value={newValue}
          onChange={(e) => setNewValue(e.target.value)}
          placeholder="value"
          disabled={busy}
          spellCheck={false}
          className="w-44 font-mono"
        />
        <Button size="sm" type="submit" disabled={busy || newKey === ''}>
          Add
        </Button>
      </form>

      {error && <p className="text-xs text-danger">{error}</p>}

      <div className="flex items-center gap-2">
        <Button size="sm" variant="primary" onClick={save} disabled={busy}>
          Save
        </Button>
        <Button size="sm" onClick={() => setEditing(false)} disabled={busy}>
          Cancel
        </Button>
      </div>
    </div>
  )
}
