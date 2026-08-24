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
import { ErrorBoundary } from './components/ErrorBoundary'
import { EmptyState, ErrorState, Spinner } from './components/primitives'
import { ToastProvider } from './components/Toast'
import { CreateResource } from './pages/CreateResource'
import { Events } from './pages/Events'
import { Fleet } from './pages/Fleet'
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

/**
 * "/" for a single-cluster deployment jumps straight in; with several
 * clusters it shows the fleet, because side-by-side health is the point of
 * running one console over many clusters.
 */
function Home() {
  const { data, isLoading, error, refetch } = useClusters()
  const navigate = useNavigate()

  const clusters = data?.clusters ?? []
  const only = clusters.length === 1 ? clusters[0] : undefined

  useEffect(() => {
    if (only) navigate(`/c/${only.name}`, { replace: true })
  }, [only, navigate])

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
  if (only) return null
  return <Fleet clusters={clusters} />
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

  return (
    <AppShell>
      {/* A banner, not a page swap: replacing the subtree on a single failed
          health probe would tear down open terminals, log streams and unsaved
          YAML edits exactly when the cluster is flaky. */}
      {entry && !entry.available && (
        <div className="border-b border-border bg-danger/10 px-4 py-2 text-sm text-danger">
          {entry.displayName} is unreachable —{' '}
          {entry.error ?? 'the server will keep retrying automatically'}. Live data resumes when
          it recovers.
        </div>
      )}
      <Routes>
        <Route index element={<Overview />} />
        <Route path="events" element={<Events />} />
        <Route path="r/:group/:version/:resource" element={<ResourceList />} />
        <Route path="r/:group/:version/:resource/create" element={<CreateResource />} />
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
        <ErrorBoundary>
          <Router>
          <Routes>
            <Route path="/login" element={<Login />} />
            <Route
              path="/"
              element={
                <RequireAuth>
                  <Home />
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
        </ErrorBoundary>
      </ToastProvider>
    </QueryClientProvider>
  )
}
