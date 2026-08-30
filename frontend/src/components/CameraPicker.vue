<script setup lang="ts">
import { computed } from 'vue'
import StateDot from '@/components/StateDot.vue'
import { cameraFirmware, cameraState, isOnline } from '@/composables/useCameraStore'
import type { Camera, CameraStatus } from '@/types'

const props = defineProps<{
  cameras: Camera[]
  statuses: Record<string, CameraStatus>
  modelValue: string[]
  /** Shown under a camera's name: a config load error, a flash result, whatever the view knows. */
  detail?: Record<string, string>
  /** Adds a line under the address with what each camera is currently running. */
  showFirmware?: boolean
}>()

const emit = defineEmits<{ 'update:modelValue': [string[]] }>()

const selected = computed(() => new Set(props.modelValue))
const onlineIds = computed(() => props.cameras.filter(isOnline).map((c) => c.id))

function toggle(id: string) {
  const next = new Set(selected.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  // Keep the order the wall shows rather than the order of clicking.
  emit(
    'update:modelValue',
    props.cameras.filter((c) => next.has(c.id)).map((c) => c.id),
  )
}

function setAll(ids: string[]) {
  emit('update:modelValue', ids)
}
</script>

<template>
  <div class="picker">
    <div class="bar">
      <span class="count">{{ modelValue.length }} of {{ cameras.length }} selected</span>
      <div class="bulk">
        <button type="button" class="link" @click="setAll(cameras.map((c) => c.id))">All</button>
        <button type="button" class="link" @click="setAll(onlineIds)">Online</button>
        <button type="button" class="link" @click="setAll([])">None</button>
      </div>
    </div>

    <p v-if="cameras.length === 0" class="empty">No cameras yet. Add one first.</p>

    <ul v-else class="list">
      <li v-for="cam in cameras" :key="cam.id" :class="{ picked: selected.has(cam.id) }">
        <label>
          <input
            type="checkbox"
            :checked="selected.has(cam.id)"
            :value="cam.id"
            @change="toggle(cam.id)"
          />
          <StateDot :state="cameraState(cam, statuses[cam.id])" />
          <span class="name">{{ cam.name }}</span>
          <span class="addr">{{ cam.address }}</span>
          <span v-if="!isOnline(cam)" class="tag offline">offline</span>
          <span v-if="detail?.[cam.id]" class="tag">{{ detail[cam.id] }}</span>

          <!-- Under the address, because what a camera is running is a property
               of the box at that address rather than of the name we gave it. -->
          <span v-if="showFirmware" class="fw">
            <template v-if="cameraFirmware(cam)">
              <span class="ver">{{ cameraFirmware(cam)?.version || 'unnamed build' }}</span>
              <span v-if="cameraFirmware(cam)?.slot" class="fwbit">
                slot {{ cameraFirmware(cam)?.slot }}
              </span>
              <span v-if="cameraFirmware(cam)?.built" class="fwbit">
                built {{ cameraFirmware(cam)?.built }}
              </span>
              <!-- An image on trial goes back to the one before it the moment
                   the camera reboots, so it has to be visible before then. -->
              <span
                v-if="cameraFirmware(cam)?.onTrial"
                class="fwtag trial"
                title="On trial: this image reverts to the previous one if the camera reboots before it is confirmed"
              >
                on trial
              </span>
              <span
                v-if="cameraFirmware(cam)?.rolledBackFrom"
                class="fwtag back"
                :title="`Rolled back from ${cameraFirmware(cam)?.rolledBackFrom}`"
              >
                rolled back from {{ cameraFirmware(cam)?.rolledBackFrom }}
              </span>
            </template>
            <span v-else class="fwbit">firmware unknown, the camera has not answered</span>
          </span>
        </label>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.picker {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  font-size: 0.78rem;
  color: #8a8a8a;
}

.bulk {
  display: flex;
  gap: 0.6rem;
}

.link {
  padding: 0;
  font: inherit;
  font-size: 0.78rem;
  color: #2a7;
  background: none;
  border: none;
  cursor: pointer;
}
.link:hover {
  text-decoration: underline;
}

.list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 0.4rem;
}

.list li {
  background: #111;
  border: 1px solid #242424;
  border-radius: 8px;
}
.list li.picked {
  border-color: #2a7;
}

label {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.35rem 0.5rem;
  padding: 0.5rem 0.6rem;
  cursor: pointer;
  min-width: 0;
}

input[type='checkbox'] {
  accent-color: #2a7;
  flex: none;
  width: 1rem;
  height: 1rem;
}

.name {
  font-size: 0.85rem;
  color: #eee;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.addr {
  font-size: 0.72rem;
  color: #7a7a7a;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tag {
  margin-left: auto;
  flex: none;
  font-size: 0.68rem;
  color: #9a9a9a;
  padding: 0.05rem 0.35rem;
  border: 1px solid #2c2c2c;
  border-radius: 4px;
}
.tag.offline {
  color: #f55;
  border-color: #5a2020;
}

.fw {
  flex: 0 0 100%;
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.3rem 0.45rem;
  margin-left: 1.5rem;
  font-size: 0.7rem;
  color: #7a7a7a;
}

.ver {
  color: #bbb;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

.fwbit {
  color: #6a6a6a;
}

.fwtag {
  font-size: 0.66rem;
  padding: 0.02rem 0.3rem;
  border-radius: 4px;
  border: 1px solid #5a4520;
  color: #c90;
}
.fwtag.back {
  border-color: #5a2020;
  color: #f55;
}

.empty {
  margin: 0;
  font-size: 0.8rem;
  color: #8a8a8a;
}
</style>
