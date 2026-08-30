<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { api } from '@/api/client'
import { useStreamSlot } from '@/composables/useStreamSlot'

const props = withDefaults(
  defineProps<{
    cameraId: string
    name: string
    online: boolean
    /** Whether this view wants live video right now. */
    active: boolean
    fit?: 'cover' | 'contain'
  }>(),
  { fit: 'cover' },
)

/** A transparent pixel. Assigning it aborts an MJPEG connection without a request. */
const BLANK = 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7'
/** Snapshot refresh for tiles that are visible but could not get a stream slot. */
const SNAPSHOT_MS = 5000
/** No decoded frame within this window counts as a failure worth retrying. */
const FIRST_FRAME_TIMEOUT_MS = 10000
const RETRY_BASE_MS = 2000
const RETRY_MAX_MS = 30000

type Phase = 'idle' | 'connecting' | 'live' | 'failed'

const wanted = computed(() => props.active && props.online)
const hasSlot = useStreamSlot(wanted)

const phase = ref<Phase>('idle')
const failures = ref(0)
const nonce = ref(0)
const imgEl = ref<HTMLImageElement | null>(null)
const snapshotOk = ref(false)

let retryTimer: ReturnType<typeof setTimeout> | null = null
let frameWatch: ReturnType<typeof setInterval> | null = null
let firstFrameDeadline = 0
let snapshotTimer: ReturnType<typeof setInterval> | null = null

/** Live video: wanted, holding a slot, and not sitting in a retry backoff. */
const streaming = computed(() => wanted.value && hasSlot.value && phase.value !== 'failed')
/** Wanted but queued behind other streams, so it makes do with still frames. */
const snapshotting = computed(() => wanted.value && !hasSlot.value)

const streamSrc = computed(() => api.streamUrl(props.cameraId, nonce.value))
const snapshotSrc = ref('')

function clearTimers() {
  if (retryTimer) clearTimeout(retryTimer)
  if (frameWatch) clearInterval(frameWatch)
  retryTimer = null
  frameWatch = null
}

/**
 * A multipart stream in an <img> does not fire `load` per frame in every
 * browser, so a decoded frame is detected by the image gaining intrinsic size.
 */
function watchForFirstFrame() {
  if (frameWatch) clearInterval(frameWatch)
  firstFrameDeadline = Date.now() + FIRST_FRAME_TIMEOUT_MS
  frameWatch = setInterval(() => {
    if (imgEl.value && imgEl.value.naturalWidth > 0) {
      phase.value = 'live'
      failures.value = 0
      if (frameWatch) clearInterval(frameWatch)
      frameWatch = null
    } else if (Date.now() > firstFrameDeadline) {
      fail()
    }
  }, 500)
}

function fail() {
  clearTimers()
  detachStream()
  phase.value = 'failed'
  failures.value += 1
  if (!wanted.value) return
  const delay = Math.min(RETRY_BASE_MS * 2 ** (failures.value - 1), RETRY_MAX_MS)
  retryTimer = setTimeout(() => {
    if (wanted.value) startStream()
  }, delay)
}

/** Tear the connection down explicitly rather than trusting element removal. */
function detachStream() {
  if (imgEl.value && imgEl.value.src !== BLANK) imgEl.value.src = BLANK
}

function startStream() {
  clearTimers()
  nonce.value = Date.now()
  phase.value = 'connecting'
  watchForFirstFrame()
}

function stopStream() {
  clearTimers()
  detachStream()
  phase.value = 'idle'
}

function refreshSnapshot() {
  snapshotSrc.value = api.snapshotUrl(props.cameraId, Date.now())
}

function startSnapshots() {
  if (snapshotTimer) return
  refreshSnapshot()
  snapshotTimer = setInterval(refreshSnapshot, SNAPSHOT_MS)
}

function stopSnapshots() {
  if (snapshotTimer) clearInterval(snapshotTimer)
  snapshotTimer = null
  snapshotOk.value = false
  snapshotSrc.value = ''
}

// Two sources rather than a getter returning a new array: an array getter
// compares by identity and would re-fire on every render, tearing down a
// healthy stream and reconnecting it.
watch(
  [wanted, hasSlot],
  ([want, slot]) => {
    if (want && slot) {
      stopSnapshots()
      if (phase.value === 'idle') startStream()
    } else {
      stopStream()
      failures.value = 0
      if (want) startSnapshots()
      else stopSnapshots()
    }
  },
  { immediate: true },
)

// A camera that goes offline while we watch it should not keep retrying.
watch(
  () => props.online,
  (online) => {
    if (!online) {
      stopStream()
      stopSnapshots()
      failures.value = 0
    }
  },
)

// A different camera in the same slot is a different connection.
watch(
  () => props.cameraId,
  () => {
    stopStream()
    failures.value = 0
    if (wanted.value && hasSlot.value) startStream()
  },
)

onBeforeUnmount(() => {
  clearTimers()
  detachStream()
  stopSnapshots()
})

function retryNow() {
  failures.value = 0
  if (wanted.value && hasSlot.value) startStream()
  else if (wanted.value) refreshSnapshot()
}

defineExpose({ retryNow })
</script>

<template>
  <div class="stream" :class="`fit-${fit}`">
    <img
      v-if="streaming"
      ref="imgEl"
      class="frame"
      :src="streamSrc"
      :alt="`Live view from ${name}`"
      decoding="async"
      @error="fail"
    />

    <img
      v-else-if="snapshotting && snapshotSrc"
      class="frame"
      :src="snapshotSrc"
      :alt="`Latest still from ${name}`"
      decoding="async"
      @load="snapshotOk = true"
      @error="snapshotOk = false"
    />

    <div v-if="!online" class="placeholder offline">
      <svg viewBox="0 0 24 24" aria-hidden="true" class="glyph">
        <path
          d="M3 3l18 18M4 7v10a2 2 0 002 2h9M20 17V7a2 2 0 00-2-2H9"
          fill="none"
          stroke="currentColor"
          stroke-width="1.6"
          stroke-linecap="round"
        />
      </svg>
      <p class="label">Offline</p>
      <p class="hint">No response from the camera</p>
    </div>

    <div v-else-if="!active" class="placeholder paused">
      <p class="label">Stream paused</p>
      <p class="hint">Not on screen</p>
    </div>

    <div v-else-if="phase === 'failed'" class="placeholder error">
      <p class="label">Stream failed</p>
      <p class="hint">Retrying, attempt {{ failures }}</p>
      <button type="button" class="retry" @click.stop="retryNow">Retry now</button>
    </div>

    <div v-else-if="streaming && phase === 'connecting'" class="placeholder connecting">
      <p class="label">Connecting</p>
    </div>

    <div v-else-if="snapshotting && !snapshotOk" class="placeholder queued">
      <p class="label">Waiting for a stream slot</p>
      <p class="hint">Too many live streams open</p>
    </div>

    <!-- A still did arrive, so say so in a corner rather than veiling it. -->
    <span v-else-if="snapshotting" class="badge" title="Queued for a live stream slot">
      Stills only
    </span>
  </div>
</template>

<style scoped>
.stream {
  position: relative;
  width: 100%;
  height: 100%;
  background: #0b0b0b;
  overflow: hidden;
}

.frame {
  display: block;
  width: 100%;
  height: 100%;
}
.fit-cover .frame {
  object-fit: cover;
}
.fit-contain .frame {
  object-fit: contain;
}

/*
 * A tile with no picture still says which camera it is, so it shows that
 * camera's own eye behind whatever went wrong. Faint enough to read text over,
 * present enough that a wall of connecting tiles looks deliberate rather than
 * broken.
 */
.placeholder {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 0.35rem;
  padding: 1rem;
  text-align: center;
  color: #8a8a8a;
  background:
    url("data:image/svg+xml,%3Csvg%20fill-opacity%3D%270.07%27%20xmlns%3D%27http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg%27%20viewBox%3D%270%200%2084%20116%27%20fill%3D%27%23ffffff%27%20fill-rule%3D%27nonzero%27%3E%3Cpath%20d%3D%27M44.2%2080.7L45.2%2077.8L46%2074.7L46.6%2071.7L47.1%2068.6L47.3%2065.5L47.3%2062.5L47.1%2059.4L46.6%2056.3L46%2053.3L45.2%2050.2L44.2%2047.3L43%2045.9L41.3%2045.8L39.9%2047L39.8%2048.7L40.8%2051.6L41.5%2054.4L42.1%2057.1L42.5%2059.9L42.7%2062.6L42.7%2065.4L42.5%2068.1L42.1%2070.9L41.5%2073.6L40.8%2076.4L39.8%2079.3L41%2077.9L42.7%2077.8L44.1%2079ZM19.7%2033.5L24%2028.6L28.2%2024.9L32.2%2022.1L36.2%2020.3L40.1%2019.4L43.9%2019.4L47.8%2020.3L51.8%2022.1L55.8%2024.9L60%2028.6L63.7%2032L60%2035.4L55.8%2039.1L51.8%2041.9L47.8%2043.7L43.9%2044.6L40.1%2044.6L36.2%2043.7L32.2%2041.9L28.2%2039.1L24%2035.4L19.7%2030.5ZM16.3%2033.5L20.7%2038.6L25.3%2042.7L30%2045.9L34.7%2048.1L39.6%2049.2L44.4%2049.2L49.3%2048.1L54%2045.9L58.7%2042.7L63.3%2038.6L68.3%2032L63.3%2025.4L58.7%2021.3L54%2018.1L49.3%2015.9L44.4%2014.8L39.6%2014.8L34.7%2015.9L30%2018.1L25.3%2021.3L20.7%2025.4L16.3%2030.5ZM47.2%2031.6L46.9%2033.8L46%2035.3L44.6%2036.5L42.9%2037.1L41.1%2037.1L39.4%2036.5L38%2035.3L37.1%2033.8L36.8%2032L37.1%2030.2L38%2028.7L39.4%2027.5L41.1%2026.9L42.9%2026.9L44.6%2027.5L46%2028.7L47.1%2030.6ZM51%2028.3L49.5%2025.7L46.9%2023.5L43.7%2022.3L40.3%2022.3L37.1%2023.5L34.5%2025.7L32.8%2028.6L32.2%2032L32.8%2035.4L34.5%2038.3L37.1%2040.5L40.3%2041.7L43.7%2041.7L46.9%2040.5L49.5%2038.3L51.2%2035.4L51.8%2032.4ZM26.9%2018.7L12.7%2010.2L13.7%2011.6L13.5%2013.4L12%2014.4L10.3%2014.1L24.5%2022.7L26.2%2022.9L27.7%2021.9L27.9%2020.1ZM34.6%2011.8L26.5%20-2.6L26.7%20-0.9L25.6%200.5L23.9%200.7L22.5%20-0.4L30.6%2014L30.4%2012.3L31.5%2010.9L33.2%2010.7ZM44.3%2010.1L44.3%20-6.5L43.6%20-4.9L42%20-4.2L40.4%20-4.9L39.7%20-6.5L39.7%2010.1L40.4%2011.7L42%2012.3L43.6%2011.7ZM53.4%2014L61.5%20-0.4L61.7%20-2.1L60.7%20-3.5L58.9%20-3.7L57.5%20-2.6L49.4%2011.8L50.8%2010.7L52.5%2010.9L53.6%2012.3ZM59.5%2022.7L73.7%2014.1L74.7%2012.7L74.5%2011L73.1%209.9L71.3%2010.2L57.1%2018.7L58.9%2018.5L60.3%2019.5L60.6%2021.3ZM31.2%2093.5L31.6%2091.8L32.2%2090.5L33.2%2089.2L34.5%2088.1L36%2087.2L37.6%2086.6L39.4%2086.2L41.2%2086.1L43%2086.3L44.6%2086.7L46.1%2087.4L47.3%2088.2L48.2%2089.1L48.9%2090.1L49.2%2091.1L49.4%2092.1L49.3%2093L49%2093.9L48.5%2094.7L47.7%2095.5L46.8%2096.2L45.8%2096.7L44.6%2097L43.4%2097.2L42.2%2097.1L41.2%2096.9L40.2%2096.6L39.4%2096.1L38.8%2095.6L38.3%2095L38.1%2094.5L38%2094.1L38%2093.6L38.1%2093.2L38.3%2092.8L38.6%2092.4L39%2092.1L39.5%2091.8L40.1%2091.6L40.7%2091.5L41.2%2091.5L41.7%2091.6L42.1%2091.8L42.5%2091.9L42.6%2092.1L42.7%2092.2L42.8%2092.2L42.7%2092.2L42.7%2092.1L42.7%2092.3L42.8%2094.1L44.2%2095.2L45.9%2095L47.1%2093.7L47.3%2092.9L47.3%2091.9L47.1%2090.8L46.6%2089.7L45.9%2088.9L45%2088.1L44%2087.5L42.8%2087.2L41.6%2087L40.3%2087L39%2087.2L37.7%2087.6L36.5%2088.3L35.4%2089.1L34.5%2090.3L33.8%2091.6L33.4%2093L33.4%2094.6L33.8%2096.1L34.5%2097.5L35.5%2098.8L36.8%2099.9L38.3%20100.7L39.9%20101.3L41.8%20101.7L43.6%20101.7L45.5%20101.5L47.4%20101L49.2%20100.1L50.7%2099L52.1%2097.5L53.1%2095.9L53.8%2094L54%2092L53.7%2089.9L52.9%2088L51.8%2086.2L50.2%2084.6L48.3%2083.4L46.2%2082.4L43.8%2081.8L41.3%2081.5L38.8%2081.7L36.3%2082.2L34%2083.1L31.8%2084.4L29.9%2086.1L28.3%2088.1L27.3%2090.3L26.8%2092.5L27.8%2091.1L29.5%2090.8L30.9%2091.8Z%27%2F%3E%3Cpath%20d%3D%27M45.6%2032L45.2%2033.7L44%2035L42.4%2035.6L40.7%2035.4L39.3%2034.4L38.5%2032.9L38.5%2031.1L39.3%2029.6L40.7%2028.6L42.4%2028.4L44%2029L45.2%2030.3Z%27%2F%3E%3C%2Fsvg%3E") center 38% / auto 72% no-repeat,
    repeating-linear-gradient(45deg, #101010, #101010 10px, #141414 10px, #141414 20px);
}

/* The eye blinks slowly while a connection is being made, and holds still once
   there is nothing left to wait for. */
.placeholder.connecting,
.placeholder.queued {
  background:
    url("data:image/svg+xml,%3Csvg%20fill-opacity%3D%270.07%27%20xmlns%3D%27http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg%27%20viewBox%3D%270%200%2084%20116%27%20fill%3D%27%23ffffff%27%20fill-rule%3D%27nonzero%27%3E%3Cpath%20d%3D%27M44.2%2080.7L45.2%2077.8L46%2074.7L46.6%2071.7L47.1%2068.6L47.3%2065.5L47.3%2062.5L47.1%2059.4L46.6%2056.3L46%2053.3L45.2%2050.2L44.2%2047.3L43%2045.9L41.3%2045.8L39.9%2047L39.8%2048.7L40.8%2051.6L41.5%2054.4L42.1%2057.1L42.5%2059.9L42.7%2062.6L42.7%2065.4L42.5%2068.1L42.1%2070.9L41.5%2073.6L40.8%2076.4L39.8%2079.3L41%2077.9L42.7%2077.8L44.1%2079ZM19.7%2033.5L24%2028.6L28.2%2024.9L32.2%2022.1L36.2%2020.3L40.1%2019.4L43.9%2019.4L47.8%2020.3L51.8%2022.1L55.8%2024.9L60%2028.6L63.7%2032L60%2035.4L55.8%2039.1L51.8%2041.9L47.8%2043.7L43.9%2044.6L40.1%2044.6L36.2%2043.7L32.2%2041.9L28.2%2039.1L24%2035.4L19.7%2030.5ZM16.3%2033.5L20.7%2038.6L25.3%2042.7L30%2045.9L34.7%2048.1L39.6%2049.2L44.4%2049.2L49.3%2048.1L54%2045.9L58.7%2042.7L63.3%2038.6L68.3%2032L63.3%2025.4L58.7%2021.3L54%2018.1L49.3%2015.9L44.4%2014.8L39.6%2014.8L34.7%2015.9L30%2018.1L25.3%2021.3L20.7%2025.4L16.3%2030.5ZM47.2%2031.6L46.9%2033.8L46%2035.3L44.6%2036.5L42.9%2037.1L41.1%2037.1L39.4%2036.5L38%2035.3L37.1%2033.8L36.8%2032L37.1%2030.2L38%2028.7L39.4%2027.5L41.1%2026.9L42.9%2026.9L44.6%2027.5L46%2028.7L47.1%2030.6ZM51%2028.3L49.5%2025.7L46.9%2023.5L43.7%2022.3L40.3%2022.3L37.1%2023.5L34.5%2025.7L32.8%2028.6L32.2%2032L32.8%2035.4L34.5%2038.3L37.1%2040.5L40.3%2041.7L43.7%2041.7L46.9%2040.5L49.5%2038.3L51.2%2035.4L51.8%2032.4ZM26.9%2018.7L12.7%2010.2L13.7%2011.6L13.5%2013.4L12%2014.4L10.3%2014.1L24.5%2022.7L26.2%2022.9L27.7%2021.9L27.9%2020.1ZM34.6%2011.8L26.5%20-2.6L26.7%20-0.9L25.6%200.5L23.9%200.7L22.5%20-0.4L30.6%2014L30.4%2012.3L31.5%2010.9L33.2%2010.7ZM44.3%2010.1L44.3%20-6.5L43.6%20-4.9L42%20-4.2L40.4%20-4.9L39.7%20-6.5L39.7%2010.1L40.4%2011.7L42%2012.3L43.6%2011.7ZM53.4%2014L61.5%20-0.4L61.7%20-2.1L60.7%20-3.5L58.9%20-3.7L57.5%20-2.6L49.4%2011.8L50.8%2010.7L52.5%2010.9L53.6%2012.3ZM59.5%2022.7L73.7%2014.1L74.7%2012.7L74.5%2011L73.1%209.9L71.3%2010.2L57.1%2018.7L58.9%2018.5L60.3%2019.5L60.6%2021.3ZM31.2%2093.5L31.6%2091.8L32.2%2090.5L33.2%2089.2L34.5%2088.1L36%2087.2L37.6%2086.6L39.4%2086.2L41.2%2086.1L43%2086.3L44.6%2086.7L46.1%2087.4L47.3%2088.2L48.2%2089.1L48.9%2090.1L49.2%2091.1L49.4%2092.1L49.3%2093L49%2093.9L48.5%2094.7L47.7%2095.5L46.8%2096.2L45.8%2096.7L44.6%2097L43.4%2097.2L42.2%2097.1L41.2%2096.9L40.2%2096.6L39.4%2096.1L38.8%2095.6L38.3%2095L38.1%2094.5L38%2094.1L38%2093.6L38.1%2093.2L38.3%2092.8L38.6%2092.4L39%2092.1L39.5%2091.8L40.1%2091.6L40.7%2091.5L41.2%2091.5L41.7%2091.6L42.1%2091.8L42.5%2091.9L42.6%2092.1L42.7%2092.2L42.8%2092.2L42.7%2092.2L42.7%2092.1L42.7%2092.3L42.8%2094.1L44.2%2095.2L45.9%2095L47.1%2093.7L47.3%2092.9L47.3%2091.9L47.1%2090.8L46.6%2089.7L45.9%2088.9L45%2088.1L44%2087.5L42.8%2087.2L41.6%2087L40.3%2087L39%2087.2L37.7%2087.6L36.5%2088.3L35.4%2089.1L34.5%2090.3L33.8%2091.6L33.4%2093L33.4%2094.6L33.8%2096.1L34.5%2097.5L35.5%2098.8L36.8%2099.9L38.3%20100.7L39.9%20101.3L41.8%20101.7L43.6%20101.7L45.5%20101.5L47.4%20101L49.2%20100.1L50.7%2099L52.1%2097.5L53.1%2095.9L53.8%2094L54%2092L53.7%2089.9L52.9%2088L51.8%2086.2L50.2%2084.6L48.3%2083.4L46.2%2082.4L43.8%2081.8L41.3%2081.5L38.8%2081.7L36.3%2082.2L34%2083.1L31.8%2084.4L29.9%2086.1L28.3%2088.1L27.3%2090.3L26.8%2092.5L27.8%2091.1L29.5%2090.8L30.9%2091.8Z%27%2F%3E%3Cpath%20d%3D%27M45.6%2032L45.2%2033.7L44%2035L42.4%2035.6L40.7%2035.4L39.3%2034.4L38.5%2032.9L38.5%2031.1L39.3%2029.6L40.7%2028.6L42.4%2028.4L44%2029L45.2%2030.3Z%27%2F%3E%3C%2Fsvg%3E") center 38% / auto 72% no-repeat,
    rgba(11, 11, 11, 0.55);
  animation: blink 2.6s ease-in-out infinite;
}

@keyframes blink {
  0%, 46%, 100% { opacity: 1; }
  50% { opacity: 0.45; }
}

.badge {
  position: absolute;
  top: 0.5rem;
  left: 0.5rem;
  padding: 0.15rem 0.45rem;
  font-size: 0.68rem;
  font-weight: 600;
  color: #ddd;
  background: rgba(0, 0, 0, 0.75);
  border-radius: 4px;
}

.glyph {
  width: 2rem;
  height: 2rem;
  opacity: 0.7;
}

.label {
  margin: 0;
  font-size: 0.85rem;
  font-weight: 600;
  color: #bbb;
}

.placeholder.error .label {
  color: #f55;
}

.hint {
  margin: 0;
  font-size: 0.72rem;
  color: #777;
}

.connecting .label {
  animation: pulse 1.4s ease-in-out infinite;
}

@keyframes pulse {
  0%,
  100% {
    opacity: 0.45;
  }
  50% {
    opacity: 1;
  }
}

@media (prefers-reduced-motion: reduce) {
  .connecting .label {
    animation: none;
  }
}

.retry {
  margin-top: 0.35rem;
  padding: 0.3rem 0.7rem;
  font: inherit;
  font-size: 0.75rem;
  color: #eee;
  background: #222;
  border: 1px solid #333;
  border-radius: 6px;
  cursor: pointer;
}
.retry:hover {
  border-color: #2a7;
  color: #2a7;
}
</style>
