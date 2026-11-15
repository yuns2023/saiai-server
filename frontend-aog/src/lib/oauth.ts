/**
 * Google / GitHub sign-in helpers.
 *
 * The backend finishes the OAuth exchange and redirects to
 * /auth/oauth/<provider>/callback with the outcome in the URL fragment
 * (never sent to any server). The callback page reads it once, clears it
 * from the address bar and history, and acts on the parsed result.
 */
import { API_BASE_URL } from '@/api/client'
import { safeRedirectTarget } from './redirect'

export type SocialProvider = 'google' | 'github'

export const SOCIAL_PROVIDERS: readonly SocialProvider[] = ['google', 'github']

export function isSocialProvider(value: unknown): value is SocialProvider {
  return value === 'google' || value === 'github'
}

/** Full-page URL that starts sign-in; the redirect is kept only if it is a safe same-origin path. */
export function oauthStartUrl(provider: SocialProvider, redirect?: unknown, origin = window.location.origin): string {
  const base = `${API_BASE_URL}/auth/oauth/${provider}/start`
  const target = safeRedirectTarget(redirect, origin)
  return target ? `${base}?redirect=${encodeURIComponent(target)}` : base
}

export type OAuthCallbackResult =
  | { kind: 'tokens'; accessToken: string; refreshToken: string; expiresIn: number; redirect: string | null }
  | { kind: 'twoFactor'; tempToken: string; redirect: string | null }
  | { kind: 'invitation'; pendingToken: string; redirect: string | null }
  | { kind: 'error'; code: string; reason: string }

/** Parses the fragment handed over by the backend callback ("#a=1&b=2" or "a=1&b=2"). */
export function parseOAuthFragment(hash: string, origin = window.location.origin): OAuthCallbackResult {
  const params = new URLSearchParams(hash.startsWith('#') ? hash.slice(1) : hash)
  const redirect = safeRedirectTarget(params.get('redirect'), origin)
  const error = params.get('error') ?? ''

  if (error === 'invitation_required') {
    const pendingToken = params.get('pending_oauth_token') ?? ''
    if (/^[0-9a-f]{64}$/.test(pendingToken)) return { kind: 'invitation', pendingToken, redirect }
    return { kind: 'error', code: 'missing', reason: '' }
  }
  if (error) {
    return { kind: 'error', code: error, reason: params.get('error_message') ?? '' }
  }
  if (params.get('requires_2fa') === '1') {
    const tempToken = params.get('temp_token') ?? ''
    return tempToken ? { kind: 'twoFactor', tempToken, redirect } : { kind: 'error', code: 'missing', reason: '' }
  }
  const accessToken = params.get('access_token') ?? ''
  const refreshToken = params.get('refresh_token') ?? ''
  const expiresIn = Number.parseInt(params.get('expires_in') ?? '', 10)
  if (accessToken && refreshToken) {
    return { kind: 'tokens', accessToken, refreshToken, expiresIn: Number.isFinite(expiresIn) && expiresIn > 0 ? expiresIn : 0, redirect }
  }
  return { kind: 'error', code: 'missing', reason: '' }
}

/**
 * Reads the fragment once and removes it from the address bar and the
 * current history entry, so tokens are not left in history or reused on reload.
 */
export function takeOAuthFragment(win: Window = window): string {
  const hash = win.location.hash
  if (hash) {
    win.history.replaceState(win.history.state, '', `${win.location.pathname}${win.location.search}`)
  }
  return hash
}
