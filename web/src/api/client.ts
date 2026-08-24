import type {
  AccessCheck,
  Decision,
  DiscoveryResponse,
  KubeObject,
  ListResponse,
  Me,
  MetricsResponse,
  Overview,
  ClusterSummary,
} from './types'

const BASE = '/api/v1'

/** Thrown for every non-2xx response, carrying the API's structured reason. */
export class ApiError extends Error {
  status: number
  kind: string

  constructor(status: number, kind: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.kind = kind
  }

  get isAuth() {
    return this.status === 401
  }

  get isForbidden() {
    return this.status === 403
  }
}

function csrfToken(): string {
  const match = document.cookie.match(/(?:^|;\s*)orrery_csrf=([^;]+)/)
  return match ? decodeURIComponent(match[1]) : ''
}

async function request<T>(
  path: string,
  init: RequestInit = {},
  signal?: AbortSignal,
): Promise<T> {
  const headers = new Headers(init.headers)
  const method = (init.method ?? 'GET').toUpperCase()

  if (method !== 'GET' && method !== 'HEAD') {
    // Double-submit: the cookie is readable by script, the header is not
    // forgeable cross-origin.
    headers.set('X-CSRF-Token', csrfToken())
    if (!headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  }

  // An abort surfaces as a DOMException named AbortError, never an ApiError,
  // so cancelled requests bypass the 401 redirect and error handling below.
  const res = await fetch(BASE + path, {
    ...init,
    headers,
    credentials: 'same-origin',
    signal: signal ?? init.signal,
  })

  if (res.status === 204) return undefined as T

  const contentType = res.headers.get('Content-Type') ?? ''
  const isJson = contentType.includes('application/json')

  if (!res.ok) {
    let kind = 'error'
    let reason = `${res.status} ${res.statusText}`
    if (isJson) {
      try {
        const body = await res.json()
        kind = body.error ?? kind
        reason = body.reason ?? reason
      } catch {
        // Keep the status-line fallback.
      }
    }
    // A 401 on a write means the session died between poll ticks; a toast
    // saying "401" helps nobody, so route to sign-in like the query path does.
    if (res.status === 401 && method !== 'GET' && window.location.pathname !== '/login') {
      const returnTo = encodeURIComponent(window.location.pathname + window.location.search)
      window.location.href = `/login?returnTo=${returnTo}`
    }
    throw new ApiError(res.status, kind, reason)
  }

  if (!isJson) return (await res.text()) as unknown as T
  return (await res.json()) as T
}

/** Encodes the core group as "core", matching the server's URL convention. */
export function groupSegment(group: string): string {
  return group === '' ? 'core' : group
}

/** Cluster-scoped objects use "_" in the namespace position. */
export function nsSegment(namespace?: string): string {
  return namespace && namespace.length > 0 ? namespace : '_'
}

export interface ListParams {
  namespace?: string
  q?: string
  labelSelector?: string
  fieldSelector?: string
  sort?: string
  order?: 'asc' | 'desc'
  page?: number
  pageSize?: number
  view?: 'table' | 'full'
}

function qs(params: Record<string, unknown>): string {
  const sp = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '') continue
    sp.set(k, String(v))
  }
  const s = sp.toString()
  return s ? `?${s}` : ''
}

export interface ExplainField {
  name: string
  type: string
  description?: string
  required?: boolean
  hasChildren?: boolean
}

export interface ExplainResponse {
  kind: string
  fieldPath?: string
  type: string
  description?: string
  fields?: ExplainField[]
}

/** URL of the read-only HTTP proxy into a pod or service port. */
export function proxyURL(
  cluster: string,
  namespace: string,
  ptype: 'pods' | 'services',
  name: string,
  port: string | number,
): string {
  return `${BASE}/clusters/${cluster}/proxy/${namespace}/${ptype}/${name}:${port}/`
}

export interface Revision {
  revision: number
  name: string
  images: string[]
  replicas: number
  current: boolean
  changeCause?: string
  createdAt: string
}

export interface ResourceRef {
  cluster: string
  group: string
  version: string
  resource: string
  namespace?: string
  name?: string
}

export const api = {
  me: (signal?: AbortSignal) => request<Me>('/me', {}, signal),

  authConfig: () =>
    request<{ oidcEnabled: boolean; anonymous: boolean; loginPath: string }>('/auth/config'),

  logout: () => request<{ loggedOut: boolean; endSessionURL?: string }>('/auth/logout', { method: 'POST' }),

  clusters: (signal?: AbortSignal) =>
    request<{ clusters: ClusterSummary[] }>('/clusters', {}, signal),

  discovery: (cluster: string, signal?: AbortSignal) =>
    request<DiscoveryResponse>(`/clusters/${cluster}/discovery`, {}, signal),

  overview: (cluster: string, signal?: AbortSignal) =>
    request<Overview>(`/clusters/${cluster}/overview`, {}, signal),

  nodeMetrics: (cluster: string, signal?: AbortSignal) =>
    request<MetricsResponse>(`/clusters/${cluster}/metrics/nodes`, {}, signal),

  podMetrics: (cluster: string, namespace?: string, signal?: AbortSignal) =>
    request<MetricsResponse>(`/clusters/${cluster}/metrics/pods${qs({ namespace })}`, {}, signal),

  cacheStats: (cluster: string, signal?: AbortSignal) =>
    request<{ cluster: string; informers: unknown[]; totalObjects: number }>(
      `/clusters/${cluster}/stats`,
      {},
      signal,
    ),

  list: (ref: ResourceRef, params: ListParams = {}, signal?: AbortSignal) =>
    request<ListResponse>(
      `/clusters/${ref.cluster}/resources/${groupSegment(ref.group)}/${ref.version}/${ref.resource}` +
        qs(params as Record<string, unknown>),
      {},
      signal,
    ),

  get: (ref: ResourceRef, signal?: AbortSignal) =>
    request<KubeObject>(
      `/clusters/${ref.cluster}/resources/${groupSegment(ref.group)}/${ref.version}/` +
        `${ref.resource}/${nsSegment(ref.namespace)}/${ref.name}`,
      {},
      signal,
    ),

  getYaml: (ref: ResourceRef, signal?: AbortSignal) =>
    request<string>(
      `/clusters/${ref.cluster}/resources/${groupSegment(ref.group)}/${ref.version}/` +
        `${ref.resource}/${nsSegment(ref.namespace)}/${ref.name}?format=yaml`,
      {},
      signal,
    ),

  replace: (ref: ResourceRef, body: string) =>
    request<KubeObject>(
      `/clusters/${ref.cluster}/resources/${groupSegment(ref.group)}/${ref.version}/` +
        `${ref.resource}/${nsSegment(ref.namespace)}/${ref.name}`,
      { method: 'PUT', body, headers: { 'Content-Type': 'application/yaml' } },
    ),

  /** JSON merge patch (RFC 7386): a null value deletes that key. */
  patch: (ref: ResourceRef, body: unknown) =>
    request<KubeObject>(
      `/clusters/${ref.cluster}/resources/${groupSegment(ref.group)}/${ref.version}/` +
        `${ref.resource}/${nsSegment(ref.namespace)}/${ref.name}`,
      {
        method: 'PATCH',
        body: JSON.stringify(body),
        headers: { 'Content-Type': 'application/merge-patch+json' },
      },
    ),

  create: (ref: Omit<ResourceRef, 'name'>, body: string) =>
    request<KubeObject>(
      `/clusters/${ref.cluster}/resources/${groupSegment(ref.group)}/${ref.version}/${ref.resource}` +
        qs({ namespace: ref.namespace }),
      { method: 'POST', body, headers: { 'Content-Type': 'application/yaml' } },
    ),

  remove: (ref: ResourceRef, propagationPolicy = 'Background') =>
    request<{ deleted: boolean }>(
      `/clusters/${ref.cluster}/resources/${groupSegment(ref.group)}/${ref.version}/` +
        `${ref.resource}/${nsSegment(ref.namespace)}/${ref.name}` +
        qs({ propagationPolicy }),
      { method: 'DELETE' },
    ),

  events: (
    cluster: string,
    params: {
      namespace?: string
      involvedName?: string
      involvedKind?: string
      involvedUID?: string
      warningsOnly?: boolean
      limit?: number
    },
    signal?: AbortSignal,
  ) => request<ListResponse>(`/clusters/${cluster}/events${qs(params)}`, {}, signal),

  podLogs: (
    cluster: string,
    namespace: string,
    name: string,
    params: { container?: string; tailLines?: number; previous?: boolean; timestamps?: boolean },
  ) => request<string>(`/clusters/${cluster}/pods/${namespace}/${name}/logs${qs(params)}`),

  access: async (cluster: string, checks: AccessCheck[], signal?: AbortSignal): Promise<Decision[]> => {
    if (checks.length === 0) return []
    const res = await request<{ results: Record<string, Decision> }>(
      `/clusters/${cluster}/access`,
      { method: 'POST', body: JSON.stringify({ checks }) },
      signal,
    )
    return checks.map((_, i) => res.results[String(i)] ?? { allowed: false })
  },

  scale: (cluster: string, ref: Omit<ResourceRef, 'cluster'>, replicas: number) =>
    request<{ scaled: boolean }>(`/clusters/${cluster}/actions/scale`, {
      method: 'POST',
      body: JSON.stringify({ ...ref, group: groupSegment(ref.group), replicas }),
    }),

  restart: (cluster: string, ref: Omit<ResourceRef, 'cluster'>) =>
    request<{ restarted: boolean }>(`/clusters/${cluster}/actions/restart`, {
      method: 'POST',
      body: JSON.stringify({ ...ref, group: groupSegment(ref.group) }),
    }),

  explain: (
    cluster: string,
    params: { group: string; version: string; kind: string; field?: string },
    signal?: AbortSignal,
  ) => request<ExplainResponse>(`/clusters/${cluster}/explain${qs(params)}`, {}, signal),

  rolloutHistory: (cluster: string, namespace: string, name: string) =>
    request<{ revisions: Revision[] }>(
      `/clusters/${cluster}/rollout/history${qs({ namespace, name })}`,
    ),

  rolloutUndo: (cluster: string, namespace: string, name: string, toRevision?: number) =>
    request<{ rolledBack: boolean; toRevision: number }>(
      `/clusters/${cluster}/actions/rollout-undo`,
      { method: 'POST', body: JSON.stringify({ namespace, name, toRevision }) },
    ),

  triggerCronJob: (cluster: string, namespace: string, name: string) =>
    request<{ triggered: boolean; job: string; namespace: string }>(
      `/clusters/${cluster}/actions/trigger-cronjob`,
      { method: 'POST', body: JSON.stringify({ namespace, name }) },
    ),

  suspendCronJob: (cluster: string, namespace: string, name: string, suspend: boolean) =>
    request<{ suspended: boolean }>(`/clusters/${cluster}/actions/suspend-cronjob`, {
      method: 'POST',
      body: JSON.stringify({ namespace, name, suspend }),
    }),

  cordon: (cluster: string, node: string, unschedulable: boolean) =>
    request<{ node: string }>(`/clusters/${cluster}/actions/cordon`, {
      method: 'POST',
      body: JSON.stringify({ node, unschedulable }),
    }),

  drain: (
    cluster: string,
    opts: { node: string; ignoreDaemonSets: boolean; deleteEmptyDirData: boolean; dryRun: boolean },
  ) =>
    request<{
      node: string
      cordoned: boolean
      evicted: string[]
      skipped: string[]
      failed: string[]
      /** Pods on the node the caller may not evict — a count, not names. */
      notPermitted?: number
      dryRun: boolean
    }>(`/clusters/${cluster}/actions/drain`, { method: 'POST', body: JSON.stringify(opts) }),

  evict: (cluster: string, namespace: string, pod: string) =>
    request<{ evicted: boolean }>(`/clusters/${cluster}/actions/evict`, {
      method: 'POST',
      body: JSON.stringify({ namespace, pod }),
    }),
}

/** Builds an absolute ws:// or wss:// URL for a streaming endpoint. */
export function wsURL(path: string, params: Record<string, unknown> = {}): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}${BASE}${path}${qs(params)}`
}
