<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import CameraStream from '@/components/CameraStream.vue'
import StateDot from '@/components/StateDot.vue'
import MotionReading from '@/components/MotionReading.vue'
import ErrorBanner from '@/components/ErrorBanner.vue'
import { ApiError } from '@/api/client'
import { cameraState, useCameraStore, isOnline } from '@/composables/useCameraStore'
import { usePageVisible } from '@/composables/usePageVisible'
import type { CameraStatus } from '@/types'

const route = useRoute()
const router = useRouter()
const pageVisible = usePageVisible()
const { cameraById, statuses, statusErrors, listError, loaded, removeCamera } = useCameraStore()

const id = computed(() => String(route.params.id))
const camera = computed(() => cameraById.value.get(id.value))
const status = computed<CameraStatus | undefined>(() => statuses.value[id.value])
const online = computed(() => (camera.value ? isOnline(camera.value) : false))
const statusError = computed(() => statusErrors.value[id.value])
const state = computed(() => (camera.value ? cameraState(camera.value, status.value) : 'offline'))

const confirming = ref(false)
const removing = ref(false)
const removeError = ref<string | null>(null)

type Row = { label: string; value: string; tone?: 'good' | 'bad' }

const rows = computed<Row[]>(() => {
  const s = status.value
  if (!s) return []
  return [
    { label: 'Recording', value: s.active ? 'yes' : 'no', tone: s.active ? 'bad' : undefined },
    { label: 'Trigger', value: s.triggered ? 'motion' : 'manual or idle' },
    { label: 'Frames in clip', value: String(s.frames) },
    { label: 'Frame rate', value: `${s.fps.toFixed(1)} fps` },
    { label: 'Motion detection', value: s.motion ? 'enabled' : 'off' },
    { label: 'Armed', value: s.armed ? 'yes' : 'no', tone: s.armed ? 'good' : undefined },
    { label: 'Scene change', value: `${s.change}%` },
    { label: 'Threshold', value: `${s.threshold}%` },
    { label: 'Pre-trigger buffer', value: `${s.preFrames} frames, ${s.preSecs}s` },
  ]
})

async function remove() {
  removing.value = true
  removeError.value = null
  try {
    await removeCamera(id.value)
    await router.push('/')
  } catch (err) {
    removeError.value = err instanceof ApiError ? err.message : String(err)
    removing.value = false
    confirming.value = false
  }
}
</script>

<template>
  <section class="detail">
    <header class="head">
      <button type="button" class="ghost" @click="router.push('/')">Back to wall</button>
      <template v-if="camera">
        <StateDot :state="state" />
        <h1>{{ camera.name }}</h1>
        <span class="addr">{{ camera.address }}</span>
      </template>
    </header>

    <ErrorBanner v-if="listError" :message="listError" />

    <p v-if="!camera && !loaded" class="notice">Loading...</p>
    <p v-else-if="!camera" class="notice">
      No camera with id {{ id }}. It may have been removed.
    </p>

    <template v-else>
      <div class="stage">
        <CameraStream
          :camera-id="camera.id"
          :name="camera.name"
          :online="online"
          :active="pageVisible"
          fit="contain"
        />
      </div>

      <div class="panels">
        <div class="card">
          <h2>Status</h2>
          <p class="motion-line">
            Motion <MotionReading :status="status" :unavailable="!!statusError" />
          </p>

          <ErrorBanner v-if="statusError" :message="statusError" />
          <p v-else-if="!online" class="notice">Camera is offline.</p>
          <p v-else-if="rows.length === 0" class="notice">Waiting for the first status poll...</p>

          <dl v-else class="rows">
            <template v-for="row in rows" :key="row.label">
              <dt>{{ row.label }}</dt>
              <dd :class="row.tone">{{ row.value }}</dd>
            </template>
          </dl>
        </div>

        <div class="card">
          <h2>Remove</h2>
          <p class="note">
            Takes this camera off the wall. Recordings already on the camera are untouched.
          </p>

          <ErrorBanner v-if="removeError" :message="removeError" />

          <div v-if="!confirming">
            <button type="button" class="danger" @click="confirming = true">
              Remove camera
            </button>
          </div>
          <div v-else class="confirm">
            <p class="note">Remove {{ camera.name }}?</p>
            <div class="confirm-actions">
              <button type="button" class="danger" :disabled="removing" @click="remove">
                {{ removing ? 'Removing...' : 'Yes, remove' }}
              </button>
              <button type="button" class="ghost" :disabled="removing" @click="confirming = false">
                Cancel
              </button>
            </div>
          </div>
        </div>
      </div>
    </template>
  </section>
</template>

<style scoped>
.detail {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.head {
  display: flex;
  align-items: center;
  gap: 0.65rem;
  flex-wrap: wrap;
}

h1 {
  margin: 0;
  font-size: 1.15rem;
  font-weight: 600;
}

h2 {
  margin: 0 0 0.4rem;
  font-size: 0.95rem;
  font-weight: 600;
}

.addr {
  font-size: 0.8rem;
  color: #8a8a8a;
}

.stage {
  aspect-ratio: 4 / 3;
  max-height: 70vh;
  background: #0b0b0b;
  border: 1px solid #242424;
  border-radius: 10px;
  overflow: hidden;
}

.panels {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 0.9rem;
  align-items: start;
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

.motion-line {
  margin: 0;
  font-size: 0.8rem;
  color: #9a9a9a;
  display: flex;
  gap: 0.4rem;
  align-items: baseline;
}

.rows {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 0.3rem 1rem;
  margin: 0;
  font-size: 0.83rem;
}

.rows dt {
  color: #8a8a8a;
}

.rows dd {
  margin: 0;
  color: #eee;
  font-variant-numeric: tabular-nums;
}

.rows dd.good {
  color: #2a7;
}
.rows dd.bad {
  color: #f55;
}

.notice,
.note {
  margin: 0;
  font-size: 0.8rem;
  color: #8a8a8a;
}

.danger {
  align-self: flex-start;
  padding: 0.5rem 0.95rem;
  font: inherit;
  font-size: 0.83rem;
  font-weight: 600;
  color: #f55;
  background: transparent;
  border: 1px solid #5a2020;
  border-radius: 6px;
  cursor: pointer;
}
.danger:hover:not(:disabled) {
  background: #f55;
  color: #180a0a;
  border-color: #f55;
}
.danger:disabled {
  opacity: 0.55;
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
.ghost:hover:not(:disabled) {
  border-color: #2a7;
  color: #2a7;
}

.confirm-actions {
  display: flex;
  gap: 0.5rem;
  flex-wrap: wrap;
  margin-top: 0.4rem;
}
</style>
