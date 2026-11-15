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

/** What the URL fragment may carry: a one-time handoff code, or an error description. */
export type OAuthFragment = { kind: 'code'; code: string } | { kind: 'error'; code: string; reason: string }

const HANDOFF_CODE = /^[0-9a-f]{64}$/

/**
 * Parses the fragment set by the backend callback. Only `code=<handoff>` or `error=…` are
 * accepted; anything else (in particular tokens or handles put there by a third party) is
 * treated as an error and never used to sign in.
 */
export function parseOAuthFragment(hash: string): OAuthFragment {
  const params = new URLSearchParams(hash.startsWith('#') ? hash.slice(1) : hash)
  const error = params.get('error') ?? ''
  if (error) return { kind: 'error', code: error, reason: params.get('error_message') ?? '' }
  const keys = [...new Set(params.keys())]
  const code = params.get('code') ?? ''
  if (keys.length === 1 && keys[0] === 'code' && params.getAll('code').length === 1 && HANDOFF_CODE.test(code)) {
    return { kind: 'code', code }
  }
  return { kind: 'error', code: 'missing', reason: '' }
}

/** Shape of POST /auth/oauth/{provider}/exchange. */
export interface OAuthExchangeResponse {
  kind: 'tokens' | 'two_factor' | 'invitation'
  redirect?: string
  access_token?: string
  refresh_token?: string
  expires_in?: number
  temp_token?: string
  pending_oauth_token?: string
}

/** Turns the server-verified exchange response into a callback outcome. */
export function outcomeFromExchange(data: OAuthExchangeResponse, origin = window.location.origin): OAuthCallbackResult {
  const redirect = safeRedirectTarget(data.redirect, origin)
  if (data.kind === 'tokens' && data.access_token && data.refresh_token) {
    const expiresIn = typeof data.expires_in === 'number' && data.expires_in > 0 ? data.expires_in : 0
    return { kind: 'tokens', accessToken: data.access_token, refreshToken: data.refresh_token, expiresIn, redirect }
  }
  if (data.kind === 'two_factor' && data.temp_token) return { kind: 'twoFactor', tempToken: data.temp_token, redirect }
  if (data.kind === 'invitation' && data.pending_oauth_token && HANDOFF_CODE.test(data.pending_oauth_token)) {
    return { kind: 'invitation', pendingToken: data.pending_oauth_token, redirect }
  }
  return { kind: 'error', code: 'missing', reason: '' }
}

/** Fragment taken off the address bar at startup, waiting for the callback view. */
let captured: string | null = null

/**
 * Reads the fragment once and removes it from the address bar and the
 * current history entry, so tokens are not left in history or reused on reload.
 * A fragment captured earlier by captureOAuthFragmentAtStartup is returned first.
 */
export function takeOAuthFragment(win: Window = window): string {
  if (captured !== null) {
    const hash = captured
    captured = null
    return hash
  }
  const hash = win.location.hash
  if (hash) {
    win.history.replaceState(win.history.state, '', `${win.location.pathname}${win.location.search}`)
  }
  return hash
}


const CALLBACK_PATH = /^\/auth\/oauth\/(google|github)\/callback\/?$/

/**
 * Called before the app restores a session or waits for settings: on the
 * callback page, take the fragment off the address bar immediately and keep
 * it in memory for the callback view.
 */
export function captureOAuthFragmentAtStartup(win: Window = window): void {
  if (!CALLBACK_PATH.test(win.location.pathname) || !win.location.hash) return
  const hash = win.location.hash
  win.history.replaceState(win.history.state, '', `${win.location.pathname}${win.location.search}`)
  captured = hash
}
