import { useEffect, useRef, useState } from 'react'

/**
 * Returns a debounced copy of value: it only changes once the input has been
 * stable for `delay` ms. Used to save settings without an explicit button.
 */
export function useDebouncedValue<T>(value: T, delay = 600): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delay)
    return () => window.clearTimeout(timer)
  }, [value, delay])
  return debounced
}

/**
 * Runs `save` whenever the debounced value changes (skipping the first render).
 * Errors are surfaced through `onError` so the page can toast them.
 */
export function useAutoSave<T>(value: T, save: (value: T) => Promise<unknown>, options?: { delay?: number; onError?: (err: unknown) => void; enabled?: boolean }) {
  const delay = options?.delay ?? 600
  const debounced = useDebouncedValue(value, delay)
  const first = useRef(true)
  const saveRef = useRef(save)
  const errorRef = useRef(options?.onError)
  const enabled = options?.enabled ?? true
  useEffect(() => {
    saveRef.current = save
    errorRef.current = options?.onError
  })
  useEffect(() => {
    if (first.current) {
      first.current = false
      return
    }
    if (!enabled) return
    let cancelled = false
    void (async () => {
      try {
        await saveRef.current(debounced)
      } catch (err) {
        if (!cancelled) errorRef.current?.(err)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [debounced, enabled])
}
