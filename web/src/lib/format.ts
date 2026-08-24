/** Formatting helpers shared by tables, detail panes and charts. */

const UNITS: [number, string][] = [
  [365 * 24 * 3600, 'y'],
  [24 * 3600, 'd'],
  [3600, 'h'],
  [60, 'm'],
  [1, 's'],
]

/**
 * Renders an age the way kubectl does: at most two units, and a single unit
 * once the value is large enough that the second stops carrying information.
 */
export function age(timestamp?: string | null, now: number = Date.now()): string {
  if (!timestamp) return '—'
  const then = Date.parse(timestamp)
  if (Number.isNaN(then)) return '—'

  let seconds = Math.max(0, Math.floor((now - then) / 1000))
  if (seconds < 1) return '0s'

  const parts: string[] = []
  for (const [size, label] of UNITS) {
    if (seconds < size) continue
    const value = Math.floor(seconds / size)
    seconds -= value * size
    parts.push(`${value}${label}`)
    if (parts.length === 2) break
  }
  return parts.join('') || '0s'
}

/**
 * Duration between two timestamps. The Job duration column ships the pair
 * encoded as "start|end" so the server never has to guess the viewer's clock.
 */
export function duration(encoded: unknown): string {
  if (typeof encoded !== 'string' || encoded === '') return '—'
  if (encoded === 'running') return 'running'

  const [start, end] = encoded.split('|')
  if (!end) return age(start)

  const ms = Date.parse(end) - Date.parse(start)
  if (Number.isNaN(ms)) return '—'

  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m${s % 60}s`
  return `${Math.floor(s / 3600)}h${Math.floor((s % 3600) / 60)}m`
}

export function cpu(milli: number): string {
  if (milli < 1000) return `${milli}m`
  return `${(milli / 1000).toFixed(milli % 1000 === 0 ? 0 : 2)}`
}

export function memory(mib: number): string {
  if (mib < 1024) return `${Math.round(mib)} MiB`
  return `${(mib / 1024).toFixed(1)} GiB`
}

export function percent(value: number): string {
  return `${value.toFixed(value < 10 ? 1 : 0)}%`
}

/** Severity used to colour a status badge. */
export type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'idle'

const DANGER = new Set([
  'CrashLoopBackOff',
  'Error',
  'Failed',
  'ErrImagePull',
  'ImagePullBackOff',
  'CreateContainerConfigError',
  'CreateContainerError',
  'InvalidImageName',
  'OOMKilled',
  'Evicted',
  'NotReady',
  'unreachable',
  'Degraded',
  'Lost',
  'Unknown',
])

const WARN = new Set([
  'Pending',
  'ContainerCreating',
  'PodInitializing',
  'Terminating',
  'Progressing',
  'SchedulingDisabled',
  'Not scheduled',
  'Warning',
  'degraded',
  'Released',
])

const OK = new Set([
  'Running',
  'Ready',
  'Active',
  'Bound',
  'Complete',
  'Completed',
  'Succeeded',
  'Available',
  'Healthy',
  'healthy',
  'Normal',
])

export function toneFor(value: string): Tone {
  if (!value) return 'idle'
  if (DANGER.has(value)) return 'danger'
  if (WARN.has(value)) return 'warn'
  if (OK.has(value)) return 'ok'
  if (value.startsWith('Init:')) return 'warn'
  if (value === 'Scaled to zero' || value === 'Suspended') return 'idle'
  return 'info'
}

/** A "2/3" ratio is healthy only when both halves agree. */
export function ratioTone(value: string): Tone {
  const [have, want] = value.split('/').map((n) => Number.parseInt(n, 10))
  if (Number.isNaN(have) || Number.isNaN(want)) return 'idle'
  if (want === 0) return 'idle'
  if (have >= want) return 'ok'
  if (have === 0) return 'danger'
  return 'warn'
}

/**
 * Kind to the plural resource name the API uses. Only needed for owner links,
 * where all we have is a kind; everywhere else the resource name comes from
 * discovery and no guessing is involved.
 */
export function kindToResource(kind: string): string {
  const irregular: Record<string, string> = {
    Endpoints: 'endpoints',
    Ingress: 'ingresses',
    NetworkPolicy: 'networkpolicies',
    PodDisruptionBudget: 'poddisruptionbudgets',
    PriorityClass: 'priorityclasses',
    StorageClass: 'storageclasses',
    CustomResourceDefinition: 'customresourcedefinitions',
  }
  if (irregular[kind]) return irregular[kind]

  const lower = kind.toLowerCase()
  if (lower.endsWith('s')) return `${lower}es`
  if (lower.endsWith('y')) return `${lower.slice(0, -1)}ies`
  return `${lower}s`
}

/** Splits "apps/v1" into its group and version. */
export function splitApiVersion(apiVersion?: string): { group: string; version: string } {
  if (!apiVersion) return { group: '', version: 'v1' }
  const parts = apiVersion.split('/')
  return parts.length === 1
    ? { group: '', version: parts[0] }
    : { group: parts[0], version: parts[1] }
}

/** Truncates the middle of a long identifier, keeping both ends legible. */
export function ellipsize(value: string, max = 44): string {
  if (value.length <= max) return value
  const head = Math.ceil((max - 1) / 2)
  const tail = Math.floor((max - 1) / 2)
  return `${value.slice(0, head)}…${value.slice(value.length - tail)}`
}

/**
 * Nav labels for kinds whose real name does not fit a 256px sidebar, plus the
 * handful whose plural is irregular. Everything else is pluralised by rule.
 */
const NAV_LABELS: Record<string, string> = {
  PersistentVolumeClaim: 'PVCs',
  PersistentVolume: 'PVs',
  HorizontalPodAutoscaler: 'HPAs',
  PodDisruptionBudget: 'PDBs',
  CustomResourceDefinition: 'CRDs',
  ValidatingWebhookConfiguration: 'Validating webhooks',
  MutatingWebhookConfiguration: 'Mutating webhooks',
  ValidatingAdmissionPolicyBinding: 'Admission policy bindings',
  ValidatingAdmissionPolicy: 'Admission policies',
  // Already plural; the rule below would produce "Endpointses".
  Endpoints: 'Endpoints',
}

/**
 * Pluralises a Kubernetes kind for display. Nav entries name a collection, so
 * "Pods" reads better than "Pod" above a table of them.
 */
export function pluralize(kind: string): string {
  if (!kind) return kind
  const lower = kind.toLowerCase()

  if (/(?:s|x|ch|sh)$/.test(lower)) return `${kind}es`
  if (/[^aeiou]y$/.test(lower)) return `${kind.slice(0, -1)}ies`
  return `${kind}s`
}

/**
 * The label a kind gets in the sidebar: a short override when one exists,
 * otherwise its plural. The full kind is still shown in the command palette so
 * an abbreviation never leaves you guessing.
 */
export function navLabel(kind: string): string {
  return NAV_LABELS[kind] ?? pluralize(kind)
}
