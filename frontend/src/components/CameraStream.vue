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
  background: repeating-linear-gradient(
    45deg,
    #101010,
    #101010 10px,
    #141414 10px,
    #141414 20px
  );
}

.placeholder.queued,
.placeholder.connecting {
  background: rgba(11, 11, 11, 0.55);
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
