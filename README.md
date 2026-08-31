<img src="docs/brand/argus-mark.svg" alt="" width="72" align="right">

# Argus NVR

**The NVR for ESP32 security cameras.** One command to start, cameras that add
themselves, and everything after that from one page: watching, footage, settings
and updates.

![The wall, the file browser, settings across every camera, and firmware](docs/media/argus-nvr.gif)

## It finds your cameras

`docker compose up` and open a page. Cameras running Argus Cam announce
themselves over mDNS and say that they are cameras, so adding one is a click
rather than an address to look up in your router. Other things that answer the
same announcement, routers and printers among them, are counted rather than
offered as cameras you might want.

A camera that moves to a new address is followed rather than lost: the name it
advertises is resolved to whatever address answers today, so a DHCP lease
changing overnight is not something you have to notice.

## Watch

A wall of live tiles. Switch to **focus** and the camera that just saw movement
takes the large slot; switch to **single** and it fills the screen. A camera you
pick by hand keeps the slot until you let it go.

Each camera is opened once and fanned out to everyone looking, so a second
browser costs the camera nothing. Tiles you cannot see are not streamed at all,
and a tile that cannot get a live connection shows stills rather than fighting
for one.

## Keep

Argus pulls recordings off the cameras' SD cards onto a real disk, in the
background, so footage outlives the camera that recorded it. A camera whose card
has failed is recorded on Argus's behalf instead, from the stream already open
to it.

Recordings are converted to H.264, which on this hardware halves the disk
they take, and plays in the browser with a normal timeline and seeking. The
original is deleted only after the new file is decoded end to end and its frame
count checked; anything less and it is kept.

Browse by date and camera, play in the page, download what you want.

## Configure

Every setting the firmware has, across as many cameras as you select, written in
one go: motion sensitivity, recording length, pre-trigger history, schedule,
resolution, quality, exposure, white balance, flash.

Where the cameras you selected disagree, the field says **varies** and names the
values, rather than showing you one camera's setting and quietly writing it to
the rest.

## Update

Pick a firmware file, pick cameras, and Argus flashes them one at a time. If one
fails it stops there and tells you which cameras it did not touch, rather than
leaving a fleet half updated. It shows what each camera is running, whether an
image is still on trial, and what any rollback reverted from.

## Run it

```bash
docker compose up -d --build
```

Open <http://localhost:8080> and add a camera. The image carries the service,
the interface and an encoder, and needs nothing else installed.

Host networking is deliberate: mDNS needs multicast, which a bridge network does
not carry. The camera list and the recordings live in a named volume, because
the service runs as a non-root user; the list is written `0600`.

```bash
docker compose cp nvr:/data/cameras.json ./cameras.json   # to read it
```

<details>
<summary>Without Docker</summary>

```bash
cd frontend && npm ci && npm run build
cd ../backend && go run . -addr :8080 -static ../frontend/dist \
  -data ./data/cameras.json -recordings ./data/recordings
```

`-recordings` is where footage is kept, aged down to `-recordings-max-bytes`,
oldest first, 20GB by default. An empty `-recordings` keeps nothing: cameras are
still watched and proxied. Without `ffmpeg` on the path nothing is transcoded
and recordings stay as they arrived.
</details>

The cameras run [Argus Cam](https://github.com/TheAmbitiousCorry/argus-cam), the
firmware in the sibling repository.

## Honestly

A learning project, built from scratch and measured on real hardware rather than
assumed. It works: 180 recordings and 680 MB came off two cameras while it was
being written, and transcoding took 1209 MB of that footage down to 524 MB with
every frame accounted for.

There are no accounts, no TLS, and nothing standing in front of the service, so
it belongs on a network you trust and nowhere else.

`docs/roadmap.md` says what is next. `docs/islanding.md` and `docs/bulk-api.md`
were written before the code they describe, which is why the two halves agree.

Named for Argus Panoptes, who watched with a hundred eyes and never closed more
than half at once. Each camera is one of his eyes, and carries one as its mark.

## Layout

- `backend/` Go: camera sessions, stream fan-out, the archive, transcoding, mDNS
- `frontend/` Vue: the wall, files, settings, firmware
- `docs/` the decisions, and the marks

## Licence

MIT. See `LICENSE`.
