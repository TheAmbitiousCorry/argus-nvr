<img src="docs/brand/argus-eye.svg" width="72" align="right" alt="">

# Argus NVR

A recorder for a fleet of ESP32 cameras. It watches them, keeps a copy of everything they record, and records on their behalf when they cannot. One Go binary, one dark web app, one volume on disk.

![The camera wall](docs/media/argus-nvr.gif)

Built for [Argus Cam](https://github.com/TheAmbitiousCorry/argus-cam) firmware, though the contract it expects is [written down](docs/camera-api.md) and small enough to implement elsewhere.

## What it does

- **A live wall** of every camera, which picks what to show you rather than showing everything badly.
- **Pulls recordings off each camera's SD card** in the background, so losing the camera does not lose the footage.
- **Records for a camera that cannot record itself**, out of the live stream it is already receiving.
- **Transcodes MJPEG to H.264**, cutting stored size by about half, and only after proving the new file has every frame.
- **Plays back both formats in the browser**, including the raw MJPEG that no browser can decode.
- **Finds cameras on the network** over mDNS and adds them with one click.
- **Applies settings and firmware across many cameras at once**, showing you what each one said.

## What makes it different

The camera is the interesting constraint. It is a microcontroller with one radio, one sensor, 4MB of PSRAM, and 12 session slots shared with anyone browsing it. Nearly every design decision here follows from refusing to make that camera's life harder.

**The service pulls, the camera stays dumb.** There is no push protocol, no queue, no sync handshake, no agent to keep alive on the device. A background loop lists what each camera holds and downloads what it does not have. Identity is the path: `camera/day/time`. Nothing else decides whether a recording is already held, which makes the whole thing idempotent by construction. Restart mid-catch-up and it costs a listing request, not a re-download.

**Islanding is handled by not having a special case for it.** A camera cut off the network keeps recording to its own card, unaware anything is wrong. When it comes back, the same pull loop that always runs finds a bigger gap than usual and closes it. There is no recovery mode to enter, no state to reconcile, nothing to get stuck halfway through. [docs/islanding.md](docs/islanding.md) has the reasoning.

**A camera with a dead card still gets recordings.** When a camera reports its storage is missing or unwritable and says it is recording, the service writes the frames it is already receiving into an AVI on its behalf. It joins the same stream fan-out as the live viewers, so this costs zero extra connections to the camera. The camera decides what an event is; the service never invents an opinion of its own.

**One connection per camera, no matter who is watching.** All viewers of a camera share a single upstream MJPEG connection. Each subscriber gets a one-frame buffer with drop-oldest backpressure, so a slow viewer falls behind on its own rather than stalling the camera. Snapshots reuse the live frame if one is under 3 seconds old. During a page refresh, the new pump waits for the old one to finish before dialling, so viewer churn never briefly opens two.

**Transcoding is paranoid on purpose.** ffmpeg is invoked with `-fps_mode passthrough`, because the AVI runs at whatever variable rate the radio allowed that day and forcing a constant rate would desync it. The output is then fully decoded to count its frames, and the number has to match the source's own index exactly. The result also has to be smaller, or the encode is thrown away. Only after all of that does the original get deleted. `veryfast` is hardcoded because on sensor-noise footage the slower presets measured both slower *and* larger.

**Four streams, deliberately.** HTTP/1.1 allows six connections per origin, and an MJPEG stream holds its connection open forever. Take all six and the 2-second status poll starves, so the dots go stale and the wall lies about what is happening. So the browser runs four streams, keeps two connections free, and any camera without a slot falls back to a still every 5 seconds. A backgrounded tab and an off-screen tile both give their slots back.

**The wall decides what deserves the big tile.** A camera is promoted when it is recording or when scene change crosses its threshold, and stays promoted for 10 seconds after it settles, with hysteresis so a reading sitting on the line does not flap. The camera holding the large slot keeps it unless a challenger is 35% livelier, so two similarly busy cameras do not trade places every poll. Everything quiet takes turns on a rotation wheel that keeps its place when you pause it.

## Running it

```bash
git clone https://github.com/TheAmbitiousCorry/argus-nvr.git
cd argus-nvr
docker compose up -d
```

Then open **`http://localhost:8080`**.

The compose file uses `network_mode: host`, which is not optional. mDNS discovery needs multicast, and a bridge network does not carry it. That also means no port mapping: the service is on your host's port 8080 directly.

Data lives in a named volume at `/data` (`cameras.json` and `recordings/`), not a bind mount, because the container runs as non-root and a bind mount would need its ownership fixed on the host first. To get the camera list out:

```bash
docker compose cp nvr:/data/cameras.json ./cameras.json
```

### From source

```bash
cd frontend && npm ci && npm run build && cd ..
cd backend && go build -o nvr .
./nvr -static ../frontend/dist -data ./data/cameras.json -recordings ./data/recordings
```

Go 1.23 and Node 22. ffmpeg on `PATH` is optional: without it nothing is transcoded, one line is logged saying so, and the service records and serves exactly as before with larger files.

| Flag | Default | |
|---|---|---|
| `-addr` | `:8080` | Listen address |
| `-data` | `./data/cameras.json` | Camera list |
| `-static` | `./web` | Built frontend. Empty disables serving it |
| `-recordings` | `./data/recordings` | Archive root. Empty disables the archive entirely |
| `-recordings-max-bytes` | 20GB | Size cap. 0 means unlimited |
| `-transcode` | `true` | Re-encode to H.264 when ffmpeg is present |
| `-transcode-crf` | 24 | 0-51, lower is better quality |
| `-ffmpeg` | `ffmpeg` | Binary name or path |
| `-transcode-backfill-gap` | 10s | Pause between backfill encodes |

### Frontend development

```bash
cd frontend && npm run dev     # port 5173, proxies /api to localhost:8080
```

Point it elsewhere with `NVR_BACKEND=http://camera-host:8080 npm run dev`. The `/api` proxy runs with timeouts disabled, since MJPEG responses never end.

## Adding cameras

Go to **Add camera**. The right column lists what answered mDNS on your network in the last 90 seconds, filtered down to things that identified themselves as cameras with an `argus=cam` TXT record. Everything else that answered is counted, not listed, because offering you a router is worse than making you type an address.

Type the camera's admin username and password on the left, then click Add next to a discovered camera. The credentials are held by the NVR, which keeps the session with the camera; your browser never talks to a camera directly.

You can also add by address. If you type a `.local` name that discovery has already resolved, the IP is stored and the name is kept as the label, because a container has no mDNS resolver and would otherwise show a working camera as offline forever. IP changes are followed: every 30 seconds each stored address is re-resolved, and a camera that moved is quietly updated.

## Using it

![The wall](docs/media/wall.png)

**Wall** has three layouts. *Grid* shows everything at equal size, click a tile to expand it. *Focus* gives one large tile plus a scrolling strip, click a small one to hold it there. *Single* is one large tile plus name chips for the rest. Rotation cycles the quiet cameras at 5 to 60 seconds; motion interrupts it. Escape releases a held camera or closes an expanded one. The header tells you how many are online, recording, moving, and how many streams are running against how many are on stills.

**Files** filters by camera and date, offering only the combinations that actually hold recordings. A recording pulled from a camera's card and one the service recorded itself are both listed, the latter tagged `service`. Recordings whose camera has since been removed stay listed and playable.

Playback splits by format. Transcoded MP4 goes into a normal `<video>` element with native seeking, served with range request support. Raw MJPEG cannot be decoded by any browser, so it is replayed as a multipart stream paced from the actual per-frame timestamps in the file: a clip that ran slow because the radio was busy replays at its true slow pace, rather than at a guessed constant rate. Pausing freezes the current frame onto a canvas and drops the connection. Scrubbing while paused opens a stream at the target frame with `speed=0`, takes one frame, and closes again, so a scrub costs one frame over the wire rather than a stream left running.

**Settings** edits many cameras at once. It reads each selected camera's current config first, one at a time. Where cameras disagree on a field it shows `varies` and names which camera holds which value, and leaves it alone unless you explicitly change it, so applying a change never silently flattens the ones you did not touch. Only fields you edited are sent. Applying is parallel and always returns 200, with a per-camera result line, because one unreachable camera should not hide what the other nine said.

Under the hood each camera's full config is read and your patch merged over it before posting back, because the firmware's form handlers read an absent checkbox as "off". A partial post would reset everything you did not mention.

**Firmware** flashes cameras strictly one at a time, in the order you picked, with a live progress bar per camera. If one fails the run stops and the rest are marked not attempted, rather than continuing into cameras nobody watched land. Each camera reboots as it finishes, so a parallel batch would leave you with a fleet in an unknown state. The picker shows each camera's version, OTA slot, and whether it is on trial or recently rolled back. Around seven seconds per camera on a local network.

## How recordings are stored

```
/data/recordings/<camera>/<YYYY-MM-DD>/<HHMMSS>.avi   (or .mp4 once transcoded)
                                      /<HHMMSS>.json  (duration, frames, source)
                                      /.aged          (what was deleted, and when)
```

There is no database and no index. The directory tree is the index, so there is nothing to rebuild after a crash. The extension is not part of a recording's identity, which is why transcoding never breaks a URL you already have.

Every write lands on a temp file in the destination directory and is renamed into place, so a partial download is never visible as a recording. Downloads are validated against the AVI's own stated length before being accepted, which catches both a truncated transfer and the camera's plain-text error page arriving where a video should be.

When the archive passes its size cap, the oldest recordings go first, except that the newest one is never deleted. Each deletion is noted in that day's `.aged` file, so the puller does not see a gap, conclude the camera still has something it lacks, and download it back again forever.

## API

```bash
curl localhost:8080/api/cameras
curl localhost:8080/api/cameras/{id}/status
curl -o still.jpg localhost:8080/api/cameras/{id}/snapshot
curl localhost:8080/api/recordings/days
curl 'localhost:8080/api/recordings?cameraId={id}&day=2026-08-30'
```

| Method | Path | |
|---|---|---|
| `GET` | `/api/cameras` | Every camera with its cached status |
| `POST` | `/api/cameras` | `{address, name, user, pass}` |
| `DELETE` | `/api/cameras/{id}` | |
| `GET` | `/api/cameras/{id}/status` | Cached, never touches the camera |
| `GET` | `/api/cameras/{id}/config` | The camera's own config, passed through |
| `GET` | `/api/cameras/{id}/snapshot` | One JPEG |
| `GET` | `/api/cameras/{id}/stream` | MJPEG, `multipart/x-mixed-replace` |
| `GET` | `/api/discovered` | Cameras seen on the network, not yet added |
| `POST` | `/api/settings` | `{cameraIds, image, recording}`. Always 200, results per camera |
| `POST` | `/api/firmware` | Multipart: `file`, `cameraIds`. Sequential, stops on first failure |
| `GET` | `/api/recordings` | `?cameraId=&day=&start=&limit=` (max 1000) |
| `GET` | `/api/recordings/days` | Days holding recordings, with counts and sizes |
| `GET` | `/api/recordings/{camera}/{day}/{at}` | The file. Range requests supported |
| `GET` | `/api/recordings/{camera}/{day}/{at}/frames` | Frame index and timings. AVI only |
| `GET` | `/api/recordings/{camera}/{day}/{at}/stream` | Paced replay. `?from=&speed=0\|0.5\|1\|2\|4` |
| `GET` | `/api/storage` | Bytes held, cap, counts |
| `GET` | `/healthz` | `{ok, cameras}` |

Full request and response shapes are in [docs/bulk-api.md](docs/bulk-api.md); what the service expects a camera to provide is in [docs/camera-api.md](docs/camera-api.md).

There is no SSE and no WebSocket. The wall polls `/api/cameras` every 2 seconds, which returns the roster and every camera's live state in one response, and pauses entirely when the tab is hidden.

## Timings

| | |
|---|---|
| Camera poll | every 2s, 12s timeout, first tick jittered across the fleet |
| Firmware version check | every 5min once known, 1min until then |
| Recording pull | every 60s, one download at a time per camera, 2s between clips |
| Stand-in check | every 1s, ends after 10s without frames, rolls over at 10min |
| Address re-resolution | every 30s |
| mDNS sweep | 5s of listening every 30s, hosts forgotten after 90s |
| Retention sweep | every 5min, and once at startup |

The 12-second poll timeout is deliberate. A camera under load can take 3 seconds for one HTTP round trip, and a re-login costs two of them, so anything under 10 seconds reports a healthy camera as offline.

## The container

Four build stages producing a distroless image with no shell and no package manager, running as `nonroot`. ffmpeg is compiled from source into it with exactly the codecs this needs and nothing else: 5.7MB against 80MB or more for a ready-made static build carrying every codec in existence. The comment in the Dockerfile is explicit that deleting that stage still gives you a service that runs, records, and serves, and simply holds larger recordings.

Encoding runs at `nice 10` on its own process group with `-threads 1`, so an encode never competes with relaying live video to your browser.

## Not here

- **No authentication on the NVR itself.** Camera credentials are stored, in plaintext, in `cameras.json` with mode 0600, because the firmware's login form only accepts the original password so there is nothing to hash against. Put this on a network you trust and do not expose port 8080.
- **No recording deletion from the UI.** Retention is by size cap only.
- **No continuous recording.** The camera decides when an event is happening; this records what it says.
- **No motion detection here.** That runs on the camera, where the frames already are.

## Licence

MIT. See [LICENSE](LICENSE).
