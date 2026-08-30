/** A camera as the backend lists it. */
export interface Camera {
  id: string
  name: string
  address: string
  online: boolean
  /** Short summary string from the backend, if it sends one. */
  status?: string | null
}

/** Mirrors the camera firmware's /record document, relayed by the backend. */
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
