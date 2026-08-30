// Argus service worker.
//
// This app is a window onto live cameras, so almost nothing here should be
// cached: a stream never ends, a snapshot is stale the moment it arrives, and a
// status document that came from a cache is a lie about a camera. What is
// cached is the shell, so opening the app on a phone that has lost the network
// shows the interface saying the backend is unreachable rather than the
// browser's own error page.
//
// The version is part of the cache name. Changing it is what retires the old
// one, and the old one is deleted on activate rather than left to accumulate.
const VERSION = 'argus-v2'
// The shell is precached so the app opens with no network. '/' is in here
// because it is what an offline visit needs, but it is also the file that names
// every hashed chunk, so it is only ever served from the cache when the network
// could not answer at all. Anything fetched from the cache while the network is
// up would hand a tab filenames that no longer exist.
const SHELL = ['/', '/manifest.webmanifest', '/argus-mark.svg', '/argus-eye.svg']

self.addEventListener('install', (event) => {
  // Take over immediately rather than waiting for every tab to close. A stale
  // worker serving a stale shell is the failure this is most likely to cause.
  event.waitUntil(caches.open(VERSION).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()))
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((names) => Promise.all(names.filter((n) => n !== VERSION).map((n) => caches.delete(n))))
      .then(() => self.clients.claim()),
  )
})

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url)
  const sameOrigin = url.origin === self.location.origin

  // Anything live goes straight to the network, always. A cached stream would
  // be a still image that never updates, and a cached camera list would show
  // cameras that are no longer there.
  if (!sameOrigin || event.request.method !== 'GET' ||
      url.pathname.startsWith('/api/') || url.pathname === '/healthz') {
    return
  }

  // The shell: try the network first so a deployed change is picked up on the
  // next load, and fall back to the cache only when the network cannot answer.
  // Cache-first would pin someone to an old build until the cache was cleared.
  event.respondWith(
    fetch(event.request)
      .then((res) => {
        if (res && res.ok && res.type === 'basic') {
          const copy = res.clone()
          caches.open(VERSION).then((c) => c.put(event.request, copy))
        }
        return res
      })
      .catch(() =>
        caches.match(event.request).then((hit) => hit || caches.match('/')),
      ),
  )
})
