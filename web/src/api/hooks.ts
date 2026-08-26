import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient, keepPreviousData } from '@tanstack/react-query'
import { api, wsURL, groupSegment, type ListParams, type ResourceRef } from './client'
import type { KubeObject, ListResponse, Row, WatchMessage } from './types'

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

/** Authz/informer cache statistics — feeds the Control plane card. */
export function useCacheStats(cluster?: string) {
  return useQuery({
    queryKey: ['stats', cluster],
    queryFn: ({ signal }) => api.cacheStats(cluster!, signal),
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
  /** See stalledReason: a retry parked by the online manager. */
  stalled?: unknown
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
/**
 * A fetch that has failed at least once and is now parked.
 *
 * React Query pauses retries when its online manager says the browser is
 * offline, and a paused retry does not resume on its own. The query is then
 * neither loading (nothing is in flight) nor errored (the retries are not
 * exhausted), which is a state with no data and no explanation — and a page
 * that only handles "loading" and "error" renders either nothing at all or,
 * worse, an empty list that reads as "there is nothing here".
 *
 * Surfacing it costs one field and turns a blank page into a sentence.
 */
export function stalledReason(query: {
  isPaused: boolean
  failureReason: unknown
  data: unknown
}): unknown {
  if (!query.isPaused || query.data !== undefined) return undefined
  return query.failureReason ?? new Error('the browser appears to be offline')
}

type LiveState = LiveListState['live']

/**
 * The reconnecting watch socket, shared by the live list and the live object.
 *
 * It owns the connection lifecycle and the protocol messages that mean "your
 * view is no longer trustworthy" — OVERFLOW, ERROR, and returning from a drop.
 * Callers handle only the data events and are told when to resync.
 */
function useWatchSocket(
  url: string | null,
  onEvent: (msg: WatchMessage) => void,
  onResync: () => void,
): LiveState {
  const [live, setLive] = useState<LiveState>('off')
  const eventRef = useRef(onEvent)
  eventRef.current = onEvent
  const resyncRef = useRef(onResync)
  resyncRef.current = onResync

  useEffect(() => {
    if (!url) {
      setLive('off')
      return
    }

    let closed = false
    let socket: WebSocket | undefined
    let retryTimer: number | undefined
    let attempts = 0
    let dropped = false
    setLive('connecting')

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
          resyncRef.current()
        }
      }

      socket.onmessage = (event) => {
        // A message task can already be queued when cleanup runs; the caller's
        // keys belong to the next view by then, so acting on it would poke the
        // wrong query.
        if (closed) return
        let msg: WatchMessage
        try {
          msg = JSON.parse(event.data)
        } catch {
          return
        }

        switch (msg.type) {
          case 'OVERFLOW':
            // We fell behind the cluster; the only honest recovery is a reload.
            setLive('polling')
            resyncRef.current()
            break
          case 'ERROR':
            setLive('polling')
            break
          default:
            eventRef.current(msg)
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
      window.clearTimeout(retryTimer)
      if (socket) {
        // Detach before closing so the close event does not schedule a retry
        // and an already-queued message cannot touch the next view's state.
        socket.onclose = null
        socket.onmessage = null
        socket.onerror = null
        socket.close()
      }
    }
  }, [url])

  return live
}

export function useLiveList(ref: ResourceRef | null, params: ListParams): LiveListState {
  const qc = useQueryClient()

  const key = ref
    ? ['list', ref.cluster, ref.group, ref.version, ref.resource, params]
    : ['list', 'none']
  const keyRef = useRef(key)
  keyRef.current = key
  const refetchTimer = useRef<number | undefined>(undefined)

  // The watch carries the narrowing filters so a filtered page is not woken by
  // every change elsewhere in scope; sort and paging stay REST-only.
  const url = ref
    ? wsURL(
        `/clusters/${ref.cluster}/ws/watch/${groupSegment(ref.group)}/${ref.version}/${ref.resource}`,
        {
          namespace: params.namespace,
          q: params.q,
          labelSelector: params.labelSelector,
          fieldSelector: params.fieldSelector,
        },
      )
    : null

  const resync = useCallback(() => {
    qc.invalidateQueries({ queryKey: keyRef.current })
  }, [qc])

  const onEvent = useCallback(
    (msg: WatchMessage) => {
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
          window.clearTimeout(refetchTimer.current)
          refetchTimer.current = window.setTimeout(() => {
            qc.invalidateQueries({ queryKey: keyRef.current })
          }, 700)
          break
      }
    },
    [qc],
  )

  const live = useWatchSocket(url, onEvent, resync)

  const query = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => api.list(ref!, params, signal),
    enabled: !!ref,
    placeholderData: keepPreviousData,
    // The watch is the freshness mechanism; this is a safety net for a socket
    // that silently dies behind a proxy.
    refetchInterval: live === 'live' ? false : 15_000,
  })

  useEffect(() => () => window.clearTimeout(refetchTimer.current), [])

  return {
    data: query.data,
    isLoading: query.isLoading,
    error: query.error,
    stalled: stalledReason(query),
    live,
    refetch: query.refetch,
  }
}

export interface LiveObjectState {
  data?: KubeObject
  isLoading: boolean
  error: unknown
  live: LiveState
  refetch: () => void
  /** See stalledReason: a retry parked by the online manager. */
  stalled?: unknown
}

/**
 * One object, kept current by the same watch the lists use.
 *
 * The watch streams projected rows rather than whole objects, so it is used
 * here as a change signal: any event for this object refetches the
 * authoritative copy. That costs one request per actual change instead of one
 * every ten seconds regardless, and the page updates in the same breath the
 * list does.
 */
export function useLiveResource(ref: ResourceRef | null): LiveObjectState {
  const qc = useQueryClient()

  const key = ref
    ? ['object', ref.cluster, ref.group, ref.version, ref.resource, ref.namespace, ref.name]
    : ['object', 'none']
  const keyRef = useRef(key)
  keyRef.current = key

  const url = ref?.name
    ? wsURL(
        `/clusters/${ref.cluster}/ws/watch/${groupSegment(ref.group)}/${ref.version}/${ref.resource}`,
        { namespace: ref.namespace, fieldSelector: `metadata.name=${ref.name}` },
      )
    : null

  const resync = useCallback(() => {
    qc.invalidateQueries({ queryKey: keyRef.current })
  }, [qc])

  // Every event for a single object — added, modified or deleted — means the
  // copy on screen is stale, so they all resolve to the same refetch.
  const live = useWatchSocket(url, resync, resync)

  const query = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => api.get(ref!, signal),
    enabled: !!ref?.name,
    // Safety net for a socket that silently dies behind a proxy; the watch is
    // what actually keeps this current.
    refetchInterval: live === 'live' ? false : 15_000,
  })

  return {
    data: query.data,
    isLoading: query.isLoading,
    error: query.error,
    stalled: stalledReason(query),
    live,
    refetch: query.refetch,
  }
}

/**
 * Search-autocomplete vocabulary. Fetched lazily (enabled when the search bar
 * first gains focus) so pages that never search never pay for the scan.
 */
export function useFacets(ref: ResourceRef | null, namespace: string, enabled: boolean) {
  return useQuery({
    queryKey: ref
      ? ['facets', ref.cluster, ref.group, ref.version, ref.resource, namespace]
      : ['facets', 'none'],
    queryFn: ({ signal }) => api.facets(ref!, namespace || undefined, signal),
    enabled: !!ref && enabled,
    staleTime: 30_000,
  })
}


export function useEvents(
  cluster: string | undefined,
  filter: {
    namespace?: string
    q?: string
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
