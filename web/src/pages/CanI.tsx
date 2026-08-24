import clsx from 'clsx'
import { useId, useMemo, useState } from 'react'
import { useParams } from 'react-router-dom'
import { api } from '../api/client'
import { useDiscovery, useNamespaces } from '../api/hooks'
import type { AccessCheck, Decision } from '../api/types'
import { Badge, Button, Spinner } from '../components/primitives'

/* kubectl auth can-i, as a page. One question per ask; the server answers with
   the cluster's own SubjectAccessReview, so this reflects real RBAC rather
   than anything the dashboard infers. */

const VERBS = ['get', 'list', 'watch', 'create', 'update', 'patch', 'delete']
const OTHER = '__other__'
const CUSTOM = '__custom__'

interface Asked {
  check: AccessCheck
  decision: Decision
}

/** Renders a check the way people write it on the kubectl command line. */
function describe(c: AccessCheck): string {
  let target = c.resource
  if (c.group) target += `.${c.group}`
  if (c.subresource) target += `/${c.subresource}`
  let s = `${c.verb} ${target}`
  if (c.name) s += ` ${c.name}`
  s += c.namespace ? ` -n ${c.namespace}` : ' (cluster-wide)'
  return s
}

export function CanI() {
  const { cluster } = useParams<{ cluster: string }>()
  const { data: discovery } = useDiscovery(cluster)
  const { names: namespaceNames } = useNamespaces(cluster)
  const nsListId = useId()

  // The picker offers what discovery knows, but none of it is required: an
  // RBAC rule can name a resource that does not exist yet, and asking about
  // it is still a legitimate question — kubectl allows it, so do we.
  const resources = useMemo(() => {
    const all = discovery?.groups.flatMap((g) => g.resources) ?? []
    return [...all].sort(
      (a, b) => a.name.localeCompare(b.name) || a.group.localeCompare(b.group),
    )
  }, [discovery])

  const [verbChoice, setVerbChoice] = useState('get')
  const [customVerb, setCustomVerb] = useState('')
  const [resourceChoice, setResourceChoice] = useState('')
  const [customGroup, setCustomGroup] = useState('')
  const [customResource, setCustomResource] = useState('')
  const [subresource, setSubresource] = useState('')
  const [namespace, setNamespace] = useState('')
  const [name, setName] = useState('')

  const [asking, setAsking] = useState(false)
  const [askError, setAskError] = useState<string | undefined>()
  const [history, setHistory] = useState<Asked[]>([])

  const verb = verbChoice === OTHER ? customVerb.trim() : verbChoice
  const picked = resourceChoice !== CUSTOM ? resources[Number(resourceChoice)] : undefined
  const resource = resourceChoice === CUSTOM ? customResource.trim() : (picked?.name ?? '')
  const group = resourceChoice === CUSTOM ? customGroup.trim() : (picked?.group ?? '')

  const ready = verb !== '' && resource !== '' && !asking

  const ask = async (check: AccessCheck) => {
    setAsking(true)
    setAskError(undefined)
    try {
      const [decision] = await api.access(cluster!, [check])
      setHistory((prev) => [{ check, decision }, ...prev].slice(0, 20))
    } catch (err) {
      setAskError(err instanceof Error ? err.message : 'The check failed.')
    } finally {
      setAsking(false)
    }
  }

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!ready) return
    void ask({
      verb,
      group,
      version: picked?.version || undefined,
      resource,
      subresource: subresource.trim() || undefined,
      namespace: namespace.trim() || undefined,
      name: name.trim() || undefined,
    })
  }

  /** Puts a past question back into the form so it can be varied and re-asked. */
  const restore = (c: AccessCheck) => {
    if (VERBS.includes(c.verb)) {
      setVerbChoice(c.verb)
    } else {
      setVerbChoice(OTHER)
      setCustomVerb(c.verb)
    }
    const idx = resources.findIndex((r) => r.name === c.resource && r.group === c.group)
    if (idx >= 0) {
      setResourceChoice(String(idx))
    } else {
      setResourceChoice(CUSTOM)
      setCustomGroup(c.group)
      setCustomResource(c.resource)
    }
    setSubresource(c.subresource ?? '')
    setNamespace(c.namespace ?? '')
    setName(c.name ?? '')
  }

  const latest = history[0]

  const fieldClass =
    'w-full rounded-md bg-surface-2 px-2.5 py-1.5 text-sm text-ink ring-1 ring-border placeholder:text-ink-faint'
  const labelClass = 'mb-1 block text-[11px] tracking-wide text-ink-faint uppercase'

  return (
    <div className="mx-auto w-full max-w-2xl px-6 py-6">
      <h1 className="text-sm font-semibold text-ink">Can I?</h1>
      <p className="mt-1 text-sm text-ink-faint">
        Asks the cluster whether your current identity may perform an action — the same
        question as <code className="font-mono text-[12px]">kubectl auth can-i</code>.
      </p>

      <form onSubmit={submit} className="mt-5 space-y-4">
        <div className="grid grid-cols-2 gap-3">
          <label className="block">
            <span className={labelClass}>Verb</span>
            <select
              value={verbChoice}
              onChange={(e) => setVerbChoice(e.target.value)}
              className={fieldClass}
            >
              {VERBS.map((v) => (
                <option key={v} value={v}>
                  {v}
                </option>
              ))}
              <option value={OTHER}>other…</option>
            </select>
          </label>
          {verbChoice === OTHER && (
            <label className="block">
              <span className={labelClass}>Custom verb</span>
              <input
                value={customVerb}
                onChange={(e) => setCustomVerb(e.target.value)}
                placeholder="e.g. impersonate, bind, escalate"
                className={fieldClass}
              />
            </label>
          )}
        </div>

        <div className="grid grid-cols-[2fr_1fr] gap-3">
          <label className="block">
            <span className={labelClass}>Resource</span>
            <select
              value={resourceChoice}
              onChange={(e) => setResourceChoice(e.target.value)}
              className={fieldClass}
            >
              <option value="" disabled>
                Select a resource
              </option>
              {resources.map((r, i) => (
                <option key={`${r.group}/${r.version}/${r.name}`} value={String(i)}>
                  {r.name}
                  {r.group ? `.${r.group}` : ''}
                </option>
              ))}
              <option value={CUSTOM}>other…</option>
            </select>
          </label>
          <label className="block">
            <span className={labelClass}>Subresource</span>
            <input
              value={subresource}
              onChange={(e) => setSubresource(e.target.value)}
              placeholder="e.g. log, scale"
              className={fieldClass}
            />
          </label>
        </div>

        {resourceChoice === CUSTOM && (
          <div className="grid grid-cols-2 gap-3">
            <label className="block">
              <span className={labelClass}>Custom resource</span>
              <input
                value={customResource}
                onChange={(e) => setCustomResource(e.target.value)}
                placeholder="plural name, e.g. widgets"
                className={fieldClass}
              />
            </label>
            <label className="block">
              <span className={labelClass}>API group</span>
              <input
                value={customGroup}
                onChange={(e) => setCustomGroup(e.target.value)}
                placeholder="empty for core"
                className={fieldClass}
              />
            </label>
          </div>
        )}

        <div className="grid grid-cols-2 gap-3">
          <label className="block">
            <span className={labelClass}>Namespace</span>
            <input
              value={namespace}
              onChange={(e) => setNamespace(e.target.value)}
              placeholder="empty asks cluster-wide"
              list={nsListId}
              className={fieldClass}
            />
            <datalist id={nsListId}>
              {namespaceNames.map((n) => (
                <option key={n} value={n} />
              ))}
            </datalist>
          </label>
          <label className="block">
            <span className={labelClass}>Name</span>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="empty asks about all"
              className={fieldClass}
            />
          </label>
        </div>

        <div className="flex items-center gap-3">
          <Button type="submit" variant="primary" disabled={!ready}>
            {asking && <Spinner className="size-3.5" />} Ask
          </Button>
          {askError && <span className="text-sm text-danger">{askError}</span>}
        </div>
      </form>

      {latest && (
        <div
          className={clsx(
            'mt-6 rounded-lg p-4 ring-1',
            latest.decision.allowed
              ? 'bg-ok/8 ring-ok/25'
              : 'bg-danger/8 ring-danger/25',
          )}
        >
          <div className="flex items-center gap-2">
            <Badge tone={latest.decision.allowed ? 'ok' : 'danger'}>
              {latest.decision.allowed ? 'Yes' : 'No'}
            </Badge>
            <code className="font-mono text-[12px] text-ink-muted">
              {describe(latest.check)}
            </code>
          </div>
          <p className="mt-2 text-sm text-ink-muted">
            {latest.decision.reason ??
              (latest.decision.allowed
                ? 'A policy explicitly allows this.'
                : latest.decision.denied
                  ? 'A policy explicitly denies this.'
                  : 'No policy allows this, so it is denied by default.')}
          </p>
        </div>
      )}

      {history.length > 1 && (
        <div className="mt-6">
          <p className="text-[11px] font-semibold tracking-wider text-ink-faint uppercase">
            Asked earlier
          </p>
          <ul className="mt-1.5 divide-y divide-border">
            {history.slice(1).map((h, i) => (
              <li key={i}>
                <button
                  onClick={() => restore(h.check)}
                  title="Put this question back into the form"
                  className="flex w-full items-center gap-2 py-1.5 text-left transition-colors hover:bg-surface-2"
                >
                  <Badge tone={h.decision.allowed ? 'ok' : 'danger'}>
                    {h.decision.allowed ? 'Yes' : 'No'}
                  </Badge>
                  <code className="truncate font-mono text-[12px] text-ink-muted">
                    {describe(h.check)}
                  </code>
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}
