import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import WallView from '@/views/WallView.vue'

const routes: RouteRecordRaw[] = [
  { path: '/', name: 'wall', component: WallView },
  {
    path: '/add',
    name: 'add',
    component: () => import('@/views/AddCameraView.vue'),
  },
  {
    path: '/settings',
    name: 'settings',
    component: () => import('@/views/SettingsView.vue'),
  },
  {
    path: '/firmware',
    name: 'firmware',
    component: () => import('@/views/FirmwareView.vue'),
  },
  {
    path: '/camera/:id',
    name: 'camera',
    component: () => import('@/views/CameraDetailView.vue'),
  },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior: () => ({ top: 0 }),
})

/**
 * Every view but the wall is loaded on demand, from a file whose name carries a
 * hash of its contents. Deploying changes those names, so a tab left open since
 * before a deploy is holding a list of files that no longer exist: the moment
 * someone navigates, the fetch 404s and the page goes blank with nothing said.
 *
 * A reload fixes it, because it fetches the new index and with it the new
 * names. Doing that automatically is the whole of the fix. The guard on
 * `reloaded` is what stops a genuinely missing chunk turning into a reload loop:
 * one attempt, and if the second load fails too the error is left alone to be
 * seen rather than hidden behind a refresh.
 */
router.onError((error, to) => {
  const message = String((error as Error)?.message ?? error)
  const stale = /dynamically imported module|Importing a module script failed|Failed to fetch/i
  if (!stale.test(message)) return

  const key = 'argus.reloaded-for'
  try {
    if (sessionStorage.getItem(key) === to.fullPath) return
    sessionStorage.setItem(key, to.fullPath)
  } catch {
    // Private browsing refuses storage. Reloading once without the guard is
    // still better than a blank page.
  }
  window.location.assign(to.fullPath)
})
