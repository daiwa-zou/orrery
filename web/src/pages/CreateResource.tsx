import { useMemo, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { yaml } from '@codemirror/lang-yaml'

import { api, apiGroup } from '../api/client'
import { useDiscovery } from '../api/hooks'
import { namespacesIn, onlyNamespace } from '../lib/scope'
import { ExplainPanel } from '../components/ExplainPanel'
import { codeTheme } from '../components/YamlEditor'
import { Button, Corners, Spinner } from '../components/primitives'
import { useToast } from '../components/Toast'

/**
 * kubectl create -f, as a page. The manifest starts as a skeleton for the
 * resource the user was looking at, but nothing stops them pasting any kind —
 * the server routes on the manifest's own apiVersion/kind via the URL path,
 * so the skeleton is a convenience, not a constraint.
 */
export function CreateResource() {
  const { cluster, group, version, resource } = useParams<{
    cluster: string
    group: string
    version: string
    resource: string
  }>()
  const [params] = useSearchParams()
  // Creating an object has to put it in exactly one namespace, so a scope of
  // several prefills nothing: picking one of four for the reader would be
  // worse than leaving the field to them.
  const namespace = onlyNamespace(namespacesIn(params)) ?? ''
  const navigate = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()

  const { data: discovery } = useDiscovery(cluster)
  const meta = useMemo(() => {
    const g = apiGroup(group!)
    for (const grp of discovery?.groups ?? []) {
      for (const r of grp.resources) {
        if (r.group === g && r.version === version && r.name === resource) return r
      }
    }
    return undefined
  }, [discovery, group, version, resource])

  const template = useMemo(() => {
    if (!meta) return ''
    const apiVersion = meta.group ? `${meta.group}/${meta.version}` : meta.version
    const lines = [`apiVersion: ${apiVersion}`, `kind: ${meta.kind}`, 'metadata:', '  name: ']
    if (meta.namespaced) lines.push(`  namespace: ${namespace || 'default'}`)
    lines.push('')
    return lines.join('\n')
  }, [meta, namespace])

  const [draft, setDraft] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const [explainOpen, setExplainOpen] = useState(true)
  const value = draft ?? template

  const create = async () => {
    if (!cluster || !group || !version || !resource) return
    setBusy(true)
    setError(undefined)
    try {
      const created = await api.create(
        {
          cluster,
          group: apiGroup(group!),
          version,
          resource,
          namespace: namespace || undefined,
        },
        value,
      )
      toast.push({ tone: 'ok', title: `Created ${created.kind}/${created.metadata.name}` })
      qc.invalidateQueries({ queryKey: ['list'] })
      const ns = created.metadata.namespace ?? '_'
      navigate(`/c/${cluster}/r/${group}/${version}/${resource}/${ns}/${created.metadata.name}`)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-border bg-surface px-4 py-2">
        <h1 className="font-condensed text-[17px] font-semibold tracking-[.02em] text-ink">
          New {meta?.kind ?? resource}
        </h1>
        <span className="text-xs text-ink-faint">
          The manifest is submitted as written — the same contract as kubectl create -f.
        </span>
        <div className="flex-1" />
        <Button size="sm" onClick={() => setExplainOpen((v) => !v)} title="kubectl explain, inline">
          {explainOpen ? 'Hide reference' : 'Field reference'}
        </Button>
        <Button size="sm" onClick={() => navigate(-1)} disabled={busy}>
          Cancel
        </Button>
        <Button variant="primary" size="sm" onClick={create} disabled={busy || !value.trim()}>
          {busy && <Spinner className="size-3.5" />}
          Create
        </Button>
      </div>

      {error && (
        <p className="border-b border-border bg-danger/10 px-4 py-2 text-xs break-words text-danger">
          {error}
        </p>
      )}

      <div className="flex min-h-0 flex-1">
        <div className="min-h-0 min-w-0 flex-1 p-3">
          <div className="blueprint h-full overflow-auto bg-code">
            <Corners />
            <CodeMirror
              value={value}
              height="100%"
              className="h-full"
              theme="none"
              extensions={[yaml(), codeTheme]}
              onChange={setDraft}
              basicSetup={{ lineNumbers: true, foldGutter: true, autocompletion: false }}
            />
          </div>
        </div>
        {explainOpen && meta && cluster && (
          <ExplainPanel
            cluster={cluster}
            group={meta.group}
            version={meta.version}
            kind={meta.kind}
          />
        )}
      </div>
    </div>
  )
}
