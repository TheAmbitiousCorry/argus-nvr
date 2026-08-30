<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, RouterView } from 'vue-router'
import { useCameraStore } from '@/composables/useCameraStore'

// Mounted once here so the whole app shares one status poll.
const { listError, loaded } = useCameraStore()

const backend = computed(() => {
  if (!loaded.value) return { label: 'connecting', tone: 'unknown' }
  if (listError.value) return { label: 'backend unreachable', tone: 'bad' }
  return { label: 'backend ok', tone: 'good' }
})
</script>

<template>
  <div class="shell">
    <nav class="nav">
      <RouterLink class="brand" to="/">
        <!-- Argus Panoptes, who watched with a hundred eyes and never closed
             more than half of them. One eye of him is a camera. -->
        <svg class="mark" viewBox="0 0 200 158" fill="none" stroke="currentColor"
             stroke-width="4.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d='M90.9 112.0Q42.2 93.7 30.0 75.4'/>
          <path d='M95.3 112.0Q70.3 83.6 64.0 55.1'/>
          <path d='M100.0 112.0Q100.0 79.4 100.0 46.8'/>
          <path d='M104.7 112.0Q129.7 83.6 136.0 55.1'/>
          <path d='M109.1 112.0Q157.8 93.7 170.0 75.4'/>
          <path d='M11.6 56.0Q30.0 36.0 48.4 56.0Q30.0 76.0 11.6 56.0Z'/>
          <circle cx='30.0' cy='56.0' r='5.2'/>
          <circle cx='30.0' cy='56.0' r='1.6' fill='currentColor'/>
          <path d='M17.5 46.8L5.5 38.7'/>
          <path d='M22.9 41.5L16.1 28.7'/>
          <path d='M30.0 39.6L30.0 25.1'/>
          <path d='M37.1 41.5L43.9 28.7'/>
          <path d='M42.5 46.8L54.5 38.7'/>
          <path d='M42.1 32.0Q64.0 8.2 85.9 32.0Q64.0 55.8 42.1 32.0Z'/>
          <circle cx='64.0' cy='32.0' r='6.2'/>
          <circle cx='64.0' cy='32.0' r='1.6' fill='currentColor'/>
          <path d='M49.8 21.5L35.9 12.1'/>
          <path d='M56.0 15.5L48.1 0.6'/>
          <path d='M64.0 13.3L64.0 -3.5'/>
          <path d='M72.0 15.5L79.9 0.6'/>
          <path d='M78.2 21.5L92.1 12.1'/>
          <path d='M74.6 20.0Q100.0 -7.5 125.4 20.0Q100.0 47.5 74.6 20.0Z'/>
          <circle cx='100.0' cy='20.0' r='7.2'/>
          <circle cx='100.0' cy='20.0' r='1.6' fill='currentColor'/>
          <path d='M84.1 8.2L68.2 -2.5'/>
          <path d='M91.0 1.4L82.0 -15.5'/>
          <path d='M100.0 -1.1L100.0 -20.2'/>
          <path d='M109.0 1.4L118.0 -15.5'/>
          <path d='M115.9 8.2L131.8 -2.5'/>
          <path d='M114.1 32.0Q136.0 8.2 157.9 32.0Q136.0 55.8 114.1 32.0Z'/>
          <circle cx='136.0' cy='32.0' r='6.2'/>
          <circle cx='136.0' cy='32.0' r='1.6' fill='currentColor'/>
          <path d='M121.8 21.5L107.9 12.1'/>
          <path d='M128.0 15.5L120.1 0.6'/>
          <path d='M136.0 13.3L136.0 -3.5'/>
          <path d='M144.0 15.5L151.9 0.6'/>
          <path d='M150.2 21.5L164.1 12.1'/>
          <path d='M151.6 56.0Q170.0 36.0 188.4 56.0Q170.0 76.0 151.6 56.0Z'/>
          <circle cx='170.0' cy='56.0' r='5.2'/>
          <circle cx='170.0' cy='56.0' r='1.6' fill='currentColor'/>
          <path d='M157.5 46.8L145.5 38.7'/>
          <path d='M162.9 41.5L156.1 28.7'/>
          <path d='M170.0 39.6L170.0 25.1'/>
          <path d='M177.1 41.5L183.9 28.7'/>
          <path d='M182.5 46.8L194.5 38.7'/>
          <path d='M100 113c-24 1-37 8-37 18s15 17 34 17 38-7 38-17-10-15-23-15-19 5-19 11 6 9 11 9 9-3 9-6-3-5-6-5'/>
        </svg>
        <span>Argus</span>
      </RouterLink>
      <div class="links">
        <RouterLink to="/">Wall</RouterLink>
        <RouterLink to="/add">Add camera</RouterLink>
        <RouterLink to="/settings">Settings</RouterLink>
        <RouterLink to="/firmware">Firmware</RouterLink>
      </div>
      <span class="health" :class="backend.tone" :title="listError ?? backend.label">
        <span class="health-dot" aria-hidden="true"></span>
        <span class="health-text">{{ backend.label }}</span>
      </span>
    </nav>

    <main class="main">
      <RouterView />
    </main>
  </div>
</template>

<style scoped>
.shell {
  min-height: 100dvh;
  display: flex;
  flex-direction: column;
}

.nav {
  position: sticky;
  top: 0;
  z-index: 30;
  display: flex;
  align-items: center;
  gap: 1rem;
  padding: 0.65rem 1rem;
  padding-left: max(1rem, env(safe-area-inset-left));
  padding-right: max(1rem, env(safe-area-inset-right));
  background: #141414;
  border-bottom: 1px solid #242424;
}

.brand {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  color: #eee;
  text-decoration: none;
}

.links {
  display: flex;
  gap: 0.9rem;
  flex: 1 1 auto;
}

.links a {
  font-size: 0.85rem;
  color: #9a9a9a;
  text-decoration: none;
  padding: 0.2rem 0;
  border-bottom: 2px solid transparent;
}

.links a:hover {
  color: #eee;
}

.links a.router-link-active {
  color: #2a7;
  border-bottom-color: #2a7;
}

.health {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  font-size: 0.75rem;
  color: #8a8a8a;
}

.health-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: #666;
}
.health.good .health-dot {
  background: #2a7;
}
.health.bad .health-dot {
  background: #f55;
}
.health.bad {
  color: #f55;
}

.main {
  flex: 1 1 auto;
  padding: 1rem;
  padding-left: max(1rem, env(safe-area-inset-left));
  padding-right: max(1rem, env(safe-area-inset-right));
  padding-bottom: max(1rem, env(safe-area-inset-bottom));
}

@media (max-width: 560px) {
  .health-text {
    display: none;
  }
  .nav {
    gap: 0.75rem;
  }
}
.brand .mark {
  flex: none;
  width: 26px;
  height: auto;
  color: #2a7;
}
</style>