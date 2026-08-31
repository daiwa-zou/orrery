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
  /** Client-side only: renders Row._labels as clickable filter chips. */
  | 'labels'
  /** Client-side only: a small usage bar; the cell value is a BarCell. */
  | 'bar'

export interface Column {
  key: string
  label: string
  type: ColumnType
  priority?: number
  align?: 'right'
}

/** The value behind a "bar" column: a formatted reading plus its fill. */
export interface BarCell {
  text: string
  /** 0–100; drives the fill width and the warn/danger colour steps. */
  percent: number
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

/** Search-autocomplete vocabulary for one resource. */
export interface Facet {
  key: string
  values: string[]
}

export interface FacetsResponse {
  labels: Facet[]
  fields: Facet[]
  truncated?: boolean
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
  /**
   * Why `warnings` is empty when it is. An empty feed is the one field a
   * reader takes as reassurance, so it must not be shown as one unless the
   * events were actually read.
   */
  warningsForbidden?: boolean
  warningsUnavailable?: boolean
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
  pods?: {
    name: string
    namespace: string
    usage: Usage
    /** Summed container limits; absent when no container declares one. */
    limits?: Usage
  }[]
  totals?: Usage
  /**
   * What the reading is missing: a namespace whose metrics could not be read,
   * or a pod cache that could not be consulted for limits. `available` is true
   * and the numbers are real — they are simply a lower bound, and a total that
   * is short without saying so does not read as "some pods are missing", it
   * reads as "these pods are using less than you thought".
   */
  warnings?: string[]
}

export interface Me {
  authenticated: boolean
  oidcEnabled: boolean
  anonymous: boolean
  /**
   * Optional capabilities the server is actually serving. The console hides
   * what is switched off rather than offering a control that 404s.
   *
   * Absent on an older server, so callers should treat a missing flag as on
   * and let the request fail honestly if it is not.
   */
  features?: {
    proxy?: boolean
  }
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
  /**
   * The review never happened — `allowed` is false because nothing was
   * decided, not because anything was refused. Never render this as a
   * permission problem; `reason` says what actually went wrong.
   */
  unavailable?: boolean
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
  /** `pod` is present only on an aggregated stream; a batch is one pod's lines. */
  | { type: 'LOG'; lines: string[]; pod?: string }
  | { type: 'EOF' }
  /** One pod of a merged feed could not be read; the others continue. */
  | { type: 'STREAM_ERROR'; pod: string; reason: string }
  | { type: 'ERROR'; message: string }

/**
 * One object in another's neighbourhood, as the related endpoint returns it.
 *
 * `path` is the API route that serves this object, already assembled by the
 * server from resolved discovery. That is the point of reading it rather than
 * rebuilding one: a link built here has to guess the plural of a kind, and a
 * CRD may spell its plural however it likes.
 */
export interface ObjectRef {
  /**
   * Why this object is here: owner, child, descendant, node, hosts, selects,
   * selected-by, reference. Treated as an open set — an unknown relation is
   * grouped under its own heading rather than dropped.
   */
  relation: string
  /** Ownership hops from the subject; absent for non-ownership edges. */
  depth?: number
  group?: string
  version?: string
  resource?: string
  kind: string
  namespace?: string
  name: string
  uid?: string
  path?: string
  /** One-word health, for the kinds that have one. Absent is not "healthy". */
  status?: string
  /** Why a link could not be followed, when it could not be. */
  note?: string
}

export interface RelatedResponse {
  object: ObjectRef
  related: ObjectRef[]
  events?: Row[]
  eventColumns?: Column[]
  /** Scans that were skipped and why: a short answer is not a complete one. */
  warnings?: string[]
  truncated?: boolean
}

/** One object found by the cross-cluster search. */
export interface SearchHit {
  cluster: string
  group?: string
  version: string
  resource: string
  kind: string
  namespace?: string
  name: string
  /** The API route serving it, assembled server-side from resolved discovery. */
  path: string
  status?: string
  /** Higher is a better match: exact name, then prefix, then label-only. */
  score: number
}

export interface SearchResponse {
  query: string
  hits: SearchHit[]
  total: number
  limit: number
  /** Which resources were actually scanned. */
  scanned: string[]
  /** Clusters and resources that could not be searched. */
  warnings?: string[]
  truncated?: boolean
}
