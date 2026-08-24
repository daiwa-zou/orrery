import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient, keepPreviousData } from '@tanstack/react-query'
import { api, wsURL, groupSegment, type ListParams, type ResourceRef } from './client'
import type { ListResponse, Row, WatchMessage } from './types'

export function useMe() {
  return useQuery({
    queryKey: ['me'],
    queryFn: ({ signal }) => api.me(signal),
    retry: false,
    staleTime: 60_000,
  })
}

export function useClusters() {
  return useQuery({
    queryKey: ['clusters'],
    queryFn: ({ signal }) => api.clusters(signal),
    // Health is probed server-side every 30s; matching that keeps the switcher
    // current without polling harder than the data changes.
    refetchInterval: 30_000,
  })
}

export function useDiscovery(cluster?: string) {
  return useQuery({
    queryKey: ['discovery', cluster],
    queryFn: ({ signal }) => api.discovery(cluster!, signal),
    enabled: !!cluster,
    staleTime: 5 * 60_000,
  })
}

export function useOverview(cluster?: string) {
  return useQuery({
    queryKey: ['overview', cluster],
    queryFn: ({ signal }) => api.overview(cluster!, signal),
    enabled: !!cluster,
    refetchInterval: 15_000,
  })
}

export function useNamespaces(cluster?: string) {
  const query = useQuery({
    queryKey: ['namespaces', cluster],
    queryFn: ({ signal }) =>
      api.list(
        { cluster: cluster!, group: '', version: 'v1', resource: 'namespaces' },
        { pageSize: 1000, sort: 'name' },
        signal,
      ),
    enabled: !!cluster,
    staleTime: 60_000,
  })
  const names = useMemo(() => (query.data?.items ?? []).map((r) => r.name), [query.data])
  return { ...query, names }
}

export function usePodMetrics(cluster?: string, namespace?: string) {
  return useQuery({
    queryKey: ['metrics', 'pods', cluster, namespace ?? ''],
    queryFn: ({ signal }) => api.podMetrics(cluster!, namespace || undefined, signal),
    enabled: !!cluster,
    refetchInterval: 30_000,
  })
}

export function useNodeMetrics(cluster?: string) {
  return useQuery({
    queryKey: ['metrics', 'nodes', cluster],
    queryFn: ({ signal }) => api.nodeMetrics(cluster!, signal),
    enabled: !!cluster,
    refetchInterval: 30_000,
  })
}

export interface LiveListState {
  data?: ListResponse
  isLoading: boolean
  error: unknown
  /** Live connection state, surfaced so the UI never lies about freshness. */
  live: 'connecting' | 'live' | 'polling' | 'off'
  refetch: () => void
}

/**
 * useLiveList pages a resource over REST and keeps the visible page fresh from
 * the cluster's shared watch.
 *
 * Modifications are applied straight to the rows on screen, so a pod flipping
 * to CrashLoopBackOff updates instantly. Additions and deletions change what
 * belongs on the page, so those trigger a debounced refetch instead of being
 * spliced in locally — that keeps pagination and sorting honest rather than
 * approximately right.
 */
export function useLiveList(ref: ResourceRef | null, params: ListParams, enabled = true): LiveListState {
  const qc = useQueryClient()
  const [live, setLive] = useState<LiveListState['live']>('off')

  const key = ref
    ? ['list', ref.cluster, ref.group, ref.version, ref.resource, params]
    : ['list', 'none']

  const query = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => api.list(ref!, params, signal),
    enabled: !!ref && enabled,
    placeholderData: keepPreviousData,
    // The watch is the freshness mechanism; this is a safety net for a socket
    // that silently dies behind a proxy.
    refetchInterval: live === 'live' ? false : 15_000,
  })

  const refetchTimer = useRef<number | undefined>(undefined)
  const keyRef = useRef(key)
  keyRef.current = key

  useEffect(() => {
    if (!ref || !enabled) {
      setLive('off')
      return
    }

    let closed = false
    let socket: WebSocket | undefined
    let retryTimer: number | undefined
    let attempts = 0
    let dropped = false
    setLive('connecting')

    const url = wsURL(
      `/clusters/${ref.cluster}/ws/watch/${groupSegment(ref.group)}/${ref.version}/${ref.resource}`,
      { namespace: params.namespace },
    )

    const scheduleRefetch = () => {
      window.clearTimeout(refetchTimer.current)
      refetchTimer.current = window.setTimeout(() => {
        qc.invalidateQueries({ queryKey: keyRef.current })
      }, 700)
    }

    // A dropped socket (proxy idle timeout, rolling restart of the backend)
    // should heal itself. Polling covers the gap, so the backoff can be lazy.
    const scheduleReconnect = () => {
      if (closed) return
      dropped = true
      setLive('polling')
      const delay = Math.min(30_000, 1_000 * 2 ** attempts)
      attempts += 1
      window.clearTimeout(retryTimer)
      retryTimer = window.setTimeout(connect, delay)
    }

    const connect = () => {
      if (closed) return
      socket = new WebSocket(url)

      const openedAt = { t: 0 }

      socket.onopen = () => {
        if (closed) return
        openedAt.t = performance.now()
        setLive('live')
        if (dropped) {
          // Anything could have changed while the socket was down.
          dropped = false
          qc.invalidateQueries({ queryKey: keyRef.current })
        }
      }

      socket.onmessage = (event) => {
        let msg: WatchMessage
        try {
          msg = JSON.parse(event.data)
        } catch {
          return
        }

        switch (msg.type) {
          case 'MODIFIED': {
            const incoming = msg.item
            qc.setQueryData<ListResponse>(keyRef.current, (prev) => {
              if (!prev?.items) return prev
              let touched = false
              const items = prev.items.map((row: Row) => {
                if (row.uid !== incoming.uid) return row
                touched = true
                return { ...row, ...incoming }
              })
              return touched ? { ...prev, items } : prev
            })
            break
          }
          case 'ADDED':
          case 'DELETED':
            scheduleRefetch()
            break
          case 'OVERFLOW':
            // We fell behind the cluster; the only honest recovery is a reload.
            setLive('polling')
            qc.invalidateQueries({ queryKey: keyRef.current })
            break
          case 'ERROR':
            setLive('polling')
            break
        }
      }

      socket.onerror = () => {}

      socket.onclose = () => {
        // Only a connection that stayed up counts as recovery. Resetting the
        // backoff on open alone turns a server that accepts the upgrade and
        // immediately closes (e.g. watch forbidden) into a once-a-second
        // reconnect-and-refetch storm.
        if (openedAt.t > 0 && performance.now() - openedAt.t > 10_000) attempts = 0
        scheduleReconnect()
      }
    }

    connect()

    return () => {
      closed = true
      window.clearTimeout(refetchTimer.current)
      window.clearTimeout(retryTimer)
      if (socket) {
        // Detach before closing so the close event does not schedule a retry.
        socket.onclose = null
        socket.close()
      }
    }
    // params.namespace is the only param the watch itself depends on; the rest
    // are applied server-side to the REST page.
  }, [ref?.cluster, ref?.group, ref?.version, ref?.resource, params.namespace, enabled, qc])

  return {
    data: query.data,
    isLoading: query.isLoading,
    error: query.error,
    live,
    refetch: query.refetch,
  }
}

export function useResource(ref: ResourceRef | null) {
  return useQuery({
    queryKey: ref
      ? ['object', ref.cluster, ref.group, ref.version, ref.resource, ref.namespace, ref.name]
      : ['object', 'none'],
    queryFn: ({ signal }) => api.get(ref!, signal),
    enabled: !!ref?.name,
    refetchInterval: 10_000,
  })
}

export function useEvents(
  cluster: string | undefined,
  filter: {
    namespace?: string
    involvedName?: string
    involvedKind?: string
    involvedUID?: string
    warningsOnly?: boolean
    limit?: number
  },
) {
  return useQuery({
    queryKey: ['events', cluster, filter],
    queryFn: ({ signal }) => api.events(cluster!, { limit: 100, ...filter }, signal),
    enabled: !!cluster,
    refetchInterval: 15_000,
  })
}

/** Batch-checks permissions so the UI only offers actions that will succeed. */
/**
 * Batch-checks list access for a set of resources in the current scope, so
 * the navigation can show what this identity can actually open. Answers come
 * back keyed by "group/resource"; absent means still loading.
 */
export function useListAccess(
  cluster: string | undefined,
  namespace: string,
  items: { group: string; version: string; resource: string }[],
): Map<string, boolean> | undefined {
  const key = items.map((i) => `${i.group}/${i.resource}`).join(',')
  const query = useQuery({
    queryKey: ['nav-access', cluster, namespace, key],
    queryFn: async ({ signal }) => {
      const checks = items.map((i) => ({
        verb: 'list',
        group: i.group,
        version: i.version,
        resource: i.resource,
        namespace: namespace || undefined,
      }))
      const out = new Map<string, boolean>()
      // The server answers at most 64 questions per request.
      for (let i = 0; i < checks.length; i += 64) {
        const slice = checks.slice(i, i + 64)
        const decisions = await api.access(cluster!, slice, signal)
        slice.forEach((c, j) => out.set(`${c.group}/${c.resource}`, decisions[j]?.allowed ?? false))
      }
      return out
    },
    enabled: !!cluster && items.length > 0,
    staleTime: 30_000,
  })
  return query.data
}

export function useAccess(
  cluster: string | undefined,
  checks: Parameters<typeof api.access>[1],
) {
  return useQuery({
    queryKey: ['access', cluster, checks],
    queryFn: ({ signal }) => api.access(cluster!, checks, signal),
    enabled: !!cluster && checks.length > 0,
    staleTime: 30_000,
  })
}
