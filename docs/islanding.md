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
