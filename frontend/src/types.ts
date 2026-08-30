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
