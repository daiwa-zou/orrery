import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import { Button, Spinner } from '../components/primitives'

/**
 * The login page is the only route that renders without a session, so it
 * fetches its own configuration rather than relying on any shared state.
 */
export function Login() {
  const [params] = useSearchParams()
  const [config, setConfig] = useState<{ oidcEnabled: boolean; loginPath: string }>()
  const [loadError, setLoadError] = useState<string>()

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

  return (
    <div className="flex h-full items-center justify-center p-6">
      <div className="w-full max-w-sm">
        <div className="mb-6 flex items-center gap-2.5">
          <svg viewBox="0 0 32 32" className="size-8" aria-hidden>
            <circle
              cx="16"
              cy="16"
              r="13"
              fill="none"
              stroke="currentColor"
              strokeWidth="3"
              className="text-accent"
            />
            <circle cx="16" cy="16" r="4" className="fill-accent" />
          </svg>
          <div>
            <h1 className="text-lg font-semibold tracking-tight text-ink">Orrery</h1>
            <p className="text-xs text-ink-faint">Multi-cluster Kubernetes console</p>
          </div>
        </div>

        {error && (
          <p className="mb-4 rounded-md bg-danger/10 px-3 py-2 text-sm text-danger ring-1 ring-danger/25">
            {error}
          </p>
        )}

        {loadError && (
          <p className="mb-4 rounded-md bg-danger/10 px-3 py-2 text-sm text-danger ring-1 ring-danger/25">
            Could not reach the server: {loadError}
          </p>
        )}

        {!config && !loadError && (
          <p className="flex items-center gap-2 text-sm text-ink-faint">
            <Spinner className="size-3.5" /> Checking sign-in options
          </p>
        )}

        {config?.oidcEnabled && (
          <div className="rounded-lg bg-surface p-4 ring-1 ring-border">
            <p className="mb-3 text-sm text-ink-muted">
              Sign in with your identity provider. Your cluster permissions come from your own
              account, not from this dashboard.
            </p>
            <Button variant="primary" onClick={signIn}>
              Continue with SSO
            </Button>
          </div>
        )}

        {config && !config.oidcEnabled && (
          <div className="rounded-lg bg-warn/10 p-4 ring-1 ring-warn/25">
            <p className="text-sm text-warn">
              Authentication is disabled on this server. Every visitor acts as the same identity.
            </p>
            <a href="/" className="mt-3 inline-block text-sm text-accent hover:underline">
              Continue anyway
            </a>
          </div>
        )}
      </div>
    </div>
  )
}
