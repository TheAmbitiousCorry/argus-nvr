import type {
  BulkResponse,
  Camera,
  CameraConfig,
  CameraStatus,
  DiscoveredCamera,
  NewCamera,
  Recording,
  RecordingDays,
  RecordingFrames,
  RecordingsPage,
  SettingsRequest,
} from '@/types'

/**
 * Empty in both dev and production: the Vite dev server proxies /api to the Go
 * backend, and in production the backend serves dist/ from the same origin.
 * Set VITE_API_BASE to point a dev build at a backend somewhere else.
 */
const BASE = import.meta.env.VITE_API_BASE ?? ''

export class ApiError extends Error {
  readonly status: number
  readonly url: string
  constructor(message: string, status: number, url: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.url = url
  }
}

/** True when the request never reached the backend at all. */
export function isOffline(err: unknown): boolean {
  return err instanceof ApiError && err.status === 0
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const url = BASE + path
  let res: Response
  try {
    res = await fetch(url, {
      ...init,
      headers: { Accept: 'application/json', ...(init?.headers ?? {}) },
    })
  } catch (cause) {
    throw new ApiError(
      cause instanceof Error && cause.name === 'AbortError'
        ? 'Request cancelled'
        : 'Cannot reach the NVR backend',
      0,
      url,
    )
  }

  if (!res.ok) {
    throw new ApiError(await readError(res), res.status, url)
  }
  if (res.status === 204) return undefined as T

  const body = await res.text()
  if (body.trim() === '') return undefined as T
  try {
    return JSON.parse(body) as T
  } catch {
    throw new ApiError('Backend sent a response that is not JSON', res.status, url)
  }
}

/** Backends report errors as JSON, as plain text, or not at all. Cope with each. */
async function readError(res: Response): Promise<string> {
  let body = ''
  try {
    body = (await res.text()).trim()
  } catch {
    /* body already consumed or connection died */
  }
  if (body.startsWith('{')) {
    try {
      const parsed = JSON.parse(body) as { error?: string; message?: string }
      const detail = parsed.error ?? parsed.message
      if (detail) return detail
    } catch {
      /* fall through to the raw body */
    }
  }
  if (body) return body.slice(0, 300)
  return `${res.status} ${res.statusText || 'request failed'}`
}

/**
 * An <img> element cannot carry a signal, so a stream or snapshot URL takes a
 * nonce instead: changing it forces a genuinely new connection rather than a
 * cached or half dead one.
 */
function withNonce(path: string, nonce?: number | string): string {
  return nonce === undefined ? BASE + path : `${BASE}${path}?t=${nonce}`
}

/**
 * A recording's path is its identity: camera, day and start time, in that
 * order. Every route that acts on one recording hangs off this.
 */
function recordingPath(cameraId: string, day: string, at: string): string {
  return `/api/recordings/${encodeURIComponent(cameraId)}/${encodeURIComponent(day)}/${encodeURIComponent(at)}`
}

function query(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '') search.set(key, String(value))
  }
  const out = search.toString()
  return out ? `?${out}` : ''
}

export const api = {
  listCameras: (signal?: AbortSignal) => request<Camera[]>('/api/cameras', { signal }),

  addCamera: (input: NewCamera) =>
    request<Camera | undefined>('/api/cameras', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    }),

  removeCamera: (id: string) =>
    request<void>(`/api/cameras/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  cameraStatus: (id: string, signal?: AbortSignal) =>
    request<CameraStatus>(`/api/cameras/${encodeURIComponent(id)}/status`, { signal }),

  listDiscovered: (signal?: AbortSignal) =>
    request<DiscoveredCamera[]>('/api/discovered', { signal }),

  /** The camera's whole /config document. 502 when the camera did not answer. */
  cameraConfig: (id: string, signal?: AbortSignal) =>
    request<CameraConfig>(`/api/cameras/${encodeURIComponent(id)}/config`, { signal }),

  /**
   * Applies a partial patch to several cameras at once. Always 200, with one
   * result per camera, so read `results` rather than trusting the status.
   */
  applySettings: (input: SettingsRequest, signal?: AbortSignal) =>
    request<BulkResponse>('/api/settings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
      signal,
    }),

  health: (signal?: AbortSignal) => request<unknown>('/healthz', { signal }),

  streamUrl: (id: string, nonce?: number | string) =>
    withNonce(`/api/cameras/${encodeURIComponent(id)}/stream`, nonce),

  snapshotUrl: (id: string, nonce?: number | string) =>
    withNonce(`/api/cameras/${encodeURIComponent(id)}/snapshot`, nonce),

  /** Everything held, newest first. 404 when the service has no recordings directory. */
  listRecordings: (
    params: { cameraId?: string; day?: string; start?: number; limit?: number } = {},
    signal?: AbortSignal,
  ) => request<RecordingsPage>(`/api/recordings${query(params)}`, { signal }),

  /** The days that hold something, so a date can be offered without paging the lot. */
  recordingDays: (cameraId?: string, signal?: AbortSignal) =>
    request<RecordingDays>(`/api/recordings/days${query({ cameraId })}`, { signal }),

  /** Per-frame times, so a scrubber knows where it can land. */
  recordingFrames: (cameraId: string, day: string, at: string, signal?: AbortSignal) =>
    request<RecordingFrames>(`${recordingPath(cameraId, day, at)}/frames`, { signal }),

  /** The AVI itself, for download. Ranges are answered, so a player can seek in it. */
  recordingUrl: (cameraId: string, day: string, at: string) =>
    BASE + recordingPath(cameraId, day, at),

  /**
   * The same recording replayed as multipart/x-mixed-replace, which an <img>
   * plays with no decoding in the page. This is the only way to watch one here:
   * they are MJPEG inside AVI, which no browser decodes, so a <video> pointed
   * at the download URL shows nothing at all.
   *
   * `from` is a frame number, so seeking does not replay from the beginning.
   * `speed` is 0.5, 1, 2 or 4; 0 sends frames as fast as they can be read.
   */
  recordingStreamUrl: (
    cameraId: string,
    day: string,
    at: string,
    opts: { from?: number; speed?: number; nonce?: number | string } = {},
  ) =>
    BASE +
    `${recordingPath(cameraId, day, at)}/stream` +
    query({ from: opts.from, speed: opts.speed, t: opts.nonce }),
}

/**
 * What to call a downloaded recording. Without this the browser names the file
 * after the URL's last segment, which is six digits and no camera.
 */
export function recordingFileName(rec: Recording, cameraName?: string): string {
  const who = (cameraName ?? rec.cameraId).replace(/[^\w.-]+/g, '-').replace(/^-+|-+$/g, '')
  return `${who || rec.cameraId}-${rec.day}-${rec.at}.avi`
}

/**
 * Sends firmware to one or more cameras.
 *
 * The upload is driven by XMLHttpRequest rather than fetch because it is the
 * only way to watch the request body leave the browser, and a firmware image
 * over a slow link is the one place in this app where that matters.
 */
export function uploadFirmware(opts: {
  file: File
  cameraIds: string[]
  onProgress?: (fraction: number) => void
  signal?: AbortSignal
}): Promise<BulkResponse> {
  const url = `${BASE}/api/firmware`
  return new Promise((resolve, reject) => {
    const form = new FormData()
    form.append('file', opts.file, opts.file.name)
    form.append('cameraIds', opts.cameraIds.join(','))

    const xhr = new XMLHttpRequest()
    xhr.open('POST', url)
    xhr.setRequestHeader('Accept', 'application/json')

    xhr.upload.addEventListener('progress', (ev) => {
      if (ev.lengthComputable && ev.total > 0) opts.onProgress?.(ev.loaded / ev.total)
    })

    xhr.addEventListener('load', () => {
      if (xhr.status < 200 || xhr.status >= 300) {
        reject(new ApiError(errorFromText(xhr.responseText, xhr.status, xhr.statusText), xhr.status, url))
        return
      }
      try {
        resolve(JSON.parse(xhr.responseText) as BulkResponse)
      } catch {
        reject(new ApiError('Backend sent a response that is not JSON', xhr.status, url))
      }
    })
    xhr.addEventListener('error', () =>
      reject(new ApiError('Cannot reach the NVR backend', 0, url)),
    )
    xhr.addEventListener('abort', () => reject(new ApiError('Upload cancelled', 0, url)))

    opts.signal?.addEventListener('abort', () => xhr.abort(), { once: true })
    xhr.send(form)
  })
}

function errorFromText(body: string, status: number, statusText: string): string {
  const trimmed = (body ?? '').trim()
  if (trimmed.startsWith('{')) {
    try {
      const parsed = JSON.parse(trimmed) as { error?: string; message?: string }
      const detail = parsed.error ?? parsed.message
      if (detail) return detail
    } catch {
      /* fall through to the raw body */
    }
  }
  if (trimmed) return trimmed.slice(0, 300)
  return `${status} ${statusText || 'request failed'}`
}
