# Argus NVR

<img src="docs/brand/argus-mark.svg" alt="" width="86" align="right">

Watches several ESP32-CAM cameras from one screen. The cameras run the firmware
in the sibling repository
[esp32-cam-fw](https://github.com/TheAmbitiousCorry/esp32-cam-fw).

Named for Argus Panoptes, who watched with a hundred eyes and never closed more
than half of them at once. Each camera is one eye of him, and carries one eye as
its own mark.

A Go service holds the cameras' credentials, keeps a session with each, and
re-serves their video. A Vue interface shows them as a wall.

## Why it proxies rather than linking to cameras

Each camera issues its own session cookie scoped to its own host. A page that
pointed straight at four cameras would need you to sign into four cameras. The
service signs in on your behalf and re-serves the streams, so there is one login
and the camera passwords never reach the browser.

That has a second benefit. The cameras are microcontrollers with one camera and
one radio between them, and every extra viewer costs a camera real frame rate.
Going through one service means one connection per camera regardless of how many
people are watching.

## Running it

```bash
docker compose up -d --build
```

Then open http://localhost:8080 and add a camera by its address, or pick one the
service found on the network. Adding one over the API instead:

```bash
curl -X POST localhost:8080/api/cameras -H 'Content-Type: application/json' \
  -d '{"address":"camera-alpha.local","name":"front door","user":"admin","pass":"..."}'
```

The image builds both halves and carries the frontend with it, so there is
nothing to build by hand and no web server to put in front.

Host networking is deliberate: mDNS discovery needs multicast, which a bridge
network does not carry. That is also why no port is published.

The camera passwords and the recordings live in a named volume rather than a
directory on the host, because the service runs as a non-root user and a bind
mount would need that user's ownership before anything worked. The camera list
is written 0600. To read it:

```bash
docker compose cp nvr:/data/cameras.json ./cameras.json
```

## Running it without Docker

```bash
cd frontend && npm ci && npm run build
cd ../backend && go run . -addr :8080 -static ../frontend/dist \
  -data ./data/cameras.json -recordings ./data/recordings
```

`-recordings` is where footage taken off the cameras is kept, and
`-recordings-max-bytes` is the size it is aged down to, oldest first, 20GB by
default. An empty `-recordings` turns both off: cameras are still watched and
proxied, nothing is kept.

## Layout

- `backend/`  Go service: camera registry, mDNS discovery, session handling, stream proxy
- `frontend/` Vue interface, built to static files the backend serves
- `docs/camera-api.md` what the cameras expose, written from the firmware
- `docs/islanding.md` how footage gets off a camera and what the service serves

## State of it

Working: camera registry, mDNS discovery, sign-in and silent re-login, MJPEG
fan-out, snapshots, and a wall view that only streams the tiles you can see.
Measured against real hardware, four browser viewers of one camera each received
the same 94 frames over twelve seconds from a single connection to the camera.

This is a learning project rather than a product. There are no accounts, no TLS,
and no authentication in front of the service itself, so it belongs on a trusted
network and nowhere else.

Footage now outlives the camera it was recorded on. The service pulls recordings
off each camera's card in the background and records on behalf of a camera that
has no usable card, keeping both under one retention limit on the data volume.
Measured against real hardware, nine recordings came off a camera's card
untouched and ffprobe read every frame of each. `docs/islanding.md` has the
design and the routes.

## Not yet built

An interface for any of it. The recordings are listed and served over the API,
and nothing in the Vue app shows them yet.

## Licence

MIT. See `LICENSE`.
