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
