/** A camera as the backend lists it. */
export interface Camera {
  id: string
  name: string
  address: string
  user?: string
  /** Absent only in the moment between adding a camera and the first poll. */
  status?: CameraStatusEnvelope
  /**
   * What the camera last said about its own image. Absent until it answers.
   * Read it through `cameraFirmware()` rather than from here: the backend has
   * carried per-camera readings inside the status envelope before, and one
   * accessor is cheaper than finding out the hard way which one it is today.
   */
  firmware?: CameraFirmware
}

/**
 * What the backend knows about a camera: whether it answered, when it was last
 * asked, and whatever the firmware said when it did.
 */
export interface CameraStatusEnvelope {
  online: boolean
  checkedAt: string
  /** Absent while the camera is unreachable. */
  record?: CameraStatus
  /** How many people the backend is currently relaying this camera to. */
  viewers?: number
  error?: string
  /** See Camera.firmware: read through `cameraFirmware()`, not from either place directly. */
  firmware?: CameraFirmware
  /** The service is recording this camera's stream itself, because the camera cannot. */
  standIn?: boolean
  /** A recording is being pulled off this camera's card, which is why checkedAt has stopped. */
  fetching?: boolean
  pulledAt?: string
  pullError?: string
}

/**
 * Mirrors the camera firmware's /record document, which the backend passes
 * through untouched. Fields the firmware gains appear here without the backend
 * changing, so anything added since this was written is optional rather than
 * missing.
 */
export interface CameraStatus {
  active: boolean
  frames: number
  fps: number
  triggered: boolean
  motion: boolean
  armed: boolean
  change: number
  threshold: number
  preFrames: number
  preSecs: number
  /** Timing, in milliseconds per frame, while recording. */
  grabMs?: number
  writeMs?: number
  indexMs?: number
  /**
   * Whether the camera can keep what it records. A camera reporting anything
   * but `ok` still reports its triggers, and the service records those from the
   * stream it already holds.
   */
  storage?: 'ok' | 'missing' | 'unwritable'
  /** True while a recording is running that the camera itself cannot write. */
  cardless?: boolean
  /** Scene brightness out of 255, and where auto exposure has settled. */
  lux?: number
  rung?: number
  ael?: number
  gc?: number
}

/**
 * One mDNS responder the service found. These are candidates, not cameras: a
 * router advertises _http._tcp too.
 *
 * The field names are the service's, checked against what it actually sends.
 * This carried an `address` that the service has never sent, and reading it
 * threw the moment discovery returned anything at all, which was hidden for as
 * long as discovery returned nothing.
 */
export interface DiscoveredCamera {
  name: string
  /** The advertised hostname, ending .local. */
  host: string
  ip: string
  port: number
  /**
   * Whether the responder said it is a camera. Every web server advertises the
   * same service, so without this the list offers routers and printers as
   * cameras to add.
   */
  camera: boolean
  /** The firmware it advertised, when it said. */
  firmware?: string
  seen: string
}

export interface NewCamera {
  address: string
  name: string
  user: string
  pass: string
}

/** What a tile's dot is telling you. */
export type CameraState = 'recording' | 'watching' | 'offline'

/**
 * The camera's own /config document, passed through by the backend unchanged.
 * Every field is optional: firmware that gains a setting must show up here
 * without the backend or these types changing, and a camera on older firmware
 * simply omits what it does not have. The index signature is what carries a
 * field this file has never heard of.
 */
export interface CameraConfig {
  camname?: string
  tz?: string
  ssid?: string
  apwin?: boolean

  moten?: boolean
  motsens?: number
  recsec?: number
  presec?: number
  quietsec?: number
  keepfree?: number

  schen?: boolean
  schfrom?: number
  schto?: number
  /** Bitmask of the days POST /recording names one at a time as schday=<0-6>. */
  schdays?: number

  fsize?: number
  jq?: number

  autoimg?: boolean
  ael?: number
  gc?: number
  bri?: number
  con?: number
  sat?: number
  wb?: number
  gray?: boolean
  hmir?: boolean
  vflip?: boolean
  flashlvl?: number

  /** What the sensor is actually set to, which under auto exposure is not what is stored. */
  aelnow?: number
  gcnow?: number
  /** Comma-separated controls this sensor refused, empty when it accepted them all. */
  unsupported?: string

  [key: string]: unknown
}

/** A partial patch: only the named fields change on the camera. */
export type SettingsPatch = Record<string, string | number | boolean>

export interface SettingsRequest {
  cameraIds: string[]
  image?: SettingsPatch
  recording?: SettingsPatch
}

/** One entry per camera asked. A camera that fails does not stop the others. */
export interface BulkResult {
  cameraId: string
  ok: boolean
  error?: string
  /** Firmware only: bytes accepted by the camera. */
  bytes?: number
}

export interface BulkResponse {
  results: BulkResult[]
}

/**
 * What a camera reports from its own /version, passed through by the backend
 * unchanged, so a field the firmware gains needs no change here. Absent when
 * the camera has not answered yet.
 */
export interface CameraFirmware {
  version?: string
  built?: string
  /** Which OTA slot is running, as "1/1". */
  slot?: string
  /** An image on trial reverts to the one before it if the camera reboots. */
  onTrial?: boolean
  /** The version this one replaced after a rollback. Empty when there was none. */
  rolledBackFrom?: string
  [key: string]: unknown
}

/**
 * One recording the service holds. The path is the identity: camera, day and
 * start time are what every other route is asked for.
 */
export interface Recording {
  cameraId: string
  day: string
  /** HHMMSS, the last segment of the recording's path. */
  at: string
  /**
   * The camera's clock, with no timezone on it. The service does not know what
   * that clock is set to, so nothing here converts it.
   */
  startedAt: string
  durMs: number
  bytes: number
  /** Absent when the camera's listing did not say. */
  frames?: number
  /**
   * `camera` is a copy of something that also exists on a card. `service` is
   * the only copy there is, recorded from the stream because the camera could
   * not write it.
   */
  source: 'camera' | 'service'
  /** This service's clock, which does carry a zone. */
  heldAt: string
}

export interface RecordingsPage {
  recordings: Recording[]
  start: number
  /** Absent rather than false on the last page, so read it as optional. */
  more?: boolean
}

/** One camera's holdings for one day, so a date can be offered before it is asked for. */
export interface RecordingDay {
  cameraId: string
  day: string
  recordings: number
  bytes: number
}

export interface RecordingDays {
  days: RecordingDay[]
}

/** The frame index, so a scrubber knows where it can land. */
export interface RecordingFrames {
  frames: number
  durMs: number
  width?: number
  height?: number
  /** Milliseconds from the start of the recording, one per frame. */
  times: number[]
}
