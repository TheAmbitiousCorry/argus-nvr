<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ApiError, api, playsInVideoElement, recordingFileName, recordingFormat } from '@/api/client'
import { clockOf, formatBytes, formatDuration } from '@/composables/useRecordings'
import type { Recording, RecordingFrames } from '@/types'

const props = defineProps<{ recording: Recording; cameraName?: string }>()
const emit = defineEmits<{ close: [] }>()

/**
 * There are two players here, because there are two forms a recording is held
 * in and they have nothing in common.
 *
 * A transcoded recording is H.264 in MP4, which the browser decodes itself. It
 * gets a <video> pointed at the recording URL and everything below is left
 * alone: play, pause, seeking, buffering and a timeline, all of it the
 * browser's, all of it better than anything reconstructed from frame times.
 *
 * A recording that has not been transcoded is MJPEG inside AVI, which no
 * browser decodes: a <video> pointed at the download URL shows nothing at all.
 * The service replays one as multipart/x-mixed-replace instead, which an
 * ordinary <img> plays with no decoding in the page, the same way the live wall
 * works and the same way the camera's own playback page has always done it.
 * That is everything from here down, and it is unchanged: a half transcoded
 * archive must not be a half broken one.
 *
 * That leaves two things an <img> cannot do by itself, and this is how each is
 * covered:
 *
 * Position. The stream does not report where it has reached, so the scrubber is
 * advanced from the frame index against the clock, exactly as the firmware page
 * does. Same timestamps, so it tracks what is on screen.
 *
 * Pause and seek. There is no way to hold a multipart connection still, and no
 * single-frame route to fall back on. So pausing paints the frame on screen
 * into a canvas and drops the connection, and seeking while paused opens the
 * stream at the wanted frame with `speed=0`, keeps the first frame it sends and
 * drops the connection again. One frame over the wire per scrub, and nothing is
 * left streaming in the background.
 */
const SPEEDS = [0.5, 1, 2, 4]

/** Pointing the <img> at this is what actually ends a multipart connection. */
const BLANK = 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7'

const imgEl = ref<HTMLImageElement | null>(null)
const canvasEl = ref<HTMLCanvasElement | null>(null)

const index = ref<RecordingFrames | null>(null)
const indexError = ref<string | null>(null)
const streamError = ref<string | null>(null)
const frame = ref(0)
const playing = ref(false)
const speed = ref(1)
const imgSrc = ref<string>(BLANK)
const frozen = ref(false)

let timer: ReturnType<typeof setInterval> | null = null
let previewTimeout: ReturnType<typeof setTimeout> | null = null
let previewing = false
let startedAt = 0
let startFrame = 0
let indexRun = 0

const hasIndex = computed(() => (index.value?.times?.length ?? 0) > 1)
const frameCount = computed(
  () => index.value?.frames ?? props.recording.frames ?? 0,
)
const lastFrame = computed(() => Math.max(0, frameCount.value - 1))
const durationMs = computed(() => index.value?.durMs ?? props.recording.durMs)

/**
 * Whether the browser can play this one itself.
 *
 * `transcodedSince` is for the recording that was transcoded while this listing
 * was on screen. The service answers 415 for a replay of an MP4, and the right
 * thing to do with that answer is to play the recording rather than report it:
 * a half transcoded archive must not be a half broken one, and the page a
 * moment out of date is the normal state of a page.
 */
const transcodedSince = ref(false)
const asVideo = computed(() => playsInVideoElement(props.recording) || transcodedSince.value)

const title = computed(
  () => `${props.cameraName ?? props.recording.cameraId} - ${props.recording.day} ${clockOf(props.recording.at)}`,
)
const downloadUrl = computed(() =>
  api.recordingUrl(props.recording.cameraId, props.recording.day, props.recording.at),
)
const downloadName = computed(() => recordingFileName(props.recording, props.cameraName))
const downloadLabel = computed(() => `Download ${recordingFormat(props.recording).toUpperCase()}`)

/** Where a frame falls. Without an index, spread them evenly and say so. */
function timeAt(n: number): number {
  const times = index.value?.times
  if (times && times.length > 0) return times[Math.min(n, times.length - 1)] ?? 0
  if (lastFrame.value === 0) return 0
  return (durationMs.value * n) / lastFrame.value
}

const position = computed(() => {
  const at = (timeAt(frame.value) / 1000).toFixed(1)
  const whole = (durationMs.value / 1000).toFixed(1)
  if (!hasIndex.value) return `${whole}s`
  return `${at}s / ${whole}s   frame ${frame.value + 1} of ${frameCount.value}`
})

function stopTimer() {
  if (timer) clearInterval(timer)
  timer = null
}

function cancelPreview() {
  previewing = false
  if (previewTimeout) clearTimeout(previewTimeout)
  previewTimeout = null
}

/** Keep whatever is on screen as a still, so pausing does not blank the picture. */
function freeze(): void {
  const img = imgEl.value
  const canvas = canvasEl.value
  if (!img || !canvas || !img.naturalWidth) return
  canvas.width = img.naturalWidth
  canvas.height = img.naturalHeight
  const ctx = canvas.getContext('2d')
  if (!ctx) return
  ctx.drawImage(img, 0, 0)
  frozen.value = true
}

function tick() {
  const elapsed = (Date.now() - startedAt) * speed.value + timeAt(startFrame)
  let n = frame.value
  while (n < lastFrame.value && timeAt(n + 1) <= elapsed) n += 1
  frame.value = n
  if (n >= lastFrame.value) pause()
}

function startStream(from: number) {
  stopTimer()
  cancelPreview()
  streamError.value = null
  playing.value = true
  frozen.value = false
  startFrame = from
  startedAt = Date.now()
  imgSrc.value = api.recordingStreamUrl(
    props.recording.cameraId,
    props.recording.day,
    props.recording.at,
    { from, speed: speed.value, nonce: startedAt },
  )
  // Without an index there is nothing to advance a position against, so the
  // stream simply runs until the recording ends.
  if (hasIndex.value) timer = setInterval(tick, 100)
}

function play() {
  if (frame.value >= lastFrame.value) frame.value = 0
  startStream(frame.value)
}

function pause() {
  stopTimer()
  cancelPreview()
  playing.value = false
  freeze()
  imgSrc.value = BLANK
}

function toggle() {
  if (playing.value) pause()
  else play()
}

/** One frame at the wanted point, then let go of the connection. */
function preview(n: number) {
  stopTimer()
  cancelPreview()
  playing.value = false
  frame.value = n
  previewing = true
  frozen.value = false
  streamError.value = null
  imgSrc.value = api.recordingStreamUrl(
    props.recording.cameraId,
    props.recording.day,
    props.recording.at,
    { from: n, speed: 0, nonce: Date.now() },
  )
  // If nothing arrives, stop asking rather than leaving a stream at full speed
  // running behind a page that is not showing it.
  previewTimeout = setTimeout(() => {
    if (!previewing) return
    previewing = false
    imgSrc.value = BLANK
  }, 4000)
}

function onLoad() {
  if (!previewing) return
  cancelPreview()
  freeze()
  imgSrc.value = BLANK
}

function onError() {
  if (imgSrc.value === BLANK) return
  cancelPreview()
  stopTimer()
  playing.value = false
  imgSrc.value = BLANK
  streamError.value = 'The service did not replay this recording.'
}

function onScrubInput(event: Event) {
  const value = (event.target as HTMLInputElement).valueAsNumber
  if (playing.value) pause()
  frame.value = Number.isFinite(value) ? value : 0
}

function onScrubChange(event: Event) {
  const value = (event.target as HTMLInputElement).valueAsNumber
  preview(Number.isFinite(value) ? value : 0)
}

async function loadIndex() {
  const run = (indexRun += 1)
  index.value = null
  indexError.value = null
  try {
    const res = await api.recordingFrames(
      props.recording.cameraId,
      props.recording.day,
      props.recording.at,
    )
    if (run !== indexRun) return
    index.value = res?.times ? res : null
    if (!index.value) indexError.value = 'No frame index, so there is nothing to scrub along.'
  } catch (err) {
    if (run !== indexRun) return
    if (err instanceof ApiError && err.status === 415) {
      transcodedSince.value = true
      return
    }
    indexError.value =
      'No frame index for this recording, so it plays from the start and cannot be scrubbed.'
  }
}

watch(
  () => `${props.recording.cameraId}/${props.recording.day}/${props.recording.at}`,
  () => {
    stopTimer()
    cancelPreview()
    playing.value = false
    frozen.value = false
    frame.value = 0
    streamError.value = null
    indexError.value = null
    transcodedSince.value = false
    imgSrc.value = BLANK
    // A transcoded recording needs none of this. Asking for its frame index
    // would be one request to be answered with a 415, and opening a replay of
    // it is not a thing the service will do.
    if (asVideo.value) return
    void loadIndex().then(() => preview(0))
  },
  { immediate: true },
)

// The endpoint is told the speed when the connection opens, so changing it
// while something is playing means opening a new one where this one reached.
watch(speed, () => {
  if (playing.value) startStream(frame.value)
})

onBeforeUnmount(() => {
  stopTimer()
  cancelPreview()
  imgSrc.value = BLANK
})
</script>

<template>
  <div class="player">
    <header class="head">
      <h2>{{ title }}</h2>
      <span class="meta">
        {{ formatDuration(recording.durMs) }} - {{ formatBytes(recording.bytes) }}
        <span v-if="recording.source === 'service'" class="tag stand-in" title="Recorded by the service, because the camera could not. This is the only copy.">
          service
        </span>
      </span>
      <button type="button" class="ghost" @click="emit('close')">Close</button>
    </header>

    <!-- H.264: the browser decodes it, so the browser plays it. -->
    <div v-if="asVideo" class="screen">
      <video
        class="feed"
        data-testid="recording-video"
        controls
        playsinline
        preload="metadata"
        :src="downloadUrl"
      ></video>
    </div>

    <!-- MJPEG in AVI: nothing decodes it, so it is replayed frame by frame. -->
    <div v-else class="screen">
      <img
        ref="imgEl"
        class="feed"
        :class="{ hidden: frozen }"
        :src="imgSrc"
        :alt="`${title} playback`"
        @load="onLoad"
        @error="onError"
      />
      <canvas ref="canvasEl" class="feed still" :class="{ hidden: !frozen }"></canvas>
      <p v-if="streamError" class="overlay">{{ streamError }}</p>
    </div>

    <input
      v-if="!asVideo"
      class="scrub"
      type="range"
      min="0"
      :max="lastFrame"
      :value="frame"
      :disabled="!hasIndex"
      aria-label="Position in this recording"
      @input="onScrubInput"
      @change="onScrubChange"
    />

    <div class="controls">
      <template v-if="!asVideo">
        <button type="button" class="primary" @click="toggle">
          {{ playing ? 'Pause' : 'Play' }}
        </button>
        <span class="pos">{{ position }}</span>

        <label class="speed">
          Speed
          <select v-model.number="speed">
            <option v-for="choice in SPEEDS" :key="choice" :value="choice">{{ choice }}x</option>
          </select>
        </label>
      </template>
      <span v-else class="pos">{{ (durationMs / 1000).toFixed(1) }}s, H.264</span>

      <a class="ghost download" :href="downloadUrl" :download="downloadName">{{ downloadLabel }}</a>
    </div>

    <p v-if="indexError && !asVideo" class="note warn">{{ indexError }}</p>
  </div>
</template>

<style scoped>
.player {
  display: flex;
  flex-direction: column;
  gap: 0.6rem;
  padding: 0.9rem;
  background: #181818;
  border: 1px solid #242424;
  border-radius: 10px;
}

.head {
  display: flex;
  align-items: baseline;
  gap: 0.75rem;
  flex-wrap: wrap;
}

h2 {
  margin: 0;
  font-size: 0.95rem;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.meta {
  font-size: 0.76rem;
  color: #8a8a8a;
  font-variant-numeric: tabular-nums;
}

.tag {
  margin-left: 0.35rem;
  font-size: 0.68rem;
  padding: 0.05rem 0.35rem;
  border: 1px solid #2c2c2c;
  border-radius: 4px;
  color: #9a9a9a;
}
.tag.stand-in {
  color: #c90;
  border-color: #5a4520;
}

.head .ghost {
  margin-left: auto;
}

.screen {
  position: relative;
  display: grid;
  place-items: center;
  min-height: 220px;
  background: #000;
  border: 1px solid #242424;
  border-radius: 8px;
  overflow: hidden;
}

.feed {
  grid-area: 1 / 1;
  display: block;
  max-width: 100%;
  max-height: 62vh;
  height: auto;
}

.feed.hidden {
  visibility: hidden;
}

.overlay {
  position: absolute;
  inset: auto 0 0 0;
  margin: 0;
  padding: 0.4rem 0.6rem;
  font-size: 0.76rem;
  color: #f9c9c9;
  background: #241414cc;
}

.scrub {
  width: 100%;
  accent-color: #2a7;
}
.scrub:disabled {
  opacity: 0.4;
}

.controls {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  flex-wrap: wrap;
}

.pos {
  font-size: 0.78rem;
  color: #9a9a9a;
  font-variant-numeric: tabular-nums;
}

.speed {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  font-size: 0.78rem;
  color: #9a9a9a;
}

.speed select {
  font: inherit;
  font-size: 0.78rem;
  color: #c7c7c7;
  background: #111;
  border: 1px solid #242424;
  border-radius: 6px;
  padding: 0.2rem 0.3rem;
}

.primary {
  padding: 0.4rem 0.9rem;
  font: inherit;
  font-size: 0.82rem;
  font-weight: 600;
  color: #06120d;
  background: #2a7;
  border: none;
  border-radius: 6px;
  cursor: pointer;
}

.ghost {
  padding: 0.35rem 0.8rem;
  font: inherit;
  font-size: 0.8rem;
  color: #ccc;
  background: transparent;
  border: 1px solid #2c2c2c;
  border-radius: 6px;
  cursor: pointer;
  text-decoration: none;
}
.ghost:hover {
  border-color: #2a7;
  color: #2a7;
}

.download {
  margin-left: auto;
}

.note {
  margin: 0;
  font-size: 0.74rem;
  color: #8a8a8a;
}
.note.warn {
  color: #c90;
}
</style>
