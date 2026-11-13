/**
 * Axios client for /api/v1.
 * - unwraps the { code, message, data } envelope
 * - attaches the bearer token, locale and timezone (GET)
 * - on 401 refreshes once (single flight) and replays queued requests
 */
import axios, { type AxiosError, type AxiosInstance, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios'
import { getLocale } from '@/i18n'
import { timezone } from '@/lib/format'
import { clearSession, getRefreshToken, getToken, readUser, saveTokens } from './session'

export const API_BASE_URL = import.meta.env.VITE_API_BASE_URL || '/api/v1'

export interface ApiError {
  status: number
  code?: string | number
  message: string
  error?: unknown
}

export const api: AxiosInstance = axios.create({
  baseURL: API_BASE_URL,
  timeout: 30000,
  headers: { 'Content-Type': 'application/json' }
})

let refreshing: Promise<string> | null = null

/** Which session sent a request, so a late 401 is never replayed as someone else. */
type SessionTagged = InternalAxiosRequestConfig & { _retry?: boolean; _sentToken?: string | null; _sentUser?: number | null }

function storedUserId(): number | null {
  return readUser<{ id?: number }>()?.id ?? null
}

class SessionChanged extends Error {
  readonly code = 'SESSION_EXPIRED'
}

api.interceptors.request.use((config: InternalAxiosRequestConfig) => {
  const tagged = config as SessionTagged
  if (tagged._retry) {
    // A replay keeps the identity it was approved for. If the stored session
    // moved on (sign-out, other account) since, it must not go out at all.
    if (storedUserId() !== tagged._sentUser || getToken() !== tagged._sentToken) throw new SessionChanged('session changed')
  } else {
    const token = getToken()
    if (token) config.headers.Authorization = `Bearer ${token}`
    tagged._sentToken = token
    tagged._sentUser = storedUserId()
  }
  config.headers['Accept-Language'] = getLocale()
  if (config.method === 'get') {
    config.params = { ...(config.params || {}), timezone: timezone() }
  }
  return config
})

api.interceptors.response.use(
  (response: AxiosResponse) => {
    const body = response.data as { code?: number; message?: string; data?: unknown } | undefined
    if (body && typeof body === 'object' && 'code' in body) {
      if (body.code === 0) {
        response.data = body.data
      } else {
        return Promise.reject({ status: response.status, code: body.code, message: body.message || 'Unknown error' } satisfies ApiError)
      }
    }
    return response
  },
  async (error: AxiosError) => {
    if (error.code === 'ERR_CANCELED' || axios.isCancel(error)) return Promise.reject(error)
    if (error instanceof SessionChanged) return Promise.reject(SESSION_EXPIRED)
    if (!error.response) {
      return Promise.reject({ status: 0, code: 'NETWORK', message: 'network' } satisfies ApiError)
    }

    const original = error.config as SessionTagged | undefined
    const { status } = error.response
    const data = (typeof error.response.data === 'object' && error.response.data !== null ? error.response.data : {}) as Record<string, any>
    const url = String(original?.url || '')
    const isAuthEndpoint = /\/auth\/(login|register|refresh)/.test(url)

    if (status === 401 && original && !isAuthEndpoint) {
      return handleUnauthorized(original)
    }

    return Promise.reject({
      status,
      code: data.code,
      error: data.error,
      message: data.message || data.detail || error.message
    } satisfies ApiError)
  }
)

const SESSION_EXPIRED: ApiError = { status: 401, code: 'SESSION_EXPIRED', message: 'session_expired' }

/**
 * 401 handling bound to the session that sent the request:
 * - same session: refresh once and replay; if that fails, sign out;
 * - same account, token rotated elsewhere (other tab / classic console): replay once with it;
 * - different account or signed out since: reject only — never replay, never clear the new session.
 */
async function handleUnauthorized(req: SessionTagged): Promise<unknown> {
  const sameAccount = () => storedUserId() !== null && storedUserId() === req._sentUser
  const replay = (token: string) => {
    req._retry = true
    req._sentToken = token
    req.headers.Authorization = `Bearer ${token}`
    return api(req)
  }

  const current = getToken()
  if (current !== req._sentToken) {
    if (current && !req._retry && sameAccount()) return replay(current)
    return Promise.reject(SESSION_EXPIRED)
  }
  if (!req._retry && getRefreshToken()) {
    try {
      const fresh = await refreshAccessToken()
      if (!sameAccount()) return Promise.reject(SESSION_EXPIRED)
      return replay(fresh)
    } catch {
      const now = getToken()
      if (now && now !== req._sentToken && sameAccount()) return replay(now)
    }
  }
  // Still the session that failed: it is gone.
  if (getToken() === req._sentToken) expireSession()
  return Promise.reject(SESSION_EXPIRED)
}

/** Single-flight refresh shared by the interceptor and the proactive timer. */
export function refreshAccessToken(): Promise<string> {
  if (refreshing) return refreshing
  const refreshToken = getRefreshToken()
  if (!refreshToken) return Promise.reject(new Error('no refresh token'))
  refreshing = axios
    .post(`${API_BASE_URL}/auth/refresh`, { refresh_token: refreshToken }, { headers: { 'Content-Type': 'application/json' } })
    .then((res) => {
      const body = res.data as { code: number; data?: { access_token: string; refresh_token: string; expires_in: number } }
      if (body.code !== 0 || !body.data) throw new Error('refresh failed')
      // Signed out (or another tab rotated the session) while this was in
      // flight: do not write a session back into storage.
      if (getRefreshToken() !== refreshToken) throw new Error('session changed')
      saveTokens(body.data.access_token, body.data.refresh_token, body.data.expires_in)
      return body.data.access_token
    })
    .finally(() => {
      refreshing = null
    })
  return refreshing
}

/** Pages that stay put when a stale session is cleared. */
const PUBLIC_PATHS = ['/', '/login', '/register', '/forgot-password', '/reset-password', '/key-usage']

/** Fired after the stored session is cleared; the auth store drops its in-memory copy. */
export const SESSION_EXPIRED_EVENT = 'aog:session-expired'

function expireSession(): void {
  const hadSession = !!getToken()
  clearSession()
  window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT))
  if (hadSession) sessionStorage.setItem('auth_expired', '1')
  const here = window.location.pathname
  if (!PUBLIC_PATHS.includes(here)) {
    const back = encodeURIComponent(`${here}${window.location.search}`)
    window.location.href = `/login?redirect=${back}`
  }
}

export function errorMessage(err: unknown, fallback: string): string {
  const e = err as Partial<ApiError> | undefined
  if (e && typeof e.message === 'string' && e.message && e.message !== 'network') return e.message
  return fallback
}
