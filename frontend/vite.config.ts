import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The dev server stands in for the Go backend's static file handler, so the
// same relative /api paths work in `npm run dev` and in a production build.
const backend = process.env.NVR_BACKEND ?? 'http://localhost:8080'

const proxy = {
  '/api': {
    target: backend,
    changeOrigin: true,
    // MJPEG responses never end. No buffering, no timeout.
    timeout: 0,
    proxyTimeout: 0,
  },
  '/healthz': { target: backend, changeOrigin: true },
}

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: { port: 5173, proxy },
  // `npm run preview` checks the production bundle against the same backend.
  preview: { port: 4173, proxy },
  build: {
    outDir: 'dist',
    sourcemap: true,
  },
})
