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
docker compose up --build
```

Then open http://localhost:8080.

Host networking is deliberate: mDNS discovery needs multicast, which a bridge
network does not carry.

## Layout

- `backend/`  Go service: camera registry, mDNS discovery, session handling, stream proxy
- `frontend/` Vue interface, built to static files the backend serves
- `docs/camera-api.md` what the cameras expose, written from the firmware

## Not yet built

Recording to this machine. The cameras keep footage on their own SD cards and
have no way to push it anywhere, so an FTP or upload receiver here would have
nothing to receive. That needs firmware work first, and it is on the firmware
repository's roadmap.
