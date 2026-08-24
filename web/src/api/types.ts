// Types mirroring the Go API. Kept hand-written and small: the surface is one
// uniform resource endpoint, so there is little to generate.

export type ColumnType =
  | 'text'
  | 'number'
  | 'age'
  | 'status'
  | 'list'
  | 'bool'
  | 'ratio'

export interface Column {
  key: string
  label: string
  type: ColumnType
  priority?: number
  align?: 'right'
}

/** A projected table row. Keys correspond to Column.key, plus metadata. */
export interface Row {
  name: string
  namespace?: string
  uid: string
  age: string
  _terminating?: boolean
  _labels?: Record<string, string>
  [key: string]: unknown
}

export interface ResourceMeta {
  group: string
  version: string
  name: string
  kind: string
  namespaced: boolean
  verbs: string[]
}

export interface Scope {
  allNamespaces: boolean
  namespaces?: string[]
  namespace?: string
}

export interface ListResponse {
  items?: Row[]
  objects?: KubeObject[]
  columns?: Column[]
  total: number
  page: number
  pageSize: number
  resource: ResourceMeta
  scope: Scope
  warnings?: string[]
}

export interface KubeObject {
  apiVersion?: string
  kind?: string
  metadata: {
    name: string
    namespace?: string
    uid?: string
    creationTimestamp?: string
    labels?: Record<string, string>
    annotations?: Record<string, string>
    ownerReferences?: { apiVersion?: string; kind: string; name: string; uid: string }[]
    deletionTimestamp?: string
    resourceVersion?: string
  }
  spec?: Record<string, unknown>
  status?: Record<string, unknown>
  [key: string]: unknown
}

export type HealthStatus = 'healthy' | 'degraded' | 'unreachable' | 'unknown'

export interface ClusterSummary {
  name: string
  displayName: string
  labels?: Record<string, string>
  authMode: string
  available: boolean
  error?: string
  health: {
    status: HealthStatus
    message?: string
    version?: string
    latencyMs: number
    checkedAt: string
  }
}

export interface APIResource {
  group: string
  version: string
  name: string
  singularName: string
  kind: string
  namespaced: boolean
  verbs: string[]
  shortNames?: string[]
  categories?: string[]
  preferred: boolean
}

export interface DiscoveryResponse {
  groups: { group: string; resources: APIResource[] }[]
  serverVersion: string
}

export interface CountSummary {
  total: number
  byStatus?: Record<string, number>
  forbidden?: boolean
  /** The server could not compute this (cache not synced) — not an RBAC "no". */
  unavailable?: boolean
}

export interface Usage {
  cpuMilli: number
  memoryMiB: number
}

export interface Overview {
  cluster: ClusterSummary
  nodes: CountSummary
  namespaces: CountSummary
  pods: CountSummary
  workloads: Record<string, CountSummary>
  warnings: {
    namespace: string
    reason: string
    message: string
    object: string
    count: number
    lastSeen: string
  }[]
  capacity?: Usage
  requested?: Usage
}

export interface NodeMetric {
  name: string
  usage: Usage
  capacity: Usage
  allocatable: Usage
  cpuPercent: number
  memPercent: number
}

export interface MetricsResponse {
  available: boolean
  reason?: string
  nodes?: NodeMetric[]
  pods?: { name: string; namespace: string; usage: Usage }[]
  totals?: Usage
}

export interface Me {
  authenticated: boolean
  oidcEnabled: boolean
  anonymous: boolean
  csrfToken?: string
  expiresAt?: string
  user: {
    username: string
    groups?: string[]
    email?: string
    name?: string
    picture?: string
  }
}

export interface AccessCheck {
  verb: string
  group: string
  version?: string
  resource: string
  subresource?: string
  namespace?: string
  name?: string
}

export interface Decision {
  allowed: boolean
  denied?: boolean
  reason?: string
}

/** Live watch messages. */
export type WatchMessage =
  | { type: 'INIT'; columns: Column[]; items: Row[]; resource: ResourceMeta }
  | { type: 'ADDED' | 'MODIFIED' | 'DELETED'; item: Row }
  | { type: 'SYNCED' }
  | { type: 'OVERFLOW' }
  | { type: 'ERROR'; message: string }

export type LogMessage =
  | { type: 'LOG'; lines: string[] }
  | { type: 'EOF' }
  | { type: 'ERROR'; message: string }
