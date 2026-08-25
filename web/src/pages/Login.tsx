import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import { LogoMark } from '../components/Logo'
import { Button, Corners, Spinner } from '../components/primitives'
import { shouldAutoLogin } from '../lib/autologin'

/**
 * The login page is the only route that renders without a session, so it
 * fetches its own configuration rather than relying on any shared state.
 */
export function Login() {
  const [params] = useSearchParams()
  const [config, setConfig] = useState<Awaited<ReturnType<typeof api.authConfig>>>()
  const [loadError, setLoadError] = useState<string>()
  const [redirecting, setRedirecting] = useState(false)

  const error = params.get('error')
  const returnTo = params.get('returnTo') ?? '/'

  useEffect(() => {
    api
      .authConfig()
      .then(setConfig)
      .catch((e: Error) => setLoadError(e.message))
  }, [])

  const signIn = () => {
    if (!config) return
    window.location.href = `${config.loginPath}?returnTo=${encodeURIComponent(returnTo)}`
  }

  // With autoLogin the button is a formality; skip it. Errors and fresh
  // sign-outs still render the page (see shouldAutoLogin).
  useEffect(() => {
    if (config && shouldAutoLogin(config, params)) {
      setRedirecting(true)
      window.location.href = `${config.loginPath}?returnTo=${encodeURIComponent(returnTo)}`
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- fires once, when config arrives
  }, [config])

  return (
    <div className="flex h-full items-center justify-center p-6">
      <div className="w-full max-w-[380px]">
        <div className="mb-[26px] flex items-center gap-3">
          <LogoMark large className="size-10" />
          <div>
            <h1 className="font-condensed text-2xl font-semibold tracking-[.06em] text-ink">
              ORRERY
            </h1>
            <p className="text-[11.5px] text-ink-faint">Multi-cluster Kubernetes console</p>
          </div>
        </div>

        {error && (
          <p className="mb-4 bg-danger/10 px-3 py-2 text-[13px] text-danger ring-1 ring-danger/25">
            {error}
          </p>
        )}

        {loadError && (
          <p className="mb-4 bg-danger/10 px-3 py-2 text-[13px] text-danger ring-1 ring-danger/25">
            Could not reach the server: {loadError}
          </p>
        )}

        {!config && !loadError && (
          <p className="flex items-center gap-2 text-[13px] text-ink-faint">
            <Spinner className="size-3.5" /> Checking sign-in options
          </p>
        )}

        {redirecting && (
          <p className="flex items-center gap-2 text-[13px] text-ink-faint">
            <Spinner className="size-3.5" /> Redirecting to your identity provider
          </p>
        )}

        {config?.oidcEnabled && !redirecting && (
          <div className="blueprint bg-surface p-[18px]">
            <Corners />
            <p className="mb-3.5 text-[13.5px] leading-relaxed text-ink-muted">
              Sign in with your identity provider. Your cluster permissions come from your own
              account, not from this dashboard.
            </p>
            <Button variant="primary" onClick={signIn} className="w-full py-2.5">
              Continue with SSO
            </Button>
          </div>
        )}

        {config && !config.oidcEnabled && (
          <div className="blueprint bg-warn/10 p-[18px] ring-warn/25">
            <Corners />
            <p className="text-[13px] text-warn">
              Authentication is disabled on this server. Every visitor acts as the same identity.
            </p>
            <a
              href="/"
              className="mt-3 inline-block text-[13px] text-accent-text hover:text-accent-text-hover hover:underline"
            >
              Continue anyway
            </a>
          </div>
        )}
      </div>
    </div>
  )
}
