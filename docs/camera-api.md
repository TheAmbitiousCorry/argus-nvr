# ESP32-CAM firmware API

The cameras this NVR talks to. Firmware source is the sibling repository
`esp32-cam-fw`. One camera is live on the development network at
`192.168.10.208`, named `camera-alpha`.

## Authentication

Session cookie. Everything except `/login` requires it.

```
POST /login          form: user=<name>&pass=<password>
                     302 to / with Set-Cookie: sid=<32 hex chars>
```

Sessions live in the camera's RAM, so they end when it reboots, which it does
on every firmware update and settings change. Treat a 302 to `/login` or a 401
as "log in again", not as an error.

Page routes answer an unauthenticated request with 302 to `/login`. Resource
routes answer with 401.

## Video

```
GET  http://<camera>:81/stream    multipart/x-mixed-replace
                                  boundary: espcamframeboundary
                                  parts are Content-Type: image/jpeg
GET  /capture                     one JPEG
```

The stream is on port 81, everything else on port 80. Cookies are scoped by
host and ignore the port, so one session covers both.

Measured on one camera at 640x480: about 10 frames per second, 36KB a frame.
Frame rate is limited by Wi-Fi rather than by the device.

## State

```
GET  /record     JSON, no side effects despite the name:
                 {"active":bool,        recording right now
                  "frames":int,         frames in the current recording
                  "fps":float,
                  "triggered":bool,     started by motion rather than by hand
                  "motion":bool,        motion recording enabled
                  "armed":bool,         enabled and inside its schedule
                  "change":int,         percent of the scene changing now
                  "threshold":int,      percent needed to trigger
                  "preFrames":int,      frames of pre-trigger history buffered
                  "preSecs":int}
POST /record     toggles recording, returns text
GET  /status     HTML table, one <tr><th>key</th><td>value</td></tr> per row
```

## Recordings

Stored as `/rec/YYYY-MM-DD/HHMMSS/`, containing `video.mjpeg` (concatenated
JPEGs), `index.txt` and `meta.txt`.

```
GET /files?path=<dir>&start=<n>   HTML listing, 50 per page
GET /recindex?dir=<dir>           JSON [[timestampMs, byteOffset, byteLength], ...]
GET /frame?dir=<dir>&off=<n>&len=<n>   one JPEG, read at that offset
GET /download?path=<file>         raw file, chunked
GET http://<camera>:81/playstream?dir=<dir>&from=<frame>
                                  replays a recording as the same multipart
                                  stream, paced from the index timestamps
```

`meta.txt` is one line: `<frames> <durationMs> <bytes>`.

## Discovery

Each camera advertises `_http._tcp` on port 80 under its configured name, so
`camera-alpha.local`. It also advertises `_arduino._tcp` on 3232 for
over-the-air firmware updates.

## Notes that matter

The camera is a single-core-ish microcontroller with one camera and one radio.
Two clients streaming from it halves the frame rate each. Poll `/record` at a
couple of seconds, not faster.
