import { computed, onMounted, onUnmounted, ref, shallowRef, watch, type Ref } from 'vue'
import { FocusEngine, type CameraSignal, type FocusResult } from './focusEngine'
import type { Camera, CameraStatus } from '@/types'

export type LayoutId = 'grid' | 'focus' | 'single'

export interface LayoutChoice {
  id: LayoutId
  label: string
  hint: string
  /** How many cameras this layout shows at full size. 0 means every one. */
  slots: number
}

export const LAYOUTS: readonly LayoutChoice[] = [
  { id: 'grid', label: 'Grid', hint: 'Every camera the same size', slots: 0 },
  { id: 'focus', label: 'Focus', hint: 'One large, the rest along the bottom', slots: 1 },
  { id: 'single', label: 'Single', hint: 'One camera, nothing else streaming', slots: 1 },
]

export const ROTATE_CHOICES = [5000, 10_000, 20_000, 30_000, 60_000]

const KEY = 'argus.wall.'
/** The order settles between polls, so half a second is often enough to look at. */
const TICK_MS = 500

function read(name: string): string | null {
  try {
    return localStorage.getItem(KEY + name)
  } catch {
    // Private windows and locked-down browsers throw rather than return null.
    return null
  }
}

function write(name: string, value: string): void {
  try {
    localStorage.setItem(KEY + name, value)
  } catch {
    // A preference that cannot be saved is not worth failing the wall over.
  }
}

function storedLayout(): LayoutId {
  const saved = read('layout')
  return LAYOUTS.some((l) => l.id === saved) ? (saved as LayoutId) : 'grid'
}

function storedInterval(): number {
  const saved = Number(read('rotateMs'))
  return ROTATE_CHOICES.includes(saved) ? saved : 10_000
}

function signalsOf(cameras: Camera[], statuses: Record<string, CameraStatus>): CameraSignal[] {
  return cameras.map((camera) => {
    const status = statuses[camera.id]
    return {
      id: camera.id,
      online: camera.status?.online === true,
      recording: status?.active === true,
      change: status?.change ?? 0,
      threshold: status?.threshold ?? 0,
    }
  })
}

/**
 * The wall's arrangement: which layout, whether the spare slots take turns,
 * and the running order the focus engine settles on. Held here rather than in
 * the view so the timing keeps running while the view re-renders.
 */
export function useWallLayout(
  cameras: Ref<Camera[]>,
  statuses: Ref<Record<string, CameraStatus>>,
) {
  const layout = ref<LayoutId>(storedLayout())
  const rotate = ref(read('rotate') !== 'off')
  const rotateMs = ref(storedInterval())
  /**
   * A camera the viewer picked. Not saved: it is a thing you do for a minute,
   * not a preference, and coming back to a wall stuck on one camera with no
   * memory of having asked would be baffling.
   */
  const pinned = ref<string | null>(null)

  watch(layout, (value) => write('layout', value))
  watch(rotate, (value) => write('rotate', value ? 'on' : 'off'))
  watch(rotateMs, (value) => write('rotateMs', String(value)))

  const engine = new FocusEngine()
  const result = shallowRef<FocusResult>({ order: [], moving: [], rotating: false })

  const slots = computed(() => {
    const choice = LAYOUTS.find((l) => l.id === layout.value)
    return choice && choice.slots > 0 ? choice.slots : cameras.value.length
  })

  function recompute() {
    const next = engine.update(signalsOf(cameras.value, statuses.value), {
      now: Date.now(),
      slots: slots.value,
      // The grid gives every camera the same room by design, so nothing takes
      // turns in it and tiles keep the positions the viewer learned.
      rotate: rotate.value && layout.value !== 'grid',
      rotateMs: rotateMs.value,
      pinned: pinned.value,
    })
    const same =
      next.rotating === result.value.rotating &&
      next.order.join() === result.value.order.join() &&
      next.moving.join() === result.value.moving.join()
    // Re-rendering the wall twice a second to say nothing changed costs frames
    // the streams could have had.
    if (!same) result.value = next
  }

  let timer: ReturnType<typeof setInterval> | null = null
  onMounted(() => {
    recompute()
    timer = setInterval(recompute, TICK_MS)
  })
  onUnmounted(() => {
    if (timer) clearInterval(timer)
    timer = null
  })

  watch([cameras, statuses, layout, rotate, rotateMs, pinned], recompute, { immediate: true })

  const byId = computed(() => {
    const map = new Map<string, Camera>()
    for (const camera of cameras.value) map.set(camera.id, camera)
    return map
  })

  /** Every camera, most worth looking at first. */
  const ordered = computed<Camera[]>(() => {
    const seen = result.value.order
      .map((id) => byId.value.get(id))
      .filter((c): c is Camera => c !== undefined)
    // A camera added between the last engine pass and this render still belongs
    // on the wall, at the back.
    const known = new Set(seen.map((c) => c.id))
    return [...seen, ...cameras.value.filter((c) => !known.has(c.id))]
  })

  const moving = computed(() => new Set(result.value.moving))
  const featured = computed<Camera[]>(() =>
    layout.value === 'grid' ? [] : ordered.value.slice(0, slots.value),
  )
  const remainder = computed<Camera[]>(() =>
    layout.value === 'grid' ? [] : ordered.value.slice(slots.value),
  )

  function pick(id: string) {
    pinned.value = pinned.value === id ? null : id
  }

  function release() {
    pinned.value = null
  }

  // A camera removed elsewhere must not leave the wall pinned to nothing.
  watch(cameras, (list) => {
    if (pinned.value && !list.some((c) => c.id === pinned.value)) pinned.value = null
  })

  return {
    layout,
    rotate,
    rotateMs,
    pinned,
    slots,
    ordered,
    featured,
    remainder,
    moving,
    rotating: computed(() => result.value.rotating),
    pick,
    release,
  }
}
