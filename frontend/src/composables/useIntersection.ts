import { onBeforeUnmount, onMounted, ref, watch, type Ref } from 'vue'

/**
 * Reports whether an element is on screen. Tiles use this to decide whether
 * their stream is worth holding open; a tile scrolled out of the viewport
 * gives its connection back.
 */
export function useIntersection(
  el: Ref<HTMLElement | null>,
  options: IntersectionObserverInit = { rootMargin: '96px', threshold: 0.01 },
) {
  // Without the API, assume visible: too many streams beats a blank wall.
  const supported = typeof IntersectionObserver !== 'undefined'
  const visible = ref(!supported)
  let observer: IntersectionObserver | null = null

  function observe(target: HTMLElement | null) {
    observer?.disconnect()
    if (!target || !observer) return
    observer.observe(target)
  }

  onMounted(() => {
    if (!supported) return
    observer = new IntersectionObserver((entries) => {
      for (const entry of entries) visible.value = entry.isIntersecting
    }, options)
    observe(el.value)
  })

  watch(el, (target) => observe(target))

  onBeforeUnmount(() => {
    observer?.disconnect()
    observer = null
    visible.value = false
  })

  return visible
}
