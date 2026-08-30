# argus-nvr backend

One Go binary that puts several ESP32-CAM cameras behind a single HTTP service.
It holds each camera's login session, proxies their MJPEG video, caches their
state, and serves the frontend's static files.

## Why the proxy exists

Each camera issues a session cookie scoped to its own host, so a browser cannot
be handed a camera's stream URL and expected to authenticate. Every request to a
camera goes through this service instead.

The cameras are microcontrollers with one radio between the video and everything
else, and each extra client streaming from one costs the others frame rate. Two
things follow from that, and both are deliberate:

- Every viewer of a camera shares **one** upstream connection. Six browser tabs
  cost the camera exactly what one costs it.
- The browser never polls a camera. A background poll reads `/record` every two
  seconds and the API answers from that cache.

## Build

Go 1.23. With Go installed:

```sh
go build ./...
go vet ./...
go test ./...
```

With Docker instead of a local toolchain:

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.23 go build ./...
```

Add `--network host` when a command needs to reach a camera.

## Run

```sh
go run . -addr :8080 -data ./data/cameras.json -static ./web
```

| Flag      | Default               | Meaning                                                  |
|-----------|-----------------------|----------------------------------------------------------|
| `-addr`   | `:8080`               | Listen address.                                           |
| `-data`   | `./data/cameras.json` | Camera list, created on first write.                      |
| `-static` | `./web`               | Built frontend to serve. Empty string serves the API only. |

Unknown paths fall back to `index.html`, so client-side routing survives a
reload.

### Docker

```sh
docker build -t argus-nvr .
docker run --rm -p 8080:8080 -v "$PWD/data":/data argus-nvr
```

Add `--network host` for mDNS discovery to work: multicast does not cross
Docker's default bridge network.

## Configuration file

`-data` points at a JSON array:

```json
[
  {
    "id": "a40df715f312ca6b",
    "address": "192.168.10.208",
    "name": "camera-alpha",
    "user": "admin",
    "pass": "..."
  }
]
```

The cameras only accept their original password on a form login, so there is
nothing to hash and the passwords are stored in the clear. The file is written
with mode **0600** and its directory 0700. Passwords are never logged and never
appear in an API response. Writes go through a temporary file and a rename, so
an interrupted write cannot truncate the list.

Cameras are normally added through the API rather than by editing this file.

## API

| Method | Path                            | Purpose |
|--------|---------------------------------|---------|
| GET    | `/api/cameras`                  | Camera list with cached status. |
| POST   | `/api/cameras`                  | Add one: `{"address","name","user","pass"}`. |
| DELETE | `/api/cameras/{id}`             | Remove one and stop its poller and stream. |
| GET    | `/api/cameras/{id}/status`      | Cached `/record` state. Never touches the camera. |
| GET    | `/api/cameras/{id}/snapshot`    | One JPEG. |
| GET    | `/api/cameras/{id}/stream`      | MJPEG, `multipart/x-mixed-replace`. |
| GET    | `/api/discovered`               | mDNS hosts that are not configured yet. |
| GET    | `/healthz`                      | Liveness. |

`status` returns the camera's `/record` JSON untouched under `record`, so
firmware fields this service does not know about still reach the UI.

A stream is a normal image source:

```html
<img src="/api/cameras/a40df715f312ca6b/stream">
```

## Sessions

A camera forgets its sessions when it reboots, which it does on every firmware
update and settings change. It also keeps only **three** sessions at once and
evicts the oldest, so signing in to a camera's own web UI a few times will push
this service out.

Both look identical from here: a 401 on a resource route, or a 302 to `/login`
on a page route. Either one triggers one silent re-login and a retry of the
original request. Concurrent requests that all hit a dead session share a single
re-login rather than each starting their own, because a login storm is precisely
what these devices cannot absorb.

A stream that drops reconnects every two seconds for as long as someone is
watching, so a camera reboot shows as a paused image rather than a broken one.
Bad credentials end the stream instead of retrying forever.

## Discovery

mDNS browsing of `_http._tcp` runs in the background, sweeping for five seconds
every thirty. It never blocks startup and finds nothing gracefully on a machine
with no network. Hosts that stop answering age out after 90 seconds.

Anything that speaks HTTP answers this browse, routers included, and the cameras
advertise an empty TXT record. `/api/discovered` therefore lists candidates to
add, not confirmed cameras. Entries matching a configured camera by IP or by
mDNS hostname are filtered out.

## Snapshots

Snapshots try the firmware's `/capture` route first. The camera tested against
answers 404 there despite linking to it from its own UI, so when that happens
the service remembers and serves a frame off the live stream instead. If someone
is already watching that camera, a snapshot costs the device nothing extra.
