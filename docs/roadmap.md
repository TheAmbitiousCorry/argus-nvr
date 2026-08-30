# Roadmap

Argus NVR: watches several ESP32-CAM cameras running the firmware in
[esp32-cam-fw](https://github.com/TheAmbitiousCorry/esp32-cam-fw).

Items describe the situation someone is in, not the feature that would fix it,
so the shape stays open until it is worked on. Appetite is how much time the
item is worth, not an estimate of how long it will take.

## Now

- **Footage survives the camera it was recorded on**: a recording that exists
  only on a card in a camera is lost with the camera, and a camera with no card
  records nothing at all. The card is an island: reachable only by whoever holds
  the camera, and useless the moment there is no card in it. What this needs is
  for the service to be a place footage can live, for a camera to use it when it
  has no card of its own, and for a camera that was cut off to catch up once it
  is back. Domains: storage, network. Appetite: large.

## Next

- **Footage costs a tenth of the disk it does now**: a recording is MJPEG, every
  frame stored whole with no knowledge of the one before it, which on a mostly
  still scene is almost all waste. Measured on a real recording here: ten seconds
  of 800x600 is 11.69MB as MJPEG and 1.96MB as H.264 at CRF 24, 0.66MB at CRF 28.
  Six to eighteen times, for 0.4 seconds of encoding. It also removes the reason
  the interface cannot use a video element. Domains: storage. Appetite: medium.

- **Someone is told what was there, not that something moved**: the camera reports
  the percentage of the scene that changed, which fires on shadows, headlights and
  branches. The three things worth knowing are whether it was a person, who it
  was, and which vehicle. Each is a separate capability with a different cost and
  a different weight:

  - **Object recognition** answers "a person, a car, a cat". It is the one that
    turns a recorder into an alarm, and the only one of the three that needs no
    stored identity to be useful.
  - **Facial recognition** answers "who". It needs a store of faces to compare
    against, which is a different kind of thing to hold than footage.
  - **Plate recognition** answers "which vehicle", and wants a still frame at a
    resolution these cameras are at the edge of providing.

  All three read pixels, so all three need decoding, and all three must run
  **before** any transcode: MJPEG is every-frame-a-keyframe, so one frame can be
  pulled out and decoded alone, while H.264 would mean decoding a run of frames to
  reach any one of them. Sampling is enough, roughly one frame in twenty at a
  fraction of the resolution, so the cost is small next to the ~900 frames a
  second one core decodes.

  Worth knowing before starting rather than after: faces and plates are biometric
  and vehicle data, which POPIA treats as special personal information and the
  GDPR treats likewise. That does not make them off limits on a private property,
  but it does make where the data lives and how long it is kept part of the
  design rather than an afterthought. Domains: detection, storage, privacy.
  Appetite: large.

- **A recording is filed under the camera that made it**: the archive is keyed on
  an identifier the service generates, so removing a camera and adding it back
  orphans everything already pulled off it and downloads all of it again. That
  happened here: 462MB filed under a camera that no longer exists, with the same
  footage arriving beside it. The camera now reports its MAC, which is the one
  name it owns. Domains: storage. Appetite: small.

- **One screen shows what is worth looking at**: a wall of equal tiles gives a
  quiet hallway the same room as a person at the door, so the thing worth seeing
  is the same size as everything else and arrives with no more emphasis. The
  firmware already reports how much of each scene changed, which is the signal
  this would be built on. Views that hold one feed at a time and cycle through
  the rest; a camera that sees movement takes the focus; when several see
  movement at once, those are the ones on screen and the still ones wait.
  Domains: interface. Appetite: medium.

## Later

- **Someone is told an event happened**: the wall only tells you about motion
  while you are looking at it. Delivery route deliberately unspecified. What
  would promote it: cameras being somewhere nobody watches. Domains: network.
  Appetite: small.

- **A camera is trusted before it is believed**: the service holds credentials
  for every camera and talks to them over plain HTTP on a flat network. What
  would promote it: putting a camera anywhere less trusted than a home LAN.
  Domains: network, security. Appetite: medium.
