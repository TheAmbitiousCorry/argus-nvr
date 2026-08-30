<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import ErrorBanner from '@/components/ErrorBanner.vue'
import RecordingPlayer from '@/components/RecordingPlayer.vue'
import { recordingFileName } from '@/api/client'
import { useCameraStore } from '@/composables/useCameraStore'
import {
  clockOf,
  formatBytes,
  formatDuration,
  useRecordings,
  weekdayOf,
} from '@/composables/useRecordings'
import type { Recording } from '@/types'

const { cameraById } = useCameraStore()
const {
  cameraId,
  day,
  recordings,
  cameraOptions,
  dayOptions,
  heldCount,
  more,
  loading,
  loadingMore,
  loaded,
  error,
  daysError,
  refresh,
  loadMore,
  stop,
} = useRecordings()

const selected = ref<Recording | null>(null)

/**
 * A recording outlives the camera it came from: remove a camera and what it
 * recorded is still held, still listed, and still worth watching. So the name
 * is looked up rather than assumed, and the id stands in when there is nothing
 * to look up.
 */
function nameOf(id: string): string {
  return cameraById.value.get(id)?.name ?? id
}

function knownCamera(id: string): boolean {
  return cameraById.value.has(id)
}

const selectedKey = computed(() =>
  selected.value
    ? `${selected.value.cameraId}/${selected.value.day}/${selected.value.at}`
    : null,
)

function keyOf(rec: Recording): string {
  return `${rec.cameraId}/${rec.day}/${rec.at}`
}

function open(rec: Recording) {
  selected.value = keyOf(rec) === selectedKey.value ? null : rec
}

function downloadName(rec: Recording): string {
  return recordingFileName(rec, cameraById.value.get(rec.cameraId)?.name)
}

function downloadUrl(rec: Recording): string {
  return `/api/recordings/${encodeURIComponent(rec.cameraId)}/${encodeURIComponent(rec.day)}/${encodeURIComponent(rec.at)}`
}

// Filtering to something the open recording is not part of leaves a player
// above a table that does not contain it.
watch([cameraId, day], () => {
  if (!selected.value) return
  if (cameraId.value && selected.value.cameraId !== cameraId.value) selected.value = null
  else if (day.value && selected.value.day !== day.value) selected.value = null
})

onMounted(() => void refresh())
onBeforeUnmount(stop)
</script>

<template>
  <section class="files">
    <header class="head">
      <h1>Files</h1>
      <p class="counts">
        <span>{{ heldCount }} held</span>
        <span v-if="recordings.length < heldCount">{{ recordings.length }} listed</span>
        <span v-if="cameraOptions.length">
          {{ cameraOptions.length }} {{ cameraOptions.length === 1 ? 'camera' : 'cameras' }}
        </span>
      </p>
    </header>

    <div class="controls">
      <label class="filter">
        Camera
        <select v-model="cameraId">
          <option value="">All cameras</option>
          <option v-for="option in cameraOptions" :key="option.id" :value="option.id">
            {{ nameOf(option.id) }}{{ knownCamera(option.id) ? '' : ' (removed)' }} ({{
              option.count
            }})
          </option>
        </select>
      </label>

      <!-- Only days that hold something are offered: a calendar of mostly empty
           dates is a worse control than a short list of dates with footage. -->
      <label class="filter">
        Date
        <select v-model="day">
          <option value="">All dates</option>
          <option v-for="option in dayOptions" :key="option.day" :value="option.day">
            {{ option.day }} {{ weekdayOf(option.day) }} ({{ option.count }})
          </option>
        </select>
      </label>

      <button type="button" class="control" :disabled="loading" @click="refresh">
        {{ loading ? 'Loading...' : 'Refresh' }}
      </button>

      <button
        v-if="cameraId || day"
        type="button"
        class="control"
        @click="
          () => {
            cameraId = ''
            day = ''
          }
        "
      >
        Clear filters
      </button>
    </div>

    <ErrorBanner v-if="error" :message="error" action-label="Retry" @action="refresh" />
    <ErrorBanner v-else-if="daysError" :message="`Could not load the dates: ${daysError}`" />

    <RecordingPlayer
      v-if="selected"
      :recording="selected"
      :camera-name="nameOf(selected.cameraId)"
      @close="selected = null"
    />

    <p v-if="!loaded" class="notice">Loading recordings...</p>
    <p v-else-if="recordings.length === 0 && !error" class="notice">
      Nothing held
      <template v-if="cameraId || day">for that filter</template>
      <template v-else>yet. Recordings appear here as the service pulls them off the cards</template>.
    </p>

    <table v-else class="list">
      <thead>
        <tr>
          <th class="camera">Camera</th>
          <th class="date">Date</th>
          <th class="start">Start</th>
          <th class="num">Duration</th>
          <th class="num">Size</th>
          <th class="act"><span class="sr">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="rec in recordings"
          :key="keyOf(rec)"
          :class="{ on: keyOf(rec) === selectedKey }"
          @click="open(rec)"
        >
          <td class="camera">
            <span class="cname">{{ nameOf(rec.cameraId) }}</span>
            <span
              v-if="rec.source === 'service'"
              class="tag stand-in"
              title="Recorded by the service because the camera could not. This is the only copy."
            >
              service
            </span>
            <span v-if="!knownCamera(rec.cameraId)" class="tag" title="No camera by this id is on the wall any more">
              removed
            </span>
          </td>
          <td class="date">
            {{ rec.day }}
            <span class="weekday">{{ weekdayOf(rec.day) }}</span>
          </td>
          <td class="start">{{ clockOf(rec.at) }}</td>
          <td class="num">{{ formatDuration(rec.durMs) }}</td>
          <td class="num">{{ formatBytes(rec.bytes) }}</td>
          <td class="act" @click.stop>
            <button type="button" class="link" @click="open(rec)">
              {{ keyOf(rec) === selectedKey ? 'Close' : 'Play' }}
            </button>
            <a class="link" :href="downloadUrl(rec)" :download="downloadName(rec)">Download</a>
          </td>
        </tr>
      </tbody>
    </table>

    <div v-if="more" class="pager">
      <button type="button" class="control" :disabled="loadingMore" @click="loadMore">
        {{ loadingMore ? 'Loading...' : 'Load more' }}
      </button>
      <span class="note">{{ recordings.length }} of {{ heldCount }}</span>
    </div>
  </section>
</template>

<style scoped>
.files {
  display: flex;
  flex-direction: column;
  gap: 0.9rem;
}

.head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 0.75rem;
  flex-wrap: wrap;
}

h1 {
  margin: 0;
  font-size: 1.15rem;
  font-weight: 600;
}

.counts {
  margin: 0;
  display: flex;
  gap: 0.9rem;
  flex-wrap: wrap;
  font-size: 0.8rem;
  color: #9a9a9a;
  font-variant-numeric: tabular-nums;
}

.controls {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  flex-wrap: wrap;
  margin-top: -0.4rem;
  font-size: 0.78rem;
  color: #9a9a9a;
}

.filter {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
}

.filter select {
  font: inherit;
  color: #c7c7c7;
  background: #181818;
  border: 1px solid #242424;
  border-radius: 7px;
  padding: 0.25rem 0.35rem;
  max-width: 16rem;
}

.control {
  padding: 0.3rem 0.7rem;
  font: inherit;
  font-size: 0.78rem;
  color: #c7c7c7;
  background: #181818;
  border: 1px solid #242424;
  border-radius: 7px;
  cursor: pointer;
}
.control:hover {
  color: #2a7;
  border-color: #2a7;
}
.control:disabled {
  opacity: 0.5;
  cursor: default;
}

.list {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.8rem;
}

.list th {
  text-align: left;
  font-weight: 600;
  font-size: 0.72rem;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  color: #7a7a7a;
  padding: 0.35rem 0.5rem;
  border-bottom: 1px solid #242424;
}

.list th.num,
.list th.act {
  text-align: right;
}

.list td {
  padding: 0.4rem 0.5rem;
  border-bottom: 1px solid #1c1c1c;
  color: #c7c7c7;
  white-space: nowrap;
}

.list tbody tr {
  cursor: pointer;
}
.list tbody tr:hover td {
  background: #181818;
}
.list tbody tr.on td {
  background: #14231d;
  color: #eee;
}

.cname {
  color: #eee;
}

.weekday {
  color: #6a6a6a;
  margin-left: 0.3rem;
}

.num {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.act {
  text-align: right;
  width: 1%;
}

.act .link + .link {
  margin-left: 0.7rem;
}

.link {
  padding: 0;
  font: inherit;
  font-size: 0.78rem;
  color: #2a7;
  background: none;
  border: none;
  cursor: pointer;
  text-decoration: none;
}
.link:hover {
  text-decoration: underline;
}

.tag {
  margin-left: 0.35rem;
  font-size: 0.66rem;
  color: #9a9a9a;
  padding: 0.02rem 0.3rem;
  border: 1px solid #2c2c2c;
  border-radius: 4px;
}
.tag.stand-in {
  color: #c90;
  border-color: #5a4520;
}

.pager {
  display: flex;
  align-items: center;
  gap: 0.6rem;
}

.note {
  margin: 0;
  font-size: 0.76rem;
  color: #8a8a8a;
  font-variant-numeric: tabular-nums;
}

.notice {
  margin: 0;
  color: #9a9a9a;
  font-size: 0.9rem;
}

.sr {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
}

@media (max-width: 640px) {
  .list .weekday {
    display: none;
  }
}
</style>
