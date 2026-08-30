# NVR frontend

Vue 3 and TypeScript, built with Vite. It shows a wall of ESP32-CAM streams,
adds cameras (by address or from mDNS discovery), and gives each camera a
detail page.

It talks only to the Go backend. It never contacts a camera directly.

## Run it

```sh
npm install
npm run dev      # http://localhost:5173, proxies /api and /healthz to :8080
```

Point the proxy somewhere else with an environment variable:

```sh
NVR_BACKEND=http://192.168.10.5:8080 npm run dev
```

## Build it

```sh
npm run build    # type checks, then writes static files to dist/
npm run preview  # serves dist/ on :4173 with the same proxy, for a last look
```

`dist/` is plain static output. Serve it from the Go backend on the same origin
as the API, which is why every request in the code uses a relative `/api/...`
path. To point a build at a different origin, set `VITE_API_BASE` at build time.

### What the static handler has to do

Routing uses real paths, not hashes, so any GET that is not a file in `dist/`
and not an API route has to return `index.html`. Otherwise `/add` and
`/camera/<id>` work when you navigate to them but 404 on a refresh or a
bookmark. The Go backend's `spaHandler` already does this, and its Dockerfile
expects the build at `/app/web`, so copy `dist/` there.

## Why the code is careful about streams

A camera is a microcontroller with one radio. Every extra viewer takes real
frame rate off it, so the UI treats an open stream as something to be spent
rather than something to leave running.

- Each tile watches itself with an `IntersectionObserver`. Scroll a tile off
  screen and it drops its connection; scroll back and it reconnects.
- Expanding a tile stops every other stream on the wall. So does switching to
  another browser tab.
- At most four streams run at once (`MAX_CONCURRENT_STREAMS` in
  `src/composables/useStreamSlot.ts`). Browsers allow six connections per
  origin over HTTP/1.1 and an MJPEG stream holds one open forever, so an
  unlimited wall would starve the status polling. Tiles that are visible but
  cannot get a slot fall back to a snapshot every five seconds and say so.
- Status is polled every two seconds by one timer for the whole app, in
  `useCameraStore.ts`. Tiles never poll for themselves. Polling stops while the
  tab is in the background.
- The camera list refreshes every fifth poll, since it only changes when
  someone adds or removes a camera.

## Failure is visible, never a spinner that never ends

- An offline camera draws a placeholder saying so. No broken image icon.
- A stream that errors, or that opens but decodes no frame within ten seconds,
  shows "Stream failed" with the attempt count and a retry button, and retries
  on its own with a backoff from 2 up to 30 seconds.
- A failed status poll leaves the tile reading "no status" rather than a stale
  number.
- A backend that cannot be reached turns the dot in the nav bar red and puts an
  error banner with a retry button on the wall.
- The add form reports what the backend said, including rejected credentials.

Detecting the first frame deserves a note: browsers do not reliably fire `load`
on an `<img>` holding a multipart stream, so `CameraStream.vue` polls
`naturalWidth` instead of trusting the event.

## Layout

```
src/
  api/client.ts              typed calls against the backend contract
  types.ts                   Camera, CameraStatus, DiscoveredCamera
  composables/
    useCameraStore.ts        shared camera list and the single status poll
    useStreamSlot.ts         caps concurrent streams, queues the rest
    useIntersection.ts       is this tile on screen
    usePageVisible.ts        is this tab in front
  components/
    CameraStream.vue         the <img>, its lifecycle, and every failure state
    CameraTile.vue           one wall tile: video, dot, name, motion reading
    StateDot.vue             red recording, green watching, grey offline
    MotionReading.vue        change/threshold%, red once over threshold
    ErrorBanner.vue
  views/
    WallView.vue             the grid, and expand to fill
    AddCameraView.vue        form plus the mDNS discovered list
    CameraDetailView.vue     larger stream, full status, remove
  router/index.ts
```

## Endpoints used

`GET /api/cameras`, `POST /api/cameras`, `DELETE /api/cameras/{id}`,
`GET /api/cameras/{id}/status`, `GET /api/cameras/{id}/snapshot`,
`GET /api/cameras/{id}/stream`, `GET /api/discovered`, `GET /healthz`.

Errors are read as `{"error": "..."}`, as `{"message": "..."}`, as plain text,
or as bare status codes, whichever arrives.
