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
