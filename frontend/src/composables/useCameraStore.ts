import { computed, onMounted, onUnmounted, ref, shallowRef } from 'vue'
import { ApiError, api } from '@/api/client'
import type { Camera, CameraFirmware, CameraState, CameraStatus, NewCamera } from '@/types'

/**
 * Whether the backend reached this camera the last time it asked. It lives
 * inside the status envelope rather than on the camera, because it describes
 * the last attempt rather than the camera itself.
 */
export function isOnline(camera: Camera): boolean {
  return camera.status?.online === true
}

/**
 * What the camera last reported from its own /version.
 *
 * Read through here rather than straight off the camera. This frontend has
 * already once shown every camera as offline by reading `camera.online` while
 * the backend was sending `camera.status.online`, and a version string is the
 * same shape of guess. Both places are checked, so either answers.
 *
 * Undefined when the camera has not answered, which includes the empty object a
 * backend may send in place of leaving the key out.
 */
export function cameraFirmware(camera: Camera): CameraFirmware | undefined {
  const fw = camera.firmware ?? camera.status?.firmware
  if (!fw || typeof fw !== 'object') return undefined
  const said = fw.version || fw.built || fw.slot || fw.rolledBackFrom || fw.onTrial
  return said ? fw : undefined
}

/** The firmware asks for a couple of seconds between polls. Take it literally. */
export const STATUS_POLL_MS = 2000

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
    // The list already carries each camera's status, so reading it here is what
    // every tile needs and costs nothing extra.
    const next: Record<string, CameraStatus> = {}
    const errors: Record<string, string> = {}
    for (const cam of cameras.value) {
      if (cam.status?.record) next[cam.id] = cam.status.record
      else if (cam.status?.error) errors[cam.id] = cam.status.error
    }
    statuses.value = next
    statusErrors.value = errors
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

async function tick(force = false): Promise<void> {
  if (inFlight) return
  if (!force && !pageVisible()) return
  inFlight = true
  try {
    await refreshList()
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
  if (!isOnline(camera)) return 'offline'
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
