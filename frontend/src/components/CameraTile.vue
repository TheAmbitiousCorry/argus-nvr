<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink } from 'vue-router'
import CameraStream from './CameraStream.vue'
import StateDot from './StateDot.vue'
import MotionReading from './MotionReading.vue'
import { useIntersection } from '@/composables/useIntersection'
import { cameraState, isOnline } from '@/composables/useCameraStore'
import type { Camera, CameraStatus } from '@/types'

const props = withDefaults(
  defineProps<{
    camera: Camera
    status?: CameraStatus
    statusError?: string
    expanded: boolean
    /** False while another tile is expanded, so this one gives its stream back. */
    allowStream: boolean
    pageVisible: boolean
    /**
     * `grid` keeps the tile's own 4:3 shape. `fill` lets it take whatever room
     * the layout gives it, which is what the large slot needs.
     */
    frame?: 'grid' | 'fill'
    /** Where this tile sits, so the wall can be read back from the markup. */
    slotName?: string
    /** The scene is moving, or has moved recently enough to still be worth a look. */
    moving?: boolean
    /** The viewer picked this camera, and it stays picked until released. */
    picked?: boolean
    /** How loudly this tile asks for a stream rather than snapshots. */
    priority?: number
  }>(),
  { frame: 'grid', slotName: 'grid', priority: 0 },
)

const emit = defineEmits<{ toggle: [id: string] }>()

const root = ref<HTMLElement | null>(null)
const onScreen = useIntersection(root)

const state = computed(() => cameraState(props.camera, props.status))
const online = computed(() => isOnline(props.camera))
const active = computed(
  () => props.allowStream && props.pageVisible && (props.expanded || onScreen.value),
)

function toggle() {
  emit('toggle', props.camera.id)
}

function onKey(event: KeyboardEvent) {
  if (event.key === 'Enter' || event.key === ' ') {
    event.preventDefault()
    toggle()
  } else if (event.key === 'Escape' && props.expanded) {
    toggle()
  }
}
</script>

<template>
  <article
    ref="root"
    class="tile"
    :class="{ expanded, offline: !online, moving, picked, [`frame-${frame}`]: true }"
    role="button"
    tabindex="0"
    :data-camera="camera.id"
    :data-slot="slotName"
    :aria-pressed="expanded"
    :aria-label="`${camera.name}, ${state}. ${expanded ? 'Collapse' : 'Expand'}`"
    @click="toggle"
    @keydown="onKey"
  >
    <div class="video">
      <CameraStream
        :camera-id="camera.id"
        :name="camera.name"
        :online="online"
        :active="active"
        :fit="expanded || frame === 'fill' ? 'contain' : 'cover'"
        :priority="priority"
      />
    </div>

    <header class="bar">
      <StateDot :state="state" />
      <span class="name" :title="camera.address">{{ camera.name }}</span>
      <span v-if="picked" class="picked-tag">picked</span>
      <MotionReading :status="status" :unavailable="!!statusError" />
      <RouterLink
        class="detail-link"
        :to="{ name: 'camera', params: { id: camera.id } }"
        title="Camera detail"
        @click.stop
      >
        Details
      </RouterLink>
    </header>

    <button v-if="expanded" type="button" class="collapse" title="Collapse" @click.stop="toggle">
      Close
    </button>
  </article>
</template>

<style scoped>
.tile {
  position: relative;
  display: block;
  background: #181818;
  border: 1px solid #242424;
  border-radius: 10px;
  overflow: hidden;
  cursor: pointer;
  transition: border-color 0.15s ease;
}

.tile.frame-grid {
  aspect-ratio: 4 / 3;
}

/* Takes the room the layout hands it rather than dictating its own shape. */
.tile.frame-fill {
  height: 100%;
  min-height: 0;
}

.tile.moving {
  border-color: #f55;
}

.tile.picked {
  border-color: #2a7;
}

.picked-tag {
  flex: none;
  font-size: 0.68rem;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: #2a7;
}

.tile:hover,
.tile:focus-visible {
  border-color: #2a7;
  outline: none;
}

.tile.offline {
  border-color: #2a2a2a;
}

.video {
  position: absolute;
  inset: 0;
}

.bar {
  position: absolute;
  inset: auto 0 0 0;
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.45rem 0.6rem;
  /* Solid rather than a fade: a bright scene behind a gradient leaves the
     name and the motion reading unreadable. */
  background: rgba(0, 0, 0, 0.78);
  pointer-events: none;
}

/* Softens the hard top edge of the bar without weakening its contrast. */
.bar::before {
  content: '';
  position: absolute;
  inset: auto 0 100% 0;
  height: 2rem;
  background: linear-gradient(to top, rgba(0, 0, 0, 0.55), rgba(0, 0, 0, 0));
  pointer-events: none;
}

.bar > * {
  pointer-events: auto;
}

.name {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 0.85rem;
  font-weight: 600;
  color: #eee;
}

.detail-link {
  flex: none;
  font-size: 0.72rem;
  color: #c7c7c7;
  text-decoration: none;
  border-bottom: 1px solid transparent;
}
.detail-link:hover {
  color: #2a7;
  border-bottom-color: #2a7;
}

.tile.expanded {
  position: fixed;
  inset: 0;
  z-index: 50;
  aspect-ratio: auto;
  border-radius: 0;
  border: none;
  background: #0b0b0b;
  cursor: zoom-out;
}

.collapse {
  position: absolute;
  top: 0.75rem;
  right: 0.75rem;
  padding: 0.4rem 0.85rem;
  font: inherit;
  font-size: 0.8rem;
  color: #eee;
  background: rgba(24, 24, 24, 0.9);
  border: 1px solid #333;
  border-radius: 6px;
  cursor: pointer;
}
.collapse:hover {
  border-color: #2a7;
  color: #2a7;
}
</style>
