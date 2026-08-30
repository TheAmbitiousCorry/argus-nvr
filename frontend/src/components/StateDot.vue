<script setup lang="ts">
import type { CameraState } from '@/types'

const props = defineProps<{ state: CameraState; label?: string }>()

const text: Record<CameraState, string> = {
  recording: 'Recording',
  watching: 'Watching',
  offline: 'Offline',
}
const title = () => props.label ?? text[props.state]
</script>

<template>
  <span class="dot-wrap" :title="title()">
    <span class="dot" :class="state" aria-hidden="true"></span>
    <span class="sr-only">{{ title() }}</span>
  </span>
</template>

<style scoped>
.dot-wrap {
  display: inline-flex;
  align-items: center;
  flex: none;
}

.dot {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: #666;
  box-shadow: 0 0 0 2px rgba(0, 0, 0, 0.5);
}

.dot.recording {
  background: #f55;
  animation: blink 1.6s ease-in-out infinite;
}
.dot.watching {
  background: #2a7;
}
.dot.offline {
  background: #555;
}

@keyframes blink {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.5;
  }
}

@media (prefers-reduced-motion: reduce) {
  .dot.recording {
    animation: none;
  }
}

.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
</style>
