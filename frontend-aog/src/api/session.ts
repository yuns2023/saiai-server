/**
 * Browser session storage contract shared with the classic SPA (admin console).
 * Same keys and formats as frontend/src/api/auth.ts + stores/auth.ts so a login
 * in either app is valid in the other. token_expires_at is epoch milliseconds.
 */
export const SESSION_KEYS = {
  token: 'auth_token',
  refresh: 'refresh_token',
  user: 'auth_user',
  expiresAt: 'token_expires_at'
} as const

export function getToken(): string | null {
  return localStorage.getItem(SESSION_KEYS.token)
}

export function getRefreshToken(): string | null {
  return localStorage.getItem(SESSION_KEYS.refresh)
}

export function getExpiresAt(): number | null {
  const raw = localStorage.getItem(SESSION_KEYS.expiresAt)
  if (!raw) return null
  const n = parseInt(raw, 10)
  return Number.isFinite(n) ? n : null
}

export function saveTokens(access: string, refresh?: string, expiresInSeconds?: number): void {
  localStorage.setItem(SESSION_KEYS.token, access)
  if (refresh) localStorage.setItem(SESSION_KEYS.refresh, refresh)
  if (expiresInSeconds) localStorage.setItem(SESSION_KEYS.expiresAt, String(Date.now() + expiresInSeconds * 1000))
}

export function saveUser(user: unknown): void {
  localStorage.setItem(SESSION_KEYS.user, JSON.stringify(user))
}

export function readUser<T>(): T | null {
  const raw = localStorage.getItem(SESSION_KEYS.user)
  if (!raw) return null
  try {
    return JSON.parse(raw) as T
  } catch {
    return null
  }
}

export function clearSession(): void {
  localStorage.removeItem(SESSION_KEYS.token)
  localStorage.removeItem(SESSION_KEYS.refresh)
  localStorage.removeItem(SESSION_KEYS.user)
  localStorage.removeItem(SESSION_KEYS.expiresAt)
}
