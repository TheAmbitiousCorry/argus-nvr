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

Measured on one camera at 640x480: 22 to 24 frames per second, about 19KB a
frame. Frame rate is limited by Wi-Fi rather than by the device.

Round trips are slow and vary a lot: pings between 5ms and 620ms, HTTP round
trips up to 3 seconds when the camera is busy. Timeouts under about 10 seconds
report a healthy camera as offline.

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
                  "preSecs":int,
                  "lux":int,            mean scene brightness, 0 to 255
                  "rung":int,           auto exposure position, 0 to 10
                  "ael":int,            exposure compensation in effect now
                  "gc":int}             gain ceiling in effect now
POST /record     toggles recording, returns text
POST /image      form-encoded: autoimg, ael, gc, bri, con, sat, wb, flashlvl,
                 gray, hmir, vflip. Returns "ok", or a comma-separated list of
                 the controls this sensor refused. Under autoimg=1 the camera
                 owns ael and gc and ignores them in the form.
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
The camera holds twelve sessions and evicts the oldest. A recorder holding one
permanently shares that pool with anyone opening the camera's own page, so plan
to be signed out and to sign back in without reporting the camera as offline.

Two clients streaming from it halves the frame rate each. Poll `/record` at a
couple of seconds, not faster.
