import { onBeforeUnmount, ref, watch, type Ref } from 'vue'

/**
 * Browsers cap simultaneous connections to one origin at six over HTTP/1.1, and
 * an MJPEG stream holds its connection until it is torn down. Six streams would
 * therefore starve the status polling that keeps the wall's dots honest, so
 * streams queue for a limited number of slots and everything else falls back to
 * periodic snapshots.
 */
export const MAX_CONCURRENT_STREAMS = 4

const holders = new Set<symbol>()
const waiting: symbol[] = []
const granted = new Map<symbol, Ref<boolean>>()

/** Live count, for the wall to explain itself in the UI. */
export const streamingCount = ref(0)
export const queuedCount = ref(0)

function sync() {
  streamingCount.value = holders.size
  queuedCount.value = waiting.length
}

function promoteWaiting() {
  while (holders.size < MAX_CONCURRENT_STREAMS && waiting.length > 0) {
    const next = waiting.shift()!
    const flag = granted.get(next)
    if (!flag) continue
    holders.add(next)
    flag.value = true
  }
  sync()
}

function acquire(key: symbol, flag: Ref<boolean>) {
  if (holders.has(key) || waiting.includes(key)) return
  if (holders.size < MAX_CONCURRENT_STREAMS) {
    holders.add(key)
    flag.value = true
  } else {
    waiting.push(key)
    flag.value = false
  }
  sync()
}

function release(key: symbol, flag: Ref<boolean>) {
  const held = holders.delete(key)
  const queuedAt = waiting.indexOf(key)
  if (queuedAt !== -1) waiting.splice(queuedAt, 1)
  flag.value = false
  if (held) promoteWaiting()
  else sync()
}

/**
 * Ask for a stream slot whenever `wanted` is true. The returned ref says
 * whether this consumer actually holds one.
 */
export function useStreamSlot(wanted: Ref<boolean>) {
  const key = Symbol('stream-slot')
  const hasSlot = ref(false)
  granted.set(key, hasSlot)

  watch(
    wanted,
    (want) => {
      if (want) acquire(key, hasSlot)
      else release(key, hasSlot)
    },
    { immediate: true },
  )

  onBeforeUnmount(() => {
    release(key, hasSlot)
    granted.delete(key)
  })

  return hasSlot
}
