/** A camera as the backend lists it. */
export interface Camera {
  id: string
  name: string
  address: string
  user?: string
  /** Absent only in the moment between adding a camera and the first poll. */
  status?: CameraStatusEnvelope
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
  /** Scene brightness out of 255, and where auto exposure has settled. */
  lux?: number
  rung?: number
  ael?: number
  gc?: number
}

export interface DiscoveredCamera {
  name: string
  address: string
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
