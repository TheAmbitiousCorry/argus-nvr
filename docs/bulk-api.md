# Bulk settings and firmware API

The contract between the Go service and the Vue interface for reading and
writing settings across several cameras at once. Both halves are written
against this document rather than against each other, because the last time
they were written separately they disagreed about where a field lived and every
camera showed as offline.

Field names are the firmware's own, unchanged, so a value can be traced from a
form on the camera to a control in the app without a translation table.

## What the firmware offers

Authentication is a form post to `/login` with `user` and `pass`, which sets a
`sid` cookie. The service already handles this.

### `GET /config`

Every stored setting, as JSON. This is what makes a partial update possible:
the form handlers below take a whole form, and a checkbox left out of a POST
reads as unticked rather than unchanged.

```json
{
  "camname": "alpha", "tz": "SAST-2", "ssid": "...", "apwin": true,
  "moten": true, "motsens": 15, "recsec": 10, "presec": 5, "quietsec": 5,
  "keepfree": 512,
  "schen": false, "schfrom": 22, "schto": 6, "schdays": 127,
  "fsize": 10, "jq": 14, "autoimg": true,
  "ael": 0, "gc": 0, "bri": 0, "con": 0, "sat": 0, "wb": 0,
  "gray": false, "hmir": false, "vflip": false, "flashlvl": 60,
  "aelnow": 2, "gcnow": 4, "unsupported": ""
}
```

`aelnow` and `gcnow` are what the sensor is actually set to, which under auto
exposure is not what is stored. `unsupported` is a comma-separated list of
controls this sensor refused, empty when it accepted them all. All three are
read-only.

### `POST /image`

Form-encoded. Accepts these and ignores the rest.

| Field | Type | Range |
|---|---|---|
| `autoimg` | checkbox | `1` or absent |
| `ael` | int | -2 to 2, ignored while `autoimg` is on |
| `gc` | int | 0 to 6, ignored while `autoimg` is on |
| `bri` | int | -2 to 2 |
| `con` | int | -2 to 2 |
| `sat` | int | -2 to 2 |
| `wb` | int | 0 to 4 |
| `gray` | checkbox | `1` or absent |
| `hmir` | checkbox | `1` or absent |
| `vflip` | checkbox | `1` or absent |
| `flashlvl` | int | 0 to 255 |

Replies `ok`, or a comma-separated list of controls the sensor refused.

### `POST /recording`

Form-encoded.

| Field | Type | Notes |
|---|---|---|
| `moten` | checkbox | motion detection on |
| `motsens` | int | 1 to 100, percent of the scene |
| `recsec` | int | seconds per recording |
| `presec` | int | seconds of history kept before the trigger |
| `quietsec` | int | stillness needed before a recording ends |
| `keepfree` | int | megabytes to keep free on the card |
| `fsize` | int | framesize enum |
| `jq` | int | JPEG quality, lower is better |
| `schen` | checkbox | schedule on |
| `schfrom`, `schto` | int | hours, 0 to 23 |
| `schday` | repeated | one `schday=<0-6>` per enabled day. `schdays` in `/config` is a bitmask of the same days |

### `POST /update`

The raw firmware bytes as the request body, with `Content-Length` set. Not
multipart. Replies 200 and reboots. Takes about 7 seconds on a local network,
which is eight times faster than the espota path.

## What the service exposes

### `GET /api/cameras/{id}/config`

The camera's `/config` document, passed through unchanged so a field the
firmware gains needs no change here.

- `200` the document
- `502` `{"error": "..."}` when the camera did not answer

### `POST /api/settings`

```json
{
  "cameraIds": ["a1b2", "c3d4"],
  "image":     { "bri": 1, "autoimg": false, "ael": -1 },
  "recording": { "motsens": 20 }
}
```

Both `image` and `recording` are optional and are partial: only the named
fields change. For each camera the service reads `/config`, merges the patch
over it, and posts the complete form back, because a partial post would reset
everything omitted.

Always `200`, with one result per camera. A camera that fails does not stop the
others.

```json
{
  "results": [
    { "cameraId": "a1b2", "ok": true },
    { "cameraId": "c3d4", "ok": false, "error": "camera did not answer" }
  ]
}
```

### `POST /api/firmware`

`multipart/form-data` with `file` holding the firmware image and `cameraIds` a
comma-separated list.

Cameras are flashed one at a time, never in parallel: each one reboots, and a
failure part way through should leave the rest untouched and reported rather
than a fleet in an unknown state.

```json
{
  "results": [
    { "cameraId": "a1b2", "ok": true, "bytes": 1381600 },
    { "cameraId": "c3d4", "ok": false, "error": "upload interrupted" }
  ]
}
```

## Known gap

`.local` addresses do not resolve inside the container: the image has no mDNS
resolver, so a camera added by the name its own discovery reported comes back
offline. Discovery already learns the IP alongside the name, so the address
stored should be the one that resolves.
