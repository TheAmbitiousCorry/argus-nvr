<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import CameraTile from '@/components/CameraTile.vue'
import ErrorBanner from '@/components/ErrorBanner.vue'
import StateDot from '@/components/StateDot.vue'
import MotionReading from '@/components/MotionReading.vue'
import { useCameraStore, cameraState, isOnline } from '@/composables/useCameraStore'
import { usePageVisible } from '@/composables/usePageVisible'
import { LAYOUTS, ROTATE_CHOICES, useWallLayout } from '@/composables/useWallLayout'
import { MAX_CONCURRENT_STREAMS, queuedCount, streamingCount } from '@/composables/useStreamSlot'

const { cameras, statuses, statusErrors, listError, loaded, refresh } = useCameraStore()
const pageVisible = usePageVisible()

const {
  layout,
  rotate,
  rotateMs,
  pinned,
  featured,
  remainder,
  moving,
  rotating,
  pick,
  release,
} = useWallLayout(cameras, statuses)

const expandedId = ref<string | null>(null)

const online = computed(() => cameras.value.filter(isOnline).length)
const recording = computed(
  () => cameras.value.filter((c) => statuses.value[c.id]?.active).length,
)
const pickedName = computed(
  () => cameras.value.find((c) => c.id === pinned.value)?.name ?? null,
)

/** The large feed keeps a stream even when every slot is already spoken for. */
function priorityFor(id: string): number {
  return featured.value.some((c) => c.id === id) ? 2 : 1
}

function expand(id: string) {
  expandedId.value = expandedId.value === id ? null : id
}

/**
 * In the grid a tile is a thing you open. In the other layouts the small ones
 * are how you choose what the large slot shows, and only the large one opens.
 */
function onTileClick(id: string) {
  if (layout.value === 'grid' || featured.value.some((c) => c.id === id)) expand(id)
  else pick(id)
}

function onKey(event: KeyboardEvent) {
  if (event.key !== 'Escape') return
  if (expandedId.value) expandedId.value = null
  else if (pinned.value) release()
}

function seconds(ms: number): string {
  return `${Math.round(ms / 1000)}s`
}

// A camera removed elsewhere should not leave the wall stuck in an expanded
// view of nothing.
watch(cameras, (list) => {
  if (expandedId.value && !list.some((c) => c.id === expandedId.value)) expandedId.value = null
})

watch(expandedId, (id) => {
  document.body.classList.toggle('no-scroll', id !== null)
})

onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKey)
  document.body.classList.remove('no-scroll')
})
</script>

<template>
  <section class="wall" :class="`layout-${layout}`">
    <header class="head">
      <h1>Wall</h1>
      <p class="counts">
        <span>{{ online }}/{{ cameras.length }} online</span>
        <span v-if="recording" class="rec">{{ recording }} recording</span>
        <span v-if="moving.size" class="rec">{{ moving.size }} moving</span>
        <span v-if="!pageVisible" class="paused">streams paused, tab in background</span>
        <span v-else-if="queuedCount > 0" class="paused">
          {{ streamingCount }}/{{ MAX_CONCURRENT_STREAMS }} streams,
          {{ queuedCount }} on stills
        </span>
      </p>
    </header>

    <div class="controls">
      <div class="switch" role="group" aria-label="Wall layout">
        <button
          v-for="choice in LAYOUTS"
          :key="choice.id"
          type="button"
          class="seg"
          :class="{ on: layout === choice.id }"
          :aria-pressed="layout === choice.id"
          :title="choice.hint"
          @click="layout = choice.id"
        >
          {{ choice.label }}
        </button>
      </div>

      <template v-if="layout !== 'grid'">
        <button
          type="button"
          class="control"
          :class="{ off: !rotate }"
          :aria-pressed="rotate"
          :title="
            rotate ? 'Stop cycling through the quiet cameras' : 'Cycle through the quiet cameras'
          "
          @click="rotate = !rotate"
        >
          {{ rotate ? 'Pause cycle' : 'Resume cycle' }}
        </button>

        <label class="every">
          Every
          <select v-model.number="rotateMs" :disabled="!rotate">
            <option v-for="ms in ROTATE_CHOICES" :key="ms" :value="ms">{{ seconds(ms) }}</option>
          </select>
        </label>

        <span class="state">
          <template v-if="pinned">
            Holding {{ pickedName }}
            <button type="button" class="control" @click="release">Release</button>
          </template>
          <template v-else-if="moving.size">Following motion</template>
          <template v-else-if="!rotate">Cycle paused</template>
          <template v-else-if="rotating">Cycling</template>
          <template v-else>Steady</template>
        </span>
      </template>
    </div>

    <ErrorBanner
      v-if="listError"
      :message="listError"
      action-label="Retry"
      @action="refresh"
    />

    <p v-if="!loaded" class="notice">Loading cameras...</p>

    <p v-else-if="cameras.length === 0 && !listError" class="notice">
      No cameras yet.
      <RouterLink to="/add">Add one</RouterLink>.
    </p>

    <div v-else-if="layout === 'grid'" class="grid">
      <CameraTile
        v-for="camera in cameras"
        :key="camera.id"
        :camera="camera"
        :status="statuses[camera.id]"
        :status-error="statusErrors[camera.id]"
        :expanded="expandedId === camera.id"
        :allow-stream="expandedId === null || expandedId === camera.id"
        :page-visible="pageVisible"
        :moving="moving.has(camera.id)"
        slot-name="grid"
        @toggle="onTileClick"
      />
    </div>

    <div v-else class="stack">
      <div class="stage">
        <CameraTile
          v-for="camera in featured"
          :key="camera.id"
          :camera="camera"
          :status="statuses[camera.id]"
          :status-error="statusErrors[camera.id]"
          :expanded="expandedId === camera.id"
          :allow-stream="expandedId === null || expandedId === camera.id"
          :page-visible="pageVisible"
          :moving="moving.has(camera.id)"
          :picked="pinned === camera.id"
          frame="fill"
          slot-name="stage"
          :priority="priorityFor(camera.id)"
          @toggle="onTileClick"
        />
      </div>

      <!-- The focus layout keeps the rest watchable at a glance. They ask for
           streams quietly, so a busy wall leaves them on snapshots. -->
      <div v-if="layout === 'focus' && remainder.length" class="strip">
        <CameraTile
          v-for="camera in remainder"
          :key="camera.id"
          class="small"
          :camera="camera"
          :status="statuses[camera.id]"
          :status-error="statusErrors[camera.id]"
          :expanded="expandedId === camera.id"
          :allow-stream="expandedId === null || expandedId === camera.id"
          :page-visible="pageVisible"
          :moving="moving.has(camera.id)"
          slot-name="strip"
          :priority="priorityFor(camera.id)"
          @toggle="onTileClick"
        />
      </div>

      <!-- Single feed: the others are named rather than shown, because the
           whole point of the layout is that nothing else holds a connection. -->
      <div v-else-if="layout === 'single' && remainder.length" class="rail">
        <button
          v-for="camera in remainder"
          :key="camera.id"
          type="button"
          class="chip"
          :class="{ moving: moving.has(camera.id) }"
          :data-camera="camera.id"
          data-slot="rail"
          :title="`Show ${camera.name}`"
          @click="pick(camera.id)"
        >
          <StateDot :state="cameraState(camera, statuses[camera.id])" />
          <span class="chip-name">{{ camera.name }}</span>
          <MotionReading :status="statuses[camera.id]" :unavailable="!!statusErrors[camera.id]" />
        </button>
      </div>
    </div>

    <div v-if="expandedId" class="backdrop" @click="expandedId = null"></div>
  </section>
</template>

<style scoped>
.wall {
  display: flex;
  flex-direction: column;
  gap: 1rem;
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
}

.counts .rec {
  color: #f55;
}

.counts .paused {
  color: #8a8a4a;
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

.switch {
  display: inline-flex;
  border: 1px solid #242424;
  border-radius: 7px;
  overflow: hidden;
}

.seg {
  padding: 0.3rem 0.7rem;
  font: inherit;
  color: #9a9a9a;
  background: #181818;
  border: none;
  border-right: 1px solid #242424;
  cursor: pointer;
}
.seg:last-child {
  border-right: none;
}
.seg:hover {
  color: #eee;
}
.seg.on {
  color: #0b0b0b;
  background: #2a7;
  font-weight: 600;
}

.control {
  padding: 0.3rem 0.7rem;
  font: inherit;
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
.control.off {
  color: #8a8a4a;
}

.every {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
}

.every select {
  font: inherit;
  color: #c7c7c7;
  background: #181818;
  border: 1px solid #242424;
  border-radius: 7px;
  padding: 0.25rem 0.35rem;
}
.every select:disabled {
  opacity: 0.5;
}

.state {
  display: inline-flex;
  align-items: center;
  gap: 0.5rem;
  margin-left: auto;
  color: #8a8a8a;
}

.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 0.75rem;
}

/*
 * The large feed takes the height that is left after the chrome, and the strip
 * takes what it needs. A minimum keeps the feed watchable on a short window
 * even if that means the page scrolls.
 */
.stack {
  display: grid;
  grid-template-rows: minmax(260px, 1fr) auto;
  gap: 0.75rem;
  height: calc(100dvh - 13rem);
  min-height: 400px;
}

.stage {
  min-height: 0;
}

.strip {
  display: flex;
  gap: 0.6rem;
  overflow-x: auto;
  padding-bottom: 0.2rem;
  scrollbar-width: thin;
}

/*
 * The small feeds share the width rather than each claiming a fixed slice, so
 * a wall of eight fits without scrolling. Past the minimum they stop shrinking
 * and the strip scrolls instead, because a tile too small to recognise is not
 * worth keeping on screen.
 */
.strip .small {
  flex: 1 1 0;
  min-width: 132px;
  max-width: 240px;
}

.rail {
  display: flex;
  gap: 0.5rem;
  flex-wrap: wrap;
}

.chip {
  display: inline-flex;
  align-items: center;
  gap: 0.45rem;
  padding: 0.35rem 0.6rem;
  font: inherit;
  font-size: 0.8rem;
  color: #c7c7c7;
  background: #181818;
  border: 1px solid #242424;
  border-radius: 7px;
  cursor: pointer;
}
.chip:hover {
  border-color: #2a7;
}
.chip.moving {
  border-color: #f55;
}

.chip-name {
  font-weight: 600;
  color: #eee;
}

.notice {
  margin: 0;
  color: #9a9a9a;
  font-size: 0.9rem;
}

.notice a {
  color: #2a7;
}

.backdrop {
  position: fixed;
  inset: 0;
  z-index: 40;
  background: #000;
}

@media (max-width: 700px) {
  .grid {
    grid-template-columns: 1fr;
  }

  .stack {
    height: auto;
    min-height: 0;
    grid-template-rows: minmax(220px, 60vh) auto;
  }

  .state {
    margin-left: 0;
  }
}
</style>
