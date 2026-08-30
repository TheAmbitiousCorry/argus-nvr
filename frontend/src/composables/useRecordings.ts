import { computed, ref, watch } from 'vue'
import { ApiError, api } from '@/api/client'
import type { Recording, RecordingDay } from '@/types'

/**
 * One page. Big enough that most days arrive whole, small enough that a fleet
 * with months of footage does not build a table nobody scrolls to the end of.
 */
export const PAGE_SIZE = 100

/** `2026-08-30` and `211610` are the camera's own clock. Neither is parsed as a date. */
export function clockOf(at: string): string {
  if (!/^\d{6}$/.test(at)) return at
  return `${at.slice(0, 2)}:${at.slice(2, 4)}:${at.slice(4, 6)}`
}

/** Which day of the week a date string names, without going near a timezone. */
export function weekdayOf(day: string): string {
  const parts = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day)
  if (!parts) return ''
  const date = new Date(Date.UTC(+parts[1], +parts[2] - 1, +parts[3]))
  return ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'][date.getUTCDay()] ?? ''
}

export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return '-'
  const total = Math.round(ms / 1000)
  const mins = Math.floor(total / 60)
  const secs = total % 60
  if (mins === 0) return `${(ms / 1000).toFixed(1)}s`
  return `${mins}m ${String(secs).padStart(2, '0')}s`
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes)) return '-'
  if (bytes < 1024) return `${bytes} B`
  const kb = bytes / 1024
  if (kb < 1024) return `${kb.toFixed(0)} KB`
  const mb = kb / 1024
  if (mb < 1024) return `${mb.toFixed(1)} MB`
  return `${(mb / 1024).toFixed(2)} GB`
}

function describe(err: unknown): string {
  if (err instanceof ApiError && err.status === 404) {
    return 'The service is holding no recordings: it is running without a recordings directory.'
  }
  return err instanceof ApiError ? err.message : String(err)
}

/**
 * The archive, as one filtered and paged list.
 *
 * The day index is fetched once and drives both filters, so the date control
 * offers only dates that have something behind them rather than a calendar of
 * mostly empty days.
 */
export function useRecordings() {
  const cameraId = ref('')
  const day = ref('')

  const recordings = ref<Recording[]>([])
  const days = ref<RecordingDay[]>([])
  const more = ref(false)
  const loading = ref(false)
  const loadingMore = ref(false)
  const loaded = ref(false)
  const error = ref<string | null>(null)
  const daysError = ref<string | null>(null)

  let controller: AbortController | null = null

  /** Cameras that hold something, which is not the same set as cameras that exist. */
  const cameraOptions = computed(() => {
    const totals = new Map<string, number>()
    for (const entry of days.value) {
      totals.set(entry.cameraId, (totals.get(entry.cameraId) ?? 0) + entry.recordings)
    }
    return [...totals].map(([id, count]) => ({ id, count }))
  })

  /** Days that hold something for the camera in the filter, newest first. */
  const dayOptions = computed(() => {
    const totals = new Map<string, number>()
    for (const entry of days.value) {
      if (cameraId.value && entry.cameraId !== cameraId.value) continue
      totals.set(entry.day, (totals.get(entry.day) ?? 0) + entry.recordings)
    }
    return [...totals]
      .map(([date, count]) => ({ day: date, count }))
      .sort((a, b) => (a.day < b.day ? 1 : a.day > b.day ? -1 : 0))
  })

  /** How many the filter is over, from the index rather than from the page in hand. */
  const heldCount = computed(() =>
    days.value.reduce((sum, entry) => {
      if (cameraId.value && entry.cameraId !== cameraId.value) return sum
      if (day.value && entry.day !== day.value) return sum
      return sum + entry.recordings
    }, 0),
  )

  async function loadDays(): Promise<void> {
    try {
      const res = await api.recordingDays()
      days.value = res?.days ?? []
      daysError.value = null
    } catch (err) {
      days.value = []
      daysError.value = describe(err)
    }
  }

  async function load(): Promise<void> {
    controller?.abort()
    const own = new AbortController()
    controller = own
    loading.value = true
    error.value = null
    try {
      const page = await api.listRecordings(
        { cameraId: cameraId.value, day: day.value, limit: PAGE_SIZE },
        own.signal,
      )
      if (own.signal.aborted) return
      recordings.value = page?.recordings ?? []
      more.value = page?.more === true
    } catch (err) {
      if (own.signal.aborted) return
      recordings.value = []
      more.value = false
      error.value = describe(err)
    } finally {
      if (controller === own) {
        controller = null
        loading.value = false
        loaded.value = true
      }
    }
  }

  /**
   * The next page starts where this one ended. `start` counts recordings rather
   * than pages, which is what both halves of this system agree it means.
   */
  async function loadMore(): Promise<void> {
    if (loadingMore.value || loading.value || !more.value) return
    loadingMore.value = true
    try {
      const page = await api.listRecordings({
        cameraId: cameraId.value,
        day: day.value,
        start: recordings.value.length,
        limit: PAGE_SIZE,
      })
      recordings.value = [...recordings.value, ...(page?.recordings ?? [])]
      more.value = page?.more === true
    } catch (err) {
      error.value = describe(err)
    } finally {
      loadingMore.value = false
    }
  }

  async function refresh(): Promise<void> {
    await loadDays()
    await load()
  }

  // Picking a camera that has nothing on the chosen day would show an empty
  // table with both filters looking reasonable. Drop the day instead, and let
  // the second run of this watcher do the fetch, so it stays one request.
  watch([cameraId, day], ([, chosenDay]) => {
    if (chosenDay && !dayOptions.value.some((option) => option.day === chosenDay)) {
      day.value = ''
      return
    }
    void load()
  })

  return {
    cameraId,
    day,
    recordings,
    days,
    cameraOptions,
    dayOptions,
    heldCount,
    more,
    loading,
    loadingMore,
    loaded,
    error,
    daysError,
    refresh,
    loadMore,
    stop: () => controller?.abort(),
  }
}
