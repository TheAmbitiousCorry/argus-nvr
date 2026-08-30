import type { Camera, CameraStatus, DiscoveredCamera, NewCamera } from '@/types'

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

  health: (signal?: AbortSignal) => request<unknown>('/healthz', { signal }),

  streamUrl: (id: string, nonce?: number | string) =>
    withNonce(`/api/cameras/${encodeURIComponent(id)}/stream`, nonce),

  snapshotUrl: (id: string, nonce?: number | string) =>
    withNonce(`/api/cameras/${encodeURIComponent(id)}/snapshot`, nonce),
}
