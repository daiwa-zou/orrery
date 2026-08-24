import { useEffect } from 'react'
import {
  Navigate,
  Route,
  BrowserRouter as Router,
  Routes,
  useNavigate,
  useParams,
} from 'react-router-dom'
import { QueryCache, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ApiError } from './api/client'
import { useClusters, useMe } from './api/hooks'
import { AppShell } from './components/AppShell'
import { EmptyState, ErrorState, Spinner } from './components/primitives'
import { ToastProvider } from './components/Toast'
import { Login } from './pages/Login'
import { Overview } from './pages/Overview'
import { ResourceDetail } from './pages/ResourceDetail'
import { ResourceList } from './pages/ResourceList'

/**
 * A 401 anywhere means the session is gone. Handling it once at the query
 * cache level beats sprinkling redirects through every hook.
 */
const queryClient = new QueryClient({
  queryCache: new QueryCache({
    onError: (error) => {
      if (error instanceof ApiError && error.isAuth && window.location.pathname !== '/login') {
        const returnTo = encodeURIComponent(window.location.pathname + window.location.search)
        window.location.href = `/login?returnTo=${returnTo}`
      }
    },
  }),
  defaultOptions: {
    queries: {
      retry: (count, error) => {
        // Retrying an authorization failure just produces the same answer.
        if (error instanceof ApiError && (error.isAuth || error.isForbidden)) return false
        return count < 2
      },
      staleTime: 5_000,
      refetchOnWindowFocus: true,
    },
  },
})

/** Redirects to the first available cluster, so "/" is never a dead end. */
function ClusterRedirect() {
  const { data, isLoading, error, refetch } = useClusters()
  const navigate = useNavigate()

  const clusters = data?.clusters ?? []
  const target = clusters.find((c) => c.available) ?? clusters[0]

  useEffect(() => {
    if (target) navigate(`/c/${target.name}`, { replace: true })
  }, [target, navigate])

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center gap-2 text-ink-faint">
        <Spinner /> Loading clusters
      </div>
    )
  }
  if (error) return <ErrorState error={error} retry={refetch} />
  if (clusters.length === 0) {
    return (
      <EmptyState
        title="No clusters configured"
        description="Add a cluster to the server's configuration file and restart it."
      />
    )
  }
  return null
}

/** Ensures a session exists before rendering anything that needs one. */
function RequireAuth({ children }: { children: React.ReactNode }) {
  const { data, isLoading, error } = useMe()

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center gap-2 text-ink-faint">
        <Spinner /> Signing in
      </div>
    )
  }
  if (error instanceof ApiError && error.isAuth) {
    return <Navigate to="/login" replace />
  }
  if (error) return <ErrorState error={error} />
  if (!data?.authenticated) return <Navigate to="/login" replace />

  return <>{children}</>
}

/** Guards against a cluster name in the URL that no longer exists. */
function ClusterRoutes() {
  const { cluster } = useParams<{ cluster: string }>()
  const { data, isLoading } = useClusters()

  const entry = data?.clusters.find((c) => c.name === cluster)

  if (!isLoading && data && !entry) {
    return (
      <AppShell>
        <EmptyState
          title={`Cluster "${cluster}" is not registered`}
          description="It may have been removed from the server's configuration."
        />
      </AppShell>
    )
  }

  if (entry && !entry.available) {
    return (
      <AppShell>
        <EmptyState
          title={`${entry.displayName} is unreachable`}
          description={
            entry.error ??
            'The server cannot currently connect to this cluster. It will retry automatically.'
          }
        />
      </AppShell>
    )
  }

  return (
    <AppShell>
      <Routes>
        <Route index element={<Overview />} />
        <Route path="r/:group/:version/:resource" element={<ResourceList />} />
        <Route
          path="r/:group/:version/:resource/:namespace/:name"
          element={<ResourceDetail />}
        />
        <Route path="*" element={<EmptyState title="Page not found" />} />
      </Routes>
    </AppShell>
  )
}

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <Router>
          <Routes>
            <Route path="/login" element={<Login />} />
            <Route
              path="/"
              element={
                <RequireAuth>
                  <ClusterRedirect />
                </RequireAuth>
              }
            />
            <Route
              path="/c/:cluster/*"
              element={
                <RequireAuth>
                  <ClusterRoutes />
                </RequireAuth>
              }
            />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Router>
      </ToastProvider>
    </QueryClientProvider>
  )
}
