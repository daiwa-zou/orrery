import { describe, expect, it } from 'vitest'
import { shouldAutoLogin } from './autologin'

const on = { oidcEnabled: true, autoLogin: true }

describe('shouldAutoLogin', () => {
  it('fires only when the server enables it', () => {
    expect(shouldAutoLogin(on, new URLSearchParams())).toBe(true)
    expect(shouldAutoLogin({ ...on, autoLogin: false }, new URLSearchParams())).toBe(false)
    expect(shouldAutoLogin({ ...on, oidcEnabled: false }, new URLSearchParams())).toBe(false)
    expect(shouldAutoLogin(undefined, new URLSearchParams())).toBe(false)
  })

  it('never redirects away from an error or a fresh sign-out', () => {
    expect(shouldAutoLogin(on, new URLSearchParams('error=access_denied'))).toBe(false)
    expect(shouldAutoLogin(on, new URLSearchParams('signedOut=1'))).toBe(false)
  })
})
