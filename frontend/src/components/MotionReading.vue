<script setup lang="ts">
import { computed } from 'vue'
import type { CameraStatus } from '@/types'

const props = defineProps<{ status?: CameraStatus; unavailable?: boolean }>()

const text = computed(() => {
  if (!props.status) return props.unavailable ? 'no status' : '--/--%'
  return `${props.status.change}/${props.status.threshold}%`
})

/** Over threshold means the scene is moving enough to trigger a recording. */
const over = computed(
  () => !!props.status && props.status.change >= props.status.threshold && props.status.threshold > 0,
)
</script>

<template>
  <span
    class="motion"
    :class="{ over, unknown: !status }"
    title="Scene change against the trigger threshold"
  >
    {{ text }}
  </span>
</template>

<style scoped>
.motion {
  font-variant-numeric: tabular-nums;
  font-size: 0.75rem;
  color: #c7c7c7;
  white-space: nowrap;
}
.motion.over {
  color: #f55;
  font-weight: 600;
}
.motion.unknown {
  color: #6a6a6a;
}
</style>
