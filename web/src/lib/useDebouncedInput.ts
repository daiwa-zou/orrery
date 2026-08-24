import { useEffect, useState } from 'react'

/**
 * Text input state that commits to the URL after a pause. Every committed
 * value is a server round trip, so keystrokes should not each cost one — and
 * half-typed label selectors are invalid anyway.
 */
export function useDebouncedInput(
  urlValue: string,
  commit: (value: string) => void,
  delay = 300,
): [string, (v: string) => void] {
  const [value, setValue] = useState(urlValue)

  // Adopt outside changes (back button, palette) without clobbering typing.
  useEffect(() => {
    setValue(urlValue)
  }, [urlValue])

  useEffect(() => {
    if (value === urlValue) return
    const t = window.setTimeout(() => commit(value), delay)
    return () => window.clearTimeout(t)
  }, [value, urlValue, commit, delay])

  return [value, setValue]
}
