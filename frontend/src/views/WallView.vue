<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import CameraTile from '@/components/CameraTile.vue'
import ErrorBanner from '@/components/ErrorBanner.vue'
import { useCameraStore, isOnline } from '@/composables/useCameraStore'
import { usePageVisible } from '@/composables/usePageVisible'
import { MAX_CONCURRENT_STREAMS, queuedCount, streamingCount } from '@/composables/useStreamSlot'

const { cameras, statuses, statusErrors, listError, loaded, refresh } = useCameraStore()
const pageVisible = usePageVisible()

const expandedId = ref<string | null>(null)

const online = computed(() => cameras.value.filter(isOnline).length)
const recording = computed(
  () => cameras.value.filter((c) => statuses.value[c.id]?.active).length,
)

function toggle(id: string) {
  expandedId.value = expandedId.value === id ? null : id
}

function onKey(event: KeyboardEvent) {
  if (event.key === 'Escape') expandedId.value = null
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
  <section class="wall">
    <header class="head">
      <h1>Wall</h1>
      <p class="counts">
        <span>{{ online }}/{{ cameras.length }} online</span>
        <span v-if="recording" class="rec">{{ recording }} recording</span>
        <span v-if="!pageVisible" class="paused">streams paused, tab in background</span>
        <span v-else-if="queuedCount > 0" class="paused">
          {{ streamingCount }}/{{ MAX_CONCURRENT_STREAMS }} streams,
          {{ queuedCount }} on stills
        </span>
      </p>
    </header>

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

    <div v-if="cameras.length" class="grid">
      <CameraTile
        v-for="camera in cameras"
        :key="camera.id"
        :camera="camera"
        :status="statuses[camera.id]"
        :status-error="statusErrors[camera.id]"
        :expanded="expandedId === camera.id"
        :allow-stream="expandedId === null || expandedId === camera.id"
        :page-visible="pageVisible"
        @toggle="toggle"
      />
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

.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 0.75rem;
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
}
</style>
