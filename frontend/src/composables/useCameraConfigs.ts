import { ref } from 'vue'
import { ApiError, api } from '@/api/client'
import type { CameraConfig } from '@/types'

/**
 * Loads and caches each camera's /config document.
 *
 * Configs are fetched once and kept rather than polled: they only change when
 * something writes them, and a camera answering /config is a round trip that
 * competes with the video the same device is trying to send.
 */
export function useCameraConfigs() {
  const configs = ref<Record<string, CameraConfig>>({})
  const errors = ref<Record<string, string>>({})
  const loading = ref<Record<string, boolean>>({})

  async function loadOne(id: string, force = false): Promise<void> {
    if (loading.value[id]) return
    if (!force && configs.value[id]) return
    loading.value[id] = true
    delete errors.value[id]
    try {
      configs.value[id] = await api.cameraConfig(id)
    } catch (err) {
      delete configs.value[id]
      errors.value[id] = err instanceof ApiError ? err.message : String(err)
    } finally {
      delete loading.value[id]
    }
  }

  /** Sequential on purpose: these are microcontrollers, not a web farm. */
  async function load(ids: string[], force = false): Promise<void> {
    for (const id of ids) await loadOne(id, force)
  }

  function forget(id: string): void {
    delete configs.value[id]
    delete errors.value[id]
  }

  return { configs, errors, loading, load, loadOne, forget }
}
