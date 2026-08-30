# Islanding: footage that survives the camera it was recorded on

A recording that exists only on a card in a camera is lost with the camera, and
a camera with no card records nothing at all. The card is an island: reachable
only by whoever holds the camera, and useless the moment there is no card in it.

This is the design for taking footage off that island. Both halves are written
against this document rather than against each other.

## The shape of the decision

The obvious design is for the camera to push files to the service. It is the
wrong one. The camera is a microcontroller with one radio and 4MB of flash, and
a push protocol puts retries, backoff, resume, authentication and a queue on the
side of the system least able to carry them.

The service already holds an authenticated session with every camera and an open
stream from each. It has a disk, a scheduler, and a language with a standard
library. So the service pulls, and the camera stays dumb.

That decision removes almost all of the firmware work. Nothing new is needed for
the case where a camera was cut off, because the service can already list a
camera's files and download a recording from it. Only two things are missing,
and one of them is a JSON version of a page that already exists.

## Three states, and what happens in each

**A camera with a card, on the network.** What happens today. It records to its
own card. The service notices recordings it does not have and pulls them in the
background. The card becomes a buffer rather than the only copy.

**A camera with no card.** It cannot record, so the service records for it. The
service already holds that camera's live stream; when the camera reports that it
has triggered, the service writes the frames it is already receiving. The camera
does nothing new at all.

**A camera cut off from the network.** It records to its card exactly as it does
now, unaware anything is wrong. When it comes back, the service finds recordings
it has not seen and pulls them. There is no sync protocol, no queue on the
camera, and nothing to go wrong while the network is down: the camera's job is
to keep recording, and catching up is the service's problem.

## What the firmware adds

### `GET /recordings?day=YYYY-MM-DD`

The file listing already knows this; it renders it as HTML for a person. This is
the same answer as JSON, so the service does not have to scrape a page that
exists to be looked at.

```json
{
  "day": "2026-08-30",
  "recordings": [
    { "at": "131529", "durMs": 10004, "bytes": 12256740, "frames": 213 }
  ],
  "more": false
}
```

Read from the day's summary file, which already holds exactly these fields, so
this costs one file read for a whole day. `more` is true when the day holds more
recordings than one response should carry.

`GET /recordings/days` lists the days that exist, so the service does not have
to guess dates.

### A flag saying the camera cannot record

`/config` and `/record` should say plainly that there is no usable card, so the
service knows to record on the camera's behalf rather than inferring it from a
failure. `"storage": "ok" | "missing" | "unwritable"`.

## What the service adds

### Pulling

For each camera, periodically: list days, list each day's recordings, and
download any it has no copy of, using the AVI endpoint that already exists
(`GET /video?dir=/rec/<day>/<at>`). Sequential per camera, so a catch-up after
an outage does not saturate the radio the live view is sharing.

A recording is identified by camera id, day and start time. Downloading is
idempotent: a recording already held is not fetched again, and an interrupted
download leaves no half file where a whole one is expected.

### Recording on the camera's behalf

When a camera reports `storage` other than `ok` and its `/record` shows
`active`, the service writes the frames it is already receiving into an AVI,
under the same identity scheme. Nothing is asked of the camera.

### Storage and retention

Recordings live under the data volume, one directory per camera, one per day.
Retention is the operator's, not the camera's: the service ages recordings out
by total size, and never deletes anything it has not finished writing.

The service does not delete from a camera's card. The card is the camera's, and
a service that can erase footage on twelve cameras is a service that can lose
all of it at once.

## What this is not

Not live continuous recording. The service records what a camera says is worth
recording, on the same triggers the camera already uses, because a microSD card
and a hard disk should not disagree about what an event is.

Not a replacement for the card. A camera with a working card keeps using it. The
card is what survives the network going down; the service is what survives the
camera going missing.

## What the service exposes

Written after the service half was built, against the routes it actually
serves, so the interface has something to be built against later.

Recordings live under the data volume as
`<recordings>/<camera id>/<day>/<start time>.avi`, one directory per camera and
one per day, with a small `<start time>.json` beside each holding what the AVI
cannot say. The path is the identity: nothing else decides whether a recording
is already held, which is what makes pulling idempotent across a restart.

### `GET /api/recordings`

Everything the service holds, newest first.

```
?cameraId=<id>     only this camera
?day=YYYY-MM-DD    only this day
?start=<n>         skip the first n
?limit=<n>         at most n, default 200, maximum 1000
```

```json
{
  "recordings": [
    {
      "cameraId": "2a8840a115b77cbf",
      "day": "2026-08-30",
      "at": "131529",
      "startedAt": "2026-08-30T13:15:29",
      "durMs": 9992,
      "bytes": 12261660,
      "frames": 213,
      "source": "camera",
      "heldAt": "2026-08-30T22:46:13.905Z"
    }
  ],
  "start": 0,
  "more": false
}
```

`more` and `start` work as they do on the camera's own listings, so both sides
count recordings the same way.

`source` is `camera` for a recording pulled off a card and `service` for one the
service recorded on a camera's behalf. The two are not interchangeable: the
first is a copy of something that also exists on a card, and the second is the
only copy there is.

`startedAt` carries no timezone. It is the camera's clock, and the service does
not know what that clock is set to; stamping it with the server's zone would be
an invention rather than a conversion. `heldAt` is this service's clock, and
does carry one.

`frames` is absent when the camera's listing did not say.

- `404` when the service is running without a recordings directory

### `GET /api/recordings/days`

What is held, newest day first, so a caller can offer a date without first
fetching every recording behind it. `?cameraId=<id>` narrows it.

```json
{
  "days": [
    { "cameraId": "2a8840a115b77cbf", "day": "2026-08-30", "recordings": 9, "bytes": 31380952 }
  ]
}
```

### `GET /api/recordings/{cameraId}/{day}/{at}`

The recording itself, as `video/x-msvideo`. A trailing `.avi` is accepted and
ignored, so the URL can be handed to a player that decides what to do from the
name. Range requests are answered, because a player seeking in a clip asks for
the middle of the file and without ranges every scrub fetches the whole thing
again.

- `404` no such recording, or an identity that is not a camera, a date and a
  time

### `GET /api/storage`

What the archive is using against what it is allowed, which is the number that
decides when the oldest recordings start going.

```json
{ "bytes": 32443084, "maxBytes": 1073741824, "recordings": 9, "pendingBytes": 1061320 }
```

`pendingBytes` is writes that have not finished. They count against the limit,
because the disk does not care that they are unfinished, but they are never
deleted to get under it.

### What the camera list gained

`GET /api/cameras` and `GET /api/cameras/{id}/status` carry three more fields in
`status`:

| Field | Meaning |
|---|---|
| `standIn` | the service is recording this camera's stream right now, because the camera cannot |
| `fetching` | a recording is being downloaded off this camera's card |
| `pulledAt`, `pullError` | the last attempt to catch up with the card, absent until there has been one |

`checkedAt` stops moving while `fetching` is true. A camera has one radio, and
polling through a download costs the download throughput and times out often
enough to report a camera that is busy answering us as offline.

### Retention, and the one thing it has to remember

The archive is aged down to `-recordings-max-bytes` (default 20GB), oldest
recording first. The newest recording is never removed, because an archive that
empties itself is worse than one that is over its limit, and a recording still
being written is never a candidate, because until it is finished it does not
have a name that anything can see.

A recording that is aged out is noted in a hidden `.aged` file in its day's
directory. Without that note the two halves of this fight: retention lets a
recording go to stay under the limit, the puller sees the camera still has it on
its card and that the service has no copy, and downloads it again, forever.

Nothing here deletes anything from a camera's card.

### What is not pulled

Recordings made before a camera's clock reached an NTP server are numbered
rather than dated, and the camera lists them separately. They are skipped: a
recording is identified by camera, day and start time, and something with no day
cannot be stored under that scheme or asked for again later.
