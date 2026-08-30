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
      <RouterLink class="brand" to="/">NVR</RouterLink>
      <div class="links">
        <RouterLink to="/">Wall</RouterLink>
        <RouterLink to="/add">Add camera</RouterLink>
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
</style>
