<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import CameraPicker from '@/components/CameraPicker.vue'
import ErrorBanner from '@/components/ErrorBanner.vue'
import { ApiError, uploadFirmware } from '@/api/client'
import { cameraFirmware, isOnline, useCameraStore } from '@/composables/useCameraStore'

const { cameras, statuses, loaded } = useCameraStore()

type Phase = 'pending' | 'uploading' | 'flashing' | 'ok' | 'failed' | 'skipped'

interface CameraProgress {
  phase: Phase
  /** 0 to 1 while the image is on its way to the backend. */
  fraction: number
  message?: string
  bytes?: number
}

const PHASE_TEXT: Record<Phase, string> = {
  pending: 'waiting its turn',
  uploading: 'uploading',
  flashing: 'writing and rebooting',
  ok: 'done',
  failed: 'failed',
  skipped: 'not attempted',
}

const file = ref<File | null>(null)
const selected = ref<string[]>([])
const progress = ref<Record<string, CameraProgress>>({})
const running = ref(false)
const runError = ref<string | null>(null)
const finishedAt = ref<string | null>(null)
let controller: AbortController | null = null

const fileSize = computed(() => {
  if (!file.value) return ''
  const kb = file.value.size / 1024
  return kb > 1024 ? `${(kb / 1024).toFixed(2)} MB` : `${kb.toFixed(0)} KB`
})

const looksLikeFirmware = computed(() => !!file.value?.name.toLowerCase().endsWith('.bin'))

const canFlash = computed(
  () => !!file.value && selected.value.length > 0 && !running.value && file.value.size > 0,
)

const offlineSelected = computed(() =>
  cameras.value.filter((c) => selected.value.includes(c.id) && !isOnline(c)),
)

/** What the fleet is running, so a camera left behind stands out from the list. */
const versions = computed(() => {
  const counts = new Map<string, number>()
  let unknown = 0
  for (const cam of cameras.value) {
    const version = cameraFirmware(cam)?.version
    if (version) counts.set(version, (counts.get(version) ?? 0) + 1)
    else unknown += 1
  }
  return { counts: [...counts].sort((a, b) => b[1] - a[1]), unknown }
})

const onTrial = computed(() => cameras.value.filter((c) => cameraFirmware(c)?.onTrial))
const rolledBack = computed(() => cameras.value.filter((c) => cameraFirmware(c)?.rolledBackFrom))

const summary = computed(() => {
  const values = Object.values(progress.value)
  if (values.length === 0) return null
  return {
    done: values.filter((v) => v.phase === 'ok').length,
    failed: values.filter((v) => v.phase === 'failed').length,
    skipped: values.filter((v) => v.phase === 'skipped').length,
    total: values.length,
  }
})

/** Shown inside the picker so the state is on the camera you clicked. */
const pickerDetail = computed(() => {
  const out: Record<string, string> = {}
  for (const [id, p] of Object.entries(progress.value)) {
    out[id] =
      p.phase === 'uploading' ? `uploading ${Math.round(p.fraction * 100)}%` : PHASE_TEXT[p.phase]
  }
  return out
})

function onFile(ev: Event) {
  const input = ev.target as HTMLInputElement
  file.value = input.files?.[0] ?? null
  progress.value = {}
  finishedAt.value = null
  runError.value = null
}

function set(id: string, patch: Partial<CameraProgress>) {
  progress.value = { ...progress.value, [id]: { ...progress.value[id], ...patch } as CameraProgress }
}

/**
 * One request per camera, in order, waiting for each to come back.
 *
 * The contract flashes sequentially because every camera reboots, so a batch in
 * flight would leave the fleet in an unknown state. Sending them one at a time
 * from here keeps that order and is also the only way to say which camera the
 * bytes on screen belong to. A failure stops the run rather than carrying on
 * into cameras nobody has looked at yet.
 */
async function flash() {
  if (!canFlash.value || !file.value) return
  running.value = true
  runError.value = null
  finishedAt.value = null
  controller = new AbortController()

  const ids = [...selected.value]
  progress.value = Object.fromEntries(
    ids.map((id) => [id, { phase: 'pending' as Phase, fraction: 0 }]),
  )

  let stopped = false
  for (const id of ids) {
    if (stopped || controller.signal.aborted) {
      set(id, { phase: 'skipped', fraction: 0 })
      continue
    }
    set(id, { phase: 'uploading', fraction: 0 })
    try {
      const res = await uploadFirmware({
        file: file.value,
        cameraIds: [id],
        signal: controller.signal,
        onProgress: (fraction) => {
          // Once the last byte is out of the browser the camera is writing it,
          // which is the part that takes the seconds and shows no progress.
          if (fraction >= 1) set(id, { phase: 'flashing', fraction: 1 })
          else set(id, { phase: 'uploading', fraction })
        },
      })
      const result = res?.results?.find((r) => r.cameraId === id) ?? res?.results?.[0]
      if (result?.ok) {
        set(id, { phase: 'ok', fraction: 1, bytes: result.bytes })
      } else {
        set(id, {
          phase: 'failed',
          fraction: 1,
          message: result?.error ?? 'the backend reported no result for this camera',
        })
        stopped = true
      }
    } catch (err) {
      const message = err instanceof ApiError ? err.message : String(err)
      set(id, { phase: 'failed', fraction: 0, message })
      stopped = true
      if (!(err instanceof ApiError) || err.status !== 0) runError.value = message
    }
  }

  running.value = false
  controller = null
  finishedAt.value = new Date().toLocaleTimeString()
}

function stop() {
  controller?.abort()
}

onBeforeUnmount(() => controller?.abort())

const nameOf = (id: string) => cameras.value.find((c) => c.id === id)?.name ?? id
const orderedProgress = computed(() =>
  Object.keys(progress.value).map((id) => ({ id, name: nameOf(id), ...progress.value[id] })),
)
</script>

<template>
  <section class="firmware">
    <header class="head">
      <h1>Firmware</h1>
      <p class="note">
        Sends one .bin to the cameras you pick. They are flashed one at a time, never together:
        each camera reboots when it is done, and a failure stops the run so the rest are left
        as they were rather than half updated.
      </p>
    </header>

    <div class="card">
      <h2>Image</h2>
      <label class="file">
        <span>Firmware file</span>
        <input type="file" accept=".bin,application/octet-stream" @change="onFile" />
      </label>
      <p v-if="file" class="chosen">
        <strong>{{ file.name }}</strong>
        <span class="size">{{ fileSize }}</span>
      </p>
      <p v-else class="note">No file chosen.</p>
      <p v-if="file && !looksLikeFirmware" class="warn">
        That is not a .bin. The camera will accept whatever it is sent and reboot into it.
      </p>
    </div>

    <div class="card">
      <h2>Cameras</h2>
      <p v-if="versions.counts.length || versions.unknown" class="note">
        Running:
        <template v-for="([version, count], i) in versions.counts" :key="version">
          <template v-if="i > 0">, </template>
          <span class="ver">{{ version }}</span> on {{ count }}
        </template>
        <template v-if="versions.unknown">
          <template v-if="versions.counts.length">, </template>
          {{ versions.unknown }} not answering
        </template>
      </p>

      <CameraPicker
        v-model="selected"
        :cameras="cameras"
        :statuses="statuses"
        :detail="pickerDetail"
        show-firmware
      />
      <p v-if="!loaded" class="note">Loading cameras...</p>
      <p v-else-if="offlineSelected.length > 0" class="warn">
        {{ offlineSelected.map((c) => c.name).join(', ') }}
        {{ offlineSelected.length === 1 ? 'is' : 'are' }} offline. Flashing one will fail and
        stop the run.
      </p>

      <!-- An image on trial is one reboot away from disappearing, and a
           rollback nobody noticed is a camera quietly running old code. Both
           are said here as well as under the address, because the point of them
           is being seen. -->
      <p v-if="onTrial.length" class="warn">
        {{ onTrial.map((c) => c.name).join(', ') }}
        {{ onTrial.length === 1 ? 'is' : 'are' }} running an image on trial. It reverts to the
        previous one on the next reboot unless the camera confirms it.
      </p>
      <p v-for="cam in rolledBack" :key="cam.id" class="warn">
        {{ cam.name }} rolled back from {{ cameraFirmware(cam)?.rolledBackFrom }} and is running
        {{ cameraFirmware(cam)?.version || 'the previous image' }}.
      </p>
    </div>

    <div class="card">
      <h2>Flash</h2>
      <p class="note">
        About seven seconds per camera on a local network, plus the reboot. Do not power a
        camera off while its row says it is writing.
      </p>

      <ErrorBanner v-if="runError" :message="runError" />

      <div class="action-row">
        <button type="button" class="primary" :disabled="!canFlash" @click="flash">
          {{
            running
              ? 'Flashing...'
              : `Flash ${selected.length} ${selected.length === 1 ? 'camera' : 'cameras'}`
          }}
        </button>
        <button v-if="running" type="button" class="ghost" @click="stop">
          Stop after this camera
        </button>
        <span v-if="summary" class="summary">
          {{ summary.done }} done, {{ summary.failed }} failed, {{ summary.skipped }} not
          attempted, of {{ summary.total }}
          <template v-if="finishedAt"> - finished {{ finishedAt }}</template>
        </span>
      </div>

      <ol v-if="orderedProgress.length > 0" class="progress">
        <li v-for="(row, i) in orderedProgress" :key="row.id" :class="row.phase">
          <span class="ord">{{ i + 1 }}</span>
          <span class="pname">{{ row.name }}</span>
          <span class="bar" :class="row.phase">
            <span
              class="fill"
              :style="{
                width: `${Math.round((row.phase === 'ok' || row.phase === 'failed' ? 1 : row.fraction) * 100)}%`,
              }"
            ></span>
          </span>
          <span class="phase">
            {{ row.phase === 'uploading' ? `uploading ${Math.round(row.fraction * 100)}%` : PHASE_TEXT[row.phase] }}
          </span>
          <span v-if="row.bytes !== undefined" class="detail">{{ row.bytes }} bytes</span>
          <span v-else-if="row.message" class="detail bad">{{ row.message }}</span>
        </li>
      </ol>
      <p v-else class="note">Nothing flashed yet.</p>
    </div>
  </section>
</template>

<style scoped>
.firmware {
  display: flex;
  flex-direction: column;
  gap: 0.9rem;
}

.head {
  display: flex;
  flex-direction: column;
  gap: 0.2rem;
}

h1 {
  margin: 0;
  font-size: 1.15rem;
  font-weight: 600;
}

h2 {
  margin: 0;
  font-size: 0.95rem;
  font-weight: 600;
}

.card {
  display: flex;
  flex-direction: column;
  gap: 0.6rem;
  padding: 1rem;
  background: #181818;
  border: 1px solid #242424;
  border-radius: 10px;
}

.note {
  margin: 0;
  font-size: 0.76rem;
  color: #8a8a8a;
}

.warn {
  margin: 0;
  font-size: 0.76rem;
  color: #c90;
}

.file {
  display: flex;
  flex-direction: column;
  gap: 0.3rem;
  font-size: 0.8rem;
  color: #bbb;
}

input[type='file'] {
  font: inherit;
  font-size: 0.8rem;
  color: #ccc;
  padding: 0.5rem;
  background: #111;
  border: 1px dashed #2c2c2c;
  border-radius: 6px;
}

.ver {
  color: #bbb;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

.chosen {
  margin: 0;
  display: flex;
  gap: 0.6rem;
  align-items: baseline;
  font-size: 0.83rem;
  color: #eee;
}

.size {
  font-size: 0.75rem;
  color: #8a8a8a;
  font-variant-numeric: tabular-nums;
}

.action-row {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  flex-wrap: wrap;
}

.summary {
  font-size: 0.76rem;
  color: #9a9a9a;
}

.primary {
  padding: 0.5rem 0.95rem;
  font: inherit;
  font-size: 0.85rem;
  font-weight: 600;
  color: #06120d;
  background: #2a7;
  border: none;
  border-radius: 6px;
  cursor: pointer;
}
.primary:disabled {
  opacity: 0.45;
  cursor: default;
}

.ghost {
  padding: 0.45rem 0.85rem;
  font: inherit;
  font-size: 0.82rem;
  color: #ccc;
  background: transparent;
  border: 1px solid #2c2c2c;
  border-radius: 6px;
  cursor: pointer;
}
.ghost:hover {
  border-color: #2a7;
  color: #2a7;
}

.progress {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 0.35rem;
  counter-reset: none;
}

.progress li {
  display: grid;
  grid-template-columns: 1.4rem minmax(6rem, 10rem) minmax(4rem, 1fr) auto;
  align-items: center;
  gap: 0.5rem;
  padding: 0.4rem 0.6rem;
  font-size: 0.78rem;
  background: #111;
  border: 1px solid #242424;
  border-left: 2px solid #444;
  border-radius: 6px;
}
.progress li.ok {
  border-left-color: #2a7;
}
.progress li.failed {
  border-left-color: #f55;
}
.progress li.uploading,
.progress li.flashing {
  border-left-color: #c90;
}

.ord {
  color: #6a6a6a;
  font-variant-numeric: tabular-nums;
}

.pname {
  color: #eee;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.bar {
  height: 6px;
  background: #1e1e1e;
  border-radius: 3px;
  overflow: hidden;
}

.fill {
  display: block;
  height: 100%;
  background: #c90;
  transition: width 0.15s linear;
}
.bar.ok .fill {
  background: #2a7;
}
.bar.failed .fill {
  background: #f55;
}
.bar.pending .fill,
.bar.skipped .fill {
  background: #444;
}

.phase {
  color: #9a9a9a;
  white-space: nowrap;
}

.detail {
  grid-column: 2 / -1;
  font-size: 0.72rem;
  color: #7a7a7a;
  font-variant-numeric: tabular-nums;
}
.detail.bad {
  color: #f55;
}
</style>
