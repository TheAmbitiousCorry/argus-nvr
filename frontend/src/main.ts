import { createApp } from 'vue'
import App from './App.vue'
import { router } from './router'
import './style.css'

createApp(App).use(router).mount('#app')

// Registered after load so it never competes with the first paint or the first
// stream. Only in a build: the dev server has no worker to serve, and one left
// registered from a previous run would answer requests the dev server should.
if ('serviceWorker' in navigator && import.meta.env.PROD) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {
      // An app that works is more important than one that installs. A worker
      // that will not register costs offline shell and nothing else.
    })
  })
}
