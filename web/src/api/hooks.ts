import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient, keepPreviousData } from '@tanstack/react-query'
import { api, wsURL, groupSegment, type ListParams, type ResourceRef } from './client'
import type { ListResponse, Row, WatchMessage } from './types'

export function useMe() {
  return useQuery({
    queryKey: ['me'],
    queryFn: api.me,
    retry: false,
    staleTime: 60_000,
  })
}

export function useClusters() {
  return useQuery({
    queryKey: ['clusters'],
    queryFn: api.clusters,
    // Health is probed server-side every 30s; matching that keeps the switcher
    // current without polling harder than the data changes.
    refetchInterval: 30_000,
  })
}

export function useDiscovery(cluster?: string) {
  return useQuery({
    queryKey: ['discovery', cluster],
    queryFn: () => api.discovery(cluster!),
    enabled: !!cluster,
    staleTime: 5 * 60_000,
  })
}

export function useOverview(cluster?: string) {
  return useQuery({
    queryKey: ['overview', cluster],
    queryFn: () => api.overview(cluster!),
    enabled: !!cluster,
    refetchInterval: 15_000,
  })
}

export function useNamespaces(cluster?: string) {
  const query = useQuery({
    queryKey: ['namespaces', cluster],
    queryFn: () =>
      api.list(
        { cluster: cluster!, group: '', version: 'v1', resource: 'namespaces' },
        { pageSize: 1000, sort: 'name' },
      ),
    enabled: !!cluster,
    staleTime: 60_000,
  })
  const names = useMemo(() => (query.data?.items ?? []).map((r) => r.name), [query.data])
  return { ...query, names }
}

export function useNodeMetrics(cluster?: string) {
  return useQuery({
    queryKey: ['metrics', 'nodes', cluster],
    queryFn: () => api.nodeMetrics(cluster!),
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
    queryFn: () => api.list(ref!, params),
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
    setLive('connecting')

    const url = wsURL(
      `/clusters/${ref.cluster}/ws/watch/${groupSegment(ref.group)}/${ref.version}/${ref.resource}`,
      { namespace: params.namespace },
    )
    const socket = new WebSocket(url)

    const scheduleRefetch = () => {
      window.clearTimeout(refetchTimer.current)
      refetchTimer.current = window.setTimeout(() => {
        qc.invalidateQueries({ queryKey: keyRef.current })
      }, 700)
    }

    socket.onopen = () => {
      if (!closed) setLive('live')
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

    socket.onerror = () => {
      if (!closed) setLive('polling')
    }

    socket.onclose = () => {
      if (!closed) setLive('polling')
    }

    return () => {
      closed = true
      window.clearTimeout(refetchTimer.current)
      socket.close()
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
    queryFn: () => api.get(ref!),
    enabled: !!ref?.name,
    refetchInterval: 10_000,
  })
}

export function useEvents(
  cluster: string | undefined,
  filter: { namespace?: string; involvedName?: string; involvedKind?: string; involvedUID?: string },
) {
  return useQuery({
    queryKey: ['events', cluster, filter],
    queryFn: () => api.events(cluster!, { ...filter, limit: 100 }),
    enabled: !!cluster,
    refetchInterval: 15_000,
  })
}

/** Batch-checks permissions so the UI only offers actions that will succeed. */
export function useAccess(
  cluster: string | undefined,
  checks: Parameters<typeof api.access>[1],
) {
  return useQuery({
    queryKey: ['access', cluster, checks],
    queryFn: () => api.access(cluster!, checks),
    enabled: !!cluster && checks.length > 0,
    staleTime: 30_000,
  })
}
