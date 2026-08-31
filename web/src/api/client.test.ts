import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api, apiGroup, groupSegment, proxyURL, wsURL } from './client'

/**
 * The suite runs in a node environment, so `fetch`, `document` and `window`
 * are stubbed with just the pieces client.ts touches.
 *
 * `calls` records what each request was actually sent as, which is the only
 * way to see the parts of a request the caller never names: the CSRF header,
 * the repeated query parameters, and the "_" that stands in for a
 * cluster-scoped object's namespace.
 */
type Call = { url: string; init: RequestInit }

let calls: Call[]
let reply: (call: Call) => Response

function stubFetch() {
  calls = []
  reply = () => new Response(null, { status: 204 })
  vi.stubGlobal('fetch', (url: string, init: RequestInit = {}) => {
    const call = { url, init }
    calls.push(call)
    return Promise.resolve(reply(call))
  })
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function stubLocation(loc: { protocol?: string; host?: string; pathname?: string; search?: string }) {
  const location = {
    protocol: 'http:',
    host: 'localhost:5173',
    pathname: '/',
    search: '',
    href: '',
    ...loc,
  }
  vi.stubGlobal('window', { location })
  return location
}

function stubCookie(cookie: string) {
  vi.stubGlobal('document', { cookie })
}

beforeEach(() => {
  stubFetch()
  stubLocation({})
  stubCookie('')
})

afterEach(() => {
  vi.unstubAllGlobals()
})

/** The single call the last request made, for tests that make exactly one. */
function only(): Call {
  expect(calls).toHaveLength(1)
  return calls[0]
}

function header(call: Call, name: string): string | null {
  return new Headers(call.init.headers).get(name)
}

describe('ApiError', () => {
  it('carries the status and the server’s reason', () => {
    const err = new ApiError(403, 'forbidden', 'you may not list pods')
    expect(err.status).toBe(403)
    expect(err.kind).toBe('forbidden')
    expect(err.message).toBe('you may not list pods')
    expect(err.name).toBe('ApiError')
    expect(err instanceof Error).toBe(true)
  })

  it('names the two statuses the UI routes on', () => {
    expect(new ApiError(401, 'unauthenticated', '').isAuth).toBe(true)
    expect(new ApiError(403, 'forbidden', '').isForbidden).toBe(true)
    // A 403 is not a signed-out session, and routing it to the login page
    // would sign a user out for asking about something they may not see.
    expect(new ApiError(403, 'forbidden', '').isAuth).toBe(false)
    expect(new ApiError(401, 'unauthenticated', '').isForbidden).toBe(false)
    expect(new ApiError(500, 'internal', '').isAuth).toBe(false)
  })
})

describe('group encoding', () => {
  it('round-trips the core group through its URL segment', () => {
    expect(groupSegment('')).toBe('core')
    expect(apiGroup('core')).toBe('')
    expect(groupSegment('apps')).toBe('apps')
    expect(apiGroup('apps')).toBe('apps')
    for (const group of ['', 'apps', 'networking.k8s.io']) {
      expect(apiGroup(groupSegment(group))).toBe(group)
    }
  })
})

describe('request', () => {
  it('reads a JSON body', async () => {
    reply = () => json({ clusters: [{ name: 'prod' }] })
    await expect(api.clusters()).resolves.toEqual({ clusters: [{ name: 'prod' }] })
    expect(only().url).toBe('/api/v1/clusters')
  })

  it('reads a non-JSON body as text', async () => {
    reply = () =>
      new Response('kind: Pod\n', { status: 200, headers: { 'Content-Type': 'application/yaml' } })
    const yaml = await api.getYaml({
      cluster: 'c',
      group: '',
      version: 'v1',
      resource: 'pods',
      namespace: 'ns',
      name: 'p',
    })
    expect(yaml).toBe('kind: Pod\n')
  })

  it('returns undefined for 204 rather than trying to parse nothing', async () => {
    reply = () => new Response(null, { status: 204 })
    await expect(api.logout()).resolves.toBeUndefined()
  })

  it('sends cookies so the session travels with the request', async () => {
    reply = () => json({})
    await api.clusters()
    expect(only().init.credentials).toBe('same-origin')
  })

  it('passes an abort signal through to fetch', async () => {
    const controller = new AbortController()
    reply = () => json({})
    await api.clusters(controller.signal)
    expect(only().init.signal).toBe(controller.signal)
  })
})

describe('CSRF', () => {
  it('double-submits the cookie on a write', async () => {
    stubCookie('other=1; orrery_csrf=tok%2Fen; another=2')
    reply = () => json({ scaled: true })
    await api.scale('c', { group: 'apps', version: 'v1', resource: 'deployments', namespace: 'ns', name: 'd' }, 3)

    const call = only()
    expect(call.init.method).toBe('POST')
    // Decoded: the cookie is percent-encoded and the header is not.
    expect(header(call, 'X-CSRF-Token')).toBe('tok/en')
  })

  it('does not send the header on a read', async () => {
    stubCookie('orrery_csrf=tok')
    reply = () => json({})
    await api.clusters()
    expect(header(only(), 'X-CSRF-Token')).toBeNull()
  })

  it('sends an empty token rather than omitting it when there is no cookie', async () => {
    stubCookie('unrelated=1')
    reply = () => json({ evicted: true })
    await api.evict('c', 'ns', 'p')
    // The server answers 403 to an empty token and nothing at all to a
    // missing header; the first is a message the user can act on.
    expect(header(only(), 'X-CSRF-Token')).toBe('')
  })

  it('defaults a write to JSON but leaves a declared content type alone', async () => {
    reply = () => json({})
    await api.evict('c', 'ns', 'p')
    expect(header(only(), 'Content-Type')).toBe('application/json')

    calls = []
    await api.replace(
      { cluster: 'c', group: '', version: 'v1', resource: 'pods', namespace: 'ns', name: 'p' },
      'kind: Pod',
    )
    expect(header(only(), 'Content-Type')).toBe('application/yaml')
  })
})

describe('error responses', () => {
  it('throws the server’s own reason', async () => {
    reply = () => json({ error: 'forbidden', reason: 'pods is forbidden in ns' }, 403)
    await expect(api.clusters()).rejects.toMatchObject({
      status: 403,
      kind: 'forbidden',
      message: 'pods is forbidden in ns',
    })
  })

  it('falls back to the status when the body says nothing', async () => {
    reply = () => new Response('<html>502</html>', { status: 502, headers: { 'Content-Type': 'text/html' } })
    await expect(api.clusters()).rejects.toMatchObject({
      status: 502,
      kind: 'error',
      message: 'The server returned HTTP 502.',
    })
  })

  it('falls back when the body claims JSON and is not', async () => {
    reply = () => new Response('not json', { status: 500, headers: { 'Content-Type': 'application/json' } })
    await expect(api.clusters()).rejects.toMatchObject({
      status: 500,
      message: 'The server returned HTTP 500.',
    })
  })

  // Browsers synthesise statusText for codes they do not know, so a 499
  // arrived as "499 status code 499". A cancelled request is also not a
  // failure of the server, and the sentence has to say so.
  it('explains a 499 in words rather than by its number', async () => {
    reply = () => new Response(null, { status: 499 })
    await expect(api.clusters()).rejects.toMatchObject({
      status: 499,
      message: 'The request was interrupted before it finished.',
    })
  })

  it('routes a 401 on a write to sign-in, carrying where to come back to', async () => {
    const location = stubLocation({ pathname: '/c/prod/pods', search: '?q=web' })
    reply = () => json({ error: 'unauthenticated' }, 401)

    await expect(api.evict('c', 'ns', 'p')).rejects.toBeInstanceOf(ApiError)

    expect(location.href).toBe('/login?returnTo=%2Fc%2Fprod%2Fpods%3Fq%3Dweb')
  })

  it('does not redirect a 401 that arrives on the login page', async () => {
    const location = stubLocation({ pathname: '/login' })
    reply = () => json({ error: 'unauthenticated' }, 401)

    await expect(api.evict('c', 'ns', 'p')).rejects.toBeInstanceOf(ApiError)

    expect(location.href).toBe('')
  })

  // A read's 401 is handled by the query layer, which knows whether the view
  // it belongs to is still on screen.
  it('does not redirect a 401 on a read', async () => {
    const location = stubLocation({ pathname: '/c/prod/pods' })
    reply = () => json({ error: 'unauthenticated' }, 401)

    await expect(api.clusters()).rejects.toBeInstanceOf(ApiError)

    expect(location.href).toBe('')
  })
})

describe('query strings', () => {
  const ref = { cluster: 'prod', group: 'apps', version: 'v1', resource: 'deployments' }

  it('repeats a key per array entry rather than joining', async () => {
    reply = () => json({ items: [] })
    await api.list(ref, { namespace: ['a', 'b'], where: ['replicas>2', 'age<1h'] })

    const url = new URL(only().url, 'http://x')
    expect(url.searchParams.getAll('namespace')).toEqual(['a', 'b'])
    expect(url.searchParams.getAll('where')).toEqual(['replicas>2', 'age<1h'])
  })

  it('drops absent and empty values instead of sending them empty', async () => {
    reply = () => json({ items: [] })
    await api.list(ref, { q: '', labelSelector: undefined, namespace: [], sort: 'name' })

    const url = new URL(only().url, 'http://x')
    // An empty q is not a filter; sending `q=` asks the server to match the
    // empty string against every row.
    expect(url.searchParams.has('q')).toBe(false)
    expect(url.searchParams.has('labelSelector')).toBe(false)
    expect(url.searchParams.has('namespace')).toBe(false)
    expect(url.searchParams.get('sort')).toBe('name')
  })

  it('keeps a zero, which is a value and not an absence', async () => {
    reply = () => json({ items: [] })
    await api.list(ref, { page: 0, pageSize: 0 })

    const url = new URL(only().url, 'http://x')
    expect(url.searchParams.get('page')).toBe('0')
    expect(url.searchParams.get('pageSize')).toBe('0')
  })

  it('drops empty entries inside an array', async () => {
    reply = () => json({ items: [] })
    await api.list(ref, { namespace: ['a', '', 'b'] })

    const url = new URL(only().url, 'http://x')
    expect(url.searchParams.getAll('namespace')).toEqual(['a', 'b'])
  })

  it('omits the question mark when nothing survives', async () => {
    reply = () => json({ items: [] })
    await api.list(ref, { q: '' })
    expect(only().url).toBe('/api/v1/clusters/prod/resources/apps/v1/deployments')
  })
})

describe('path building', () => {
  it('stands "_" in for a cluster-scoped object’s namespace', async () => {
    reply = () => json({})
    await api.get({ cluster: 'prod', group: '', version: 'v1', resource: 'nodes', name: 'node-1' })
    expect(only().url).toBe('/api/v1/clusters/prod/resources/core/v1/nodes/_/node-1')

    calls = []
    await api.get({
      cluster: 'prod',
      group: '',
      version: 'v1',
      resource: 'pods',
      namespace: 'ns',
      name: 'p',
    })
    expect(only().url).toBe('/api/v1/clusters/prod/resources/core/v1/pods/ns/p')
  })

  it('treats an empty namespace as cluster-scoped', async () => {
    reply = () => json({})
    await api.get({ cluster: 'prod', group: '', version: 'v1', resource: 'nodes', namespace: '', name: 'n' })
    expect(only().url).toBe('/api/v1/clusters/prod/resources/core/v1/nodes/_/n')
  })

  it('builds a proxy URL with the port after a colon', () => {
    expect(proxyURL('prod', 'ns', 'services', 'web', 8080)).toBe(
      '/api/v1/clusters/prod/proxy/ns/services/web:8080/',
    )
    expect(proxyURL('prod', 'ns', 'pods', 'p', 'http')).toBe(
      '/api/v1/clusters/prod/proxy/ns/pods/p:http/',
    )
  })
})

describe('wsURL', () => {
  it('follows the page’s scheme', () => {
    stubLocation({ protocol: 'http:', host: 'localhost:5173' })
    expect(wsURL('/clusters/prod/ws/watch')).toBe('ws://localhost:5173/api/v1/clusters/prod/ws/watch')

    stubLocation({ protocol: 'https:', host: 'orrery.example' })
    expect(wsURL('/clusters/prod/ws/watch')).toBe('wss://orrery.example/api/v1/clusters/prod/ws/watch')
  })

  // The Origin check runs against publicURL and corsOrigins, so a socket has
  // to be opened against the host the page came from — including its port.
  it('keeps the page’s port', () => {
    stubLocation({ protocol: 'http:', host: 'localhost:5178' })
    expect(wsURL('/x')).toBe('ws://localhost:5178/api/v1/x')
  })

  it('takes the same query encoding as the REST calls', () => {
    stubLocation({ protocol: 'http:', host: 'h' })
    expect(wsURL('/x', { namespace: ['a', 'b'], q: '', follow: true })).toBe(
      'ws://h/api/v1/x?namespace=a&namespace=b&follow=true',
    )
  })
})

describe('access', () => {
  const check = { verb: 'list', group: 'apps', version: 'v1', resource: 'deployments' }

  it('answers an empty batch without asking the server', async () => {
    await expect(api.access('prod', [])).resolves.toEqual([])
    expect(calls).toHaveLength(0)
  })

  it('returns the decisions in the order the checks were asked', async () => {
    reply = () =>
      json({
        results: {
          '0': { allowed: true },
          '1': { allowed: false, denied: true, reason: 'no' },
        },
      })

    await expect(api.access('prod', [check, { ...check, verb: 'delete' }])).resolves.toEqual([
      { allowed: true },
      { allowed: false, denied: true, reason: 'no' },
    ])
    expect(only().init.method).toBe('POST')
  })

  // The server answers every index, so a gap is a broken contract and not a
  // refusal. Reporting it as `{ allowed: false }` walked straight past the
  // `!d.unavailable` check its callers make and greyed out an action the user
  // very likely holds — the "you may not" / "we could not ask" confusion this
  // codebase keeps having to undo.
  it('reports a missing decision as unanswered, not as denied', async () => {
    reply = () => json({ results: { '0': { allowed: true } } })

    const [first, second] = await api.access('prod', [check, { ...check, verb: 'delete' }])

    expect(first).toEqual({ allowed: true })
    expect(second.allowed).toBe(false)
    expect(second.unavailable).toBe(true)
    expect(second.denied).toBeUndefined()
    expect(second.reason).toBeTruthy()
  })

  it('reports every decision as unanswered when the results are empty', async () => {
    reply = () => json({ results: {} })
    const out = await api.access('prod', [check])
    expect(out).toHaveLength(1)
    expect(out[0].unavailable).toBe(true)
  })
})
