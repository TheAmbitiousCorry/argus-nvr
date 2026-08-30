# esp32-cam-nvr

Watches several ESP32-CAM cameras from one screen. The cameras run the firmware
in the sibling repository `esp32-cam-fw`.

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

The camera passwords live in a named volume rather than a directory on the host,
because the service runs as a non-root user and a bind mount would need that
user's ownership before anything worked. The file is written 0600. To read it:

```bash
docker compose cp nvr:/data/cameras.json ./cameras.json
```

## Running it without Docker

```bash
cd frontend && npm ci && npm run build
cd ../backend && go run . -addr :8080 -static ../frontend/dist -data ./data/cameras.json
```

## Layout

- `backend/`  Go service: camera registry, mDNS discovery, session handling, stream proxy
- `frontend/` Vue interface, built to static files the backend serves
- `docs/camera-api.md` what the cameras expose, written from the firmware

## State of it

Working: camera registry, mDNS discovery, sign-in and silent re-login, MJPEG
fan-out, snapshots, and a wall view that only streams the tiles you can see.
Measured against real hardware, four browser viewers of one camera each received
the same 94 frames over twelve seconds from a single connection to the camera.

This is a learning project rather than a product. There are no accounts, no TLS,
and no authentication in front of the service itself, so it belongs on a trusted
network and nowhere else.

## Not yet built

Recording to this machine. The cameras keep footage on their own SD cards and
have no way to push it anywhere, so an FTP or upload receiver here would have
nothing to receive. That needs firmware work first, and it is on the firmware
repository's roadmap.

## Licence

MIT. See `LICENSE`.
