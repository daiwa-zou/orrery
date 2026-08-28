import { useSyncExternalStore } from 'react'

import { readRaw, subscribeToKey } from './storage'

/**
 * The raw stored string for a key, kept current.
 *
 * localStorage is an external mutable store, so it is read with the primitive
 * meant for one rather than copied into state by an effect. The difference is
 * visible: an effect runs after paint, so mirroring shows one frame of the
 * wrong thing — a table without the label columns that were chosen for it, an
 * unstarred star on a view that is starred — before correcting itself.
 *
 * It returns the raw string rather than a parsed value on purpose.
 * useSyncExternalStore compares snapshots by identity, and a freshly parsed
 * array is a new object every time it is asked, which is an infinite render
 * loop. Callers parse the string in a memo keyed on it.
 *
 * Because the subscription also listens for the browser's `storage` event,
 * starring a view in one tab now updates the star in another.
 */
export function useStoredRaw(key: string): string | null {
  return useSyncExternalStore(
    (onChange) => subscribeToKey(key, onChange),
    () => readRaw(key),
    () => null,
  )
}
