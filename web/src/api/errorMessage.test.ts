import { afterEach, describe, expect, it, vi } from 'vitest'

import { api, ApiError } from './client'

/**
 * What a failure says when the server did not say anything.
 *
 * Most errors carry a `reason` from the API and that is what gets shown. The
 * fallback used to be `${status} ${statusText}`, and browsers synthesise
 * statusText for codes they do not recognise — so Orrery's own 499 arrived as
 * "status code 499" and the fallback read "499 status code 499". Harmless
 * while the console rendered the word "unavailable" instead of the message,
 * and not harmless once it started showing the message to people.
 */

function respondWith(status: number, body?: unknown, statusText?: string) {
  const init: ResponseInit = { status, statusText }
  const res = body === undefined
    ? new Response(null, init)
    : new Response(JSON.stringify(body), {
        ...init,
        headers: { 'Content-Type': 'application/json' },
      })
  vi.stubGlobal('fetch', vi.fn(async () => res))
}

async function failureFrom(call: () => Promise<unknown>): Promise<ApiError> {
  try {
    await call()
  } catch (err) {
    if (err instanceof ApiError) return err
    throw err
  }
  throw new Error('the call succeeded; expected it to fail')
}

afterEach(() => vi.unstubAllGlobals())

describe('request error messages', () => {
  it('prefers the reason the server sent', async () => {
    respondWith(403, { error: 'forbidden', reason: 'you are not allowed to list pods', code: 403 })
    const err = await failureFrom(() => api.cacheStats('lens-a'))
    expect(err.message).toBe('you are not allowed to list pods')
    expect(err.kind).toBe('forbidden')
    expect(err.isForbidden).toBe(true)
  })

  // The one that was on screen. 499 is Orrery's own "the client hung up", and
  // no browser has a name for it, so statusText is whatever it invents.
  it('says something readable for a cancelled request', async () => {
    respondWith(499, { error: 'client_closed_request', code: 499 }, 'status code 499')
    const err = await failureFrom(() => api.cacheStats('lens-a'))
    expect(err.message).not.toContain('499 status code 499')
    expect(err.message).toContain('interrupted')
    expect(err.status).toBe(499)
  })

  it('names the status plainly when the server sends no reason', async () => {
    respondWith(503, { error: 'unavailable', code: 503 }, 'status code 503')
    const err = await failureFrom(() => api.cacheStats('lens-a'))
    expect(err.message).not.toContain('status code 503')
    expect(err.message).toContain('503')
    expect(err.kind).toBe('unavailable')
  })

  it('survives a body that is not JSON at all', async () => {
    // A proxy returning an HTML error page must not crash the parse.
    vi.stubGlobal('fetch', vi.fn(async () => new Response('<html>502</html>', {
      status: 502, headers: { 'Content-Type': 'text/html' },
    })))
    const err = await failureFrom(() => api.cacheStats('lens-a'))
    expect(err.status).toBe(502)
    expect(err.message).toContain('502')
  })
})
