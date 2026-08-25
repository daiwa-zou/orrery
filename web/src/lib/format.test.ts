import { describe, expect, it } from 'vitest'
import {
  age,
  cpu,
  duration,
  kindToResource,
  memory,
  navLabel,
  ratioTone,
  splitApiVersion,
  toneFor,
  RESTARTABLE_KINDS,
  RESTARTABLE_RESOURCES,
} from './format'

// A fixed "now" keeps every age assertion deterministic.
const NOW = Date.parse('2026-01-02T12:00:00Z')
const before = (seconds: number) => new Date((NOW - seconds * 1000)).toISOString()

describe('age', () => {
  it('renders a dash for missing or unparseable timestamps', () => {
    expect(age(undefined, NOW)).toBe('—')
    expect(age(null, NOW)).toBe('—')
    expect(age('not a date', NOW)).toBe('—')
  })

  it('clamps future timestamps to zero', () => {
    // Clock skew between the cluster and the viewer must not render "-3s".
    expect(age(before(-3), NOW)).toBe('0s')
  })

  it('uses at most two units, like kubectl', () => {
    expect(age(before(42), NOW)).toBe('42s')
    expect(age(before(62), NOW)).toBe('1m2s')
    expect(age(before(3600 + 120), NOW)).toBe('1h2m')
    expect(age(before(24 * 3600 + 3600), NOW)).toBe('1d1h')
    expect(age(before(365 * 24 * 3600 + 24 * 3600), NOW)).toBe('1y1d')
  })

  it('skips a zero-valued middle unit', () => {
    // 1 day and 5 seconds: the hour slot is empty, seconds still show.
    expect(age(before(24 * 3600 + 5), NOW)).toBe('1d5s')
  })
})

describe('duration', () => {
  it('renders a dash for non-strings and empty input', () => {
    expect(duration(undefined)).toBe('—')
    expect(duration(42)).toBe('—')
    expect(duration('')).toBe('—')
  })

  it('passes "running" through', () => {
    expect(duration('running')).toBe('running')
  })

  it('formats a completed start|end pair', () => {
    expect(duration('2026-01-02T12:00:00Z|2026-01-02T12:00:30Z')).toBe('30s')
    expect(duration('2026-01-02T12:00:00Z|2026-01-02T12:02:05Z')).toBe('2m5s')
    expect(duration('2026-01-02T12:00:00Z|2026-01-02T13:30:00Z')).toBe('1h30m')
  })

  it('renders a dash when either half fails to parse', () => {
    expect(duration('bogus|2026-01-02T12:00:30Z')).toBe('—')
    expect(duration('2026-01-02T12:00:00Z|bogus')).toBe('—')
  })
})

describe('cpu and memory', () => {
  it('shows millicores below one core, cores above', () => {
    expect(cpu(250)).toBe('250m')
    expect(cpu(1000)).toBe('1')
    expect(cpu(1500)).toBe('1.50')
  })

  it('shows MiB below a GiB, GiB above', () => {
    expect(memory(512)).toBe('512 MiB')
    expect(memory(512.4)).toBe('512 MiB')
    expect(memory(2048)).toBe('2.0 GiB')
  })
})

describe('toneFor', () => {
  it('maps well-known statuses onto their severity', () => {
    expect(toneFor('CrashLoopBackOff')).toBe('danger')
    expect(toneFor('Pending')).toBe('warn')
    expect(toneFor('Running')).toBe('ok')
    expect(toneFor('')).toBe('idle')
    expect(toneFor('SomethingCustom')).toBe('info')
  })

  it('treats any init-container phase as in-progress', () => {
    expect(toneFor('Init:1/3')).toBe('warn')
  })

  it('treats deliberate emptiness as idle, not broken', () => {
    expect(toneFor('Scaled to zero')).toBe('idle')
    expect(toneFor('Suspended')).toBe('idle')
  })
})

describe('ratioTone', () => {
  it('is healthy only when both halves agree', () => {
    expect(ratioTone('3/3')).toBe('ok')
    expect(ratioTone('2/3')).toBe('warn')
    expect(ratioTone('0/3')).toBe('danger')
  })

  it('is idle for zero-of-zero and unparseable input', () => {
    // A deployment scaled to zero is not unhealthy.
    expect(ratioTone('0/0')).toBe('idle')
    expect(ratioTone('n/a')).toBe('idle')
    expect(ratioTone('3')).toBe('idle')
  })
})

describe('kindToResource', () => {
  it('handles the irregular plurals', () => {
    expect(kindToResource('Endpoints')).toBe('endpoints')
    expect(kindToResource('Ingress')).toBe('ingresses')
    expect(kindToResource('NetworkPolicy')).toBe('networkpolicies')
    expect(kindToResource('StorageClass')).toBe('storageclasses')
  })

  it('pluralises regular kinds by rule', () => {
    expect(kindToResource('Pod')).toBe('pods')
    expect(kindToResource('Deployment')).toBe('deployments')
  })

  it('keeps the restartable kind and resource sets aligned', () => {
    // The resource-name set is derived; a drift here would let the list page
    // and the detail page disagree about what has a restart button.
    expect(RESTARTABLE_RESOURCES).toEqual(
      new Set([...RESTARTABLE_KINDS].map((k) => kindToResource(k))),
    )
  })
})

describe('splitApiVersion', () => {
  it('splits group/version and defaults the core group', () => {
    expect(splitApiVersion('apps/v1')).toEqual({ group: 'apps', version: 'v1' })
    expect(splitApiVersion('v1')).toEqual({ group: '', version: 'v1' })
    expect(splitApiVersion(undefined)).toEqual({ group: '', version: 'v1' })
  })
})

describe('navLabel', () => {
  it('prefers the short override when one exists', () => {
    expect(navLabel('PersistentVolumeClaim')).toBe('PVCs')
    expect(navLabel('CustomResourceDefinition')).toBe('CRDs')
    expect(navLabel('Endpoints')).toBe('Endpoints')
  })

  it('pluralises everything else for the sidebar', () => {
    expect(navLabel('Pod')).toBe('Pods')
    expect(navLabel('Ingress')).toBe('Ingresses')
    expect(navLabel('NetworkPolicy')).toBe('NetworkPolicies')
    expect(navLabel('')).toBe('')
  })
})
