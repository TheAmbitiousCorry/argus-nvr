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
        <svg class="mark" viewBox="0 0 120 108" fill="none" stroke="currentColor"
             stroke-width="6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M60 6v5M50 9l3 5M70 9l-3 5" />
          <path d="M46 22c4-6 8-9 14-9s10 3 14 9c-4 6-8 9-14 9s-10-3-14-9z" />
          <circle cx="60" cy="22" r="5" /><circle cx="60" cy="22" r="1.8" fill="currentColor" />
          <path d="M60 31c0 12-5 16-5 28s5 14 5 22" />
          <path d="M24 24v4M15 27l3 4M33 27l-3 4" />
          <path d="M11 38c4-5 7-8 13-8s9 3 13 8c-4 5-7 8-13 8s-9-3-13-8z" />
          <circle cx="24" cy="38" r="4.5" /><circle cx="24" cy="38" r="1.6" fill="currentColor" />
          <path d="M24 46c0 12 6 15 10 24s2 14 8 18" />
          <path d="M96 24v4M87 27l3 4M105 27l-3 4" />
          <path d="M83 38c4-5 7-8 13-8s9 3 13 8c-4 5-7 8-13 8s-9-3-13-8z" />
          <circle cx="96" cy="38" r="4.5" /><circle cx="96" cy="38" r="1.6" fill="currentColor" />
          <path d="M96 46c0 12-6 15-10 24s-2 14-8 18" />
          <path d="M60 79c14 0 24 6 24 14s-10 14-24 14-24-6-24-14 9-11 18-11 14 4 14 9-4 6-8 6-6-2-6-4" />
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