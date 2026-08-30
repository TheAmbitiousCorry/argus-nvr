import { onMounted, onUnmounted, ref } from 'vue'

const visible = ref(typeof document === 'undefined' || !document.hidden)
let listeners = 0

function sync() {
  visible.value = !document.hidden
}

/**
 * Whether this tab is in front. A backgrounded tab keeps MJPEG connections
 * open, and every open connection costs the camera frame rate, so streams and
 * status polling both stand down while the tab is hidden.
 */
export function usePageVisible() {
  onMounted(() => {
    if (listeners === 0) document.addEventListener('visibilitychange', sync)
    listeners += 1
    sync()
  })
  onUnmounted(() => {
    listeners -= 1
    if (listeners === 0) document.removeEventListener('visibilitychange', sync)
  })
  return visible
}
