import { computed, onMounted, onUnmounted, ref, shallowRef } from 'vue'
import { ApiError, api } from '@/api/client'
import type { Camera, CameraState, CameraStatus, NewCamera } from '@/types'

/** The firmware asks for a couple of seconds between polls. Take it literally. */
export const STATUS_POLL_MS = 2000
/** The camera list changes when someone adds or removes one, so it is lazier. */
export const LIST_REFRESH_EVERY = 5

const cameras = shallowRef<Camera[]>([])
const statuses = ref<Record<string, CameraStatus>>({})
const statusErrors = ref<Record<string, string>>({})
const listError = ref<string | null>(null)
const loaded = ref(false)
const polling = ref(false)

let timer: ReturnType<typeof setInterval> | null = null
let consumers = 0
let ticks = 0
let inFlight = false

function pageVisible(): boolean {
  return typeof document === 'undefined' || !document.hidden
}

async function refreshList(): Promise<void> {
  try {
    const list = await api.listCameras()
    cameras.value = Array.isArray(list) ? list : []
    listError.value = null
    // Forget state belonging to cameras that have gone away.
    const live = new Set(cameras.value.map((c) => c.id))
    for (const id of Object.keys(statuses.value)) if (!live.has(id)) delete statuses.value[id]
    for (const id of Object.keys(statusErrors.value)) if (!live.has(id)) delete statusErrors.value[id]
  } catch (err) {
    const detail = err instanceof ApiError ? err.message : String(err)
    listError.value = `Could not load cameras: ${detail}`
  } finally {
    loaded.value = true
  }
}

/**
 * One pass over every online camera, run from a single timer for the whole app.
 * Tiles never poll for themselves.
 */
async function refreshStatuses(): Promise<void> {
  const targets = cameras.value.filter((c) => c.online)
  const offline = cameras.value.filter((c) => !c.online)
  for (const cam of offline) {
    delete statuses.value[cam.id]
    delete statusErrors.value[cam.id]
  }
  if (targets.length === 0) return

  const results = await Promise.allSettled(targets.map((c) => api.cameraStatus(c.id)))
  results.forEach((result, i) => {
    const id = targets[i]!.id
    if (result.status === 'fulfilled' && result.value) {
      statuses.value[id] = result.value
      delete statusErrors.value[id]
    } else {
      const reason = result.status === 'rejected' ? result.reason : undefined
      statusErrors.value[id] = reason instanceof ApiError ? reason.message : 'Status unavailable'
      delete statuses.value[id]
    }
  })
}

async function tick(force = false): Promise<void> {
  if (inFlight) return
  if (!force && !pageVisible()) return
  inFlight = true
  try {
    if (ticks % LIST_REFRESH_EVERY === 0) await refreshList()
    await refreshStatuses()
    ticks += 1
  } finally {
    inFlight = false
  }
}

function onVisibilityChange() {
  if (pageVisible()) void tick(true)
}

function start() {
  if (timer) return
  polling.value = true
  ticks = 0
  void tick(true)
  timer = setInterval(() => void tick(), STATUS_POLL_MS)
  document.addEventListener('visibilitychange', onVisibilityChange)
}

function stop() {
  if (timer) clearInterval(timer)
  timer = null
  polling.value = false
  document.removeEventListener('visibilitychange', onVisibilityChange)
}

export function cameraState(camera: Camera, status?: CameraStatus): CameraState {
  if (!camera.online) return 'offline'
  return status?.active ? 'recording' : 'watching'
}

/**
 * Shared camera state. Every consumer reads the same list and the same status
 * poll; mounting more of them does not create more traffic.
 */
export function useCameraStore() {
  onMounted(() => {
    consumers += 1
    if (consumers === 1) start()
    else void tick(true)
  })
  onUnmounted(() => {
    consumers -= 1
    if (consumers === 0) stop()
  })

  const cameraById = computed(() => {
    const map = new Map<string, Camera>()
    for (const cam of cameras.value) map.set(cam.id, cam)
    return map
  })

  async function addCamera(input: NewCamera): Promise<void> {
    await api.addCamera(input)
    ticks = 0
    await tick(true)
  }

  async function removeCamera(id: string): Promise<void> {
    await api.removeCamera(id)
    cameras.value = cameras.value.filter((c) => c.id !== id)
    delete statuses.value[id]
    delete statusErrors.value[id]
    ticks = 0
    await tick(true)
  }

  return {
    cameras,
    statuses,
    statusErrors,
    listError,
    loaded,
    polling,
    cameraById,
    addCamera,
    removeCamera,
    refresh: () => tick(true),
  }
}
