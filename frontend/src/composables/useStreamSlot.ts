import { onBeforeUnmount, ref, watch, type Ref } from 'vue'

/**
 * Browsers cap simultaneous connections to one origin at six over HTTP/1.1, and
 * an MJPEG stream holds its connection until it is torn down. Six streams would
 * therefore starve the status polling that keeps the wall's dots honest, so
 * streams queue for a limited number of slots and everything else falls back to
 * periodic snapshots.
 */
export const MAX_CONCURRENT_STREAMS = 4

interface Waiter {
  /** Whether this consumer holds a slot right now. */
  flag: Ref<boolean>
  /** Higher wins. The large feed on the wall asks louder than the small ones. */
  priority: number
  /** Order of asking, so equals are served first come. 0 means not asking. */
  since: number
}

const waiters = new Map<symbol, Waiter>()
let asked = 0

/** Live count, for the wall to explain itself in the UI. */
export const streamingCount = ref(0)
export const queuedCount = ref(0)

/**
 * Hand out the slots: loudest first, and among equals whoever asked first.
 * Rerun on every change, which is what lets a tile promoted to the large slot
 * take a connection off a small one instead of queueing behind it. A consumer
 * that already holds a slot keeps it against every equal, so an ordinary
 * reshuffle never tears down a healthy stream.
 */
function allocate() {
  const asking = [...waiters].filter(([, w]) => w.since > 0)
  asking.sort(([, a], [, b]) => b.priority - a.priority || a.since - b.since)
  const winners = new Set(asking.slice(0, MAX_CONCURRENT_STREAMS).map(([key]) => key))
  for (const [key, waiter] of waiters) {
    const holds = winners.has(key)
    if (waiter.flag.value !== holds) waiter.flag.value = holds
  }
  streamingCount.value = winners.size
  queuedCount.value = asking.length - winners.size
}

/**
 * Ask for a stream slot whenever `wanted` is true. The returned ref says
 * whether this consumer actually holds one.
 */
export function useStreamSlot(wanted: Ref<boolean>, priority?: Ref<number>) {
  const key = Symbol('stream-slot')
  const hasSlot = ref(false)
  waiters.set(key, { flag: hasSlot, priority: priority?.value ?? 0, since: 0 })

  watch(
    [wanted, () => priority?.value ?? 0],
    ([want, rank]) => {
      const waiter = waiters.get(key)
      if (!waiter) return
      waiter.priority = rank
      // Asking again after standing down goes to the back of its own priority.
      if (want && waiter.since === 0) waiter.since = ++asked
      if (!want) waiter.since = 0
      allocate()
    },
    { immediate: true },
  )

  onBeforeUnmount(() => {
    waiters.delete(key)
    hasSlot.value = false
    allocate()
  })

  return hasSlot
}
