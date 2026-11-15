import { describe, expect, it } from 'vitest'
import { isSocialProvider, oauthStartUrl, outcomeFromExchange, parseOAuthFragment, takeOAuthFragment } from '../oauth'

const origin = 'https://aog.example'
const pending = 'a'.repeat(64)

describe('oauthStartUrl', () => {
  it('passes only safe same-origin redirects', () => {
    expect(oauthStartUrl('google', '/keys?x=1', origin)).toBe('/api/v1/auth/oauth/google/start?redirect=%2Fkeys%3Fx%3D1')
    for (const bad of ['//evil.example', '/\\evil.example', 'https://evil.example', 'javascript:alert(1)', '/a\r\nb', undefined, 3]) {
      expect(oauthStartUrl('github', bad, origin)).toBe('/api/v1/auth/oauth/github/start')
    }
  })
})

describe('parseOAuthFragment', () => {
  const code = 'c'.repeat(64)

  it('accepts only a one-time handoff code', () => {
    expect(parseOAuthFragment(`#code=${code}`)).toEqual({ kind: 'code', code })
  })

  it('never turns tokens or handles in the URL into a sign-in', () => {
    for (const forged of [
      '#access_token=at&refresh_token=rt&expires_in=900&token_type=Bearer',
      '#requires_2fa=1&temp_token=tmp',
      `#pending_oauth_token=${pending}`,
      `#code=${code}&access_token=at&refresh_token=rt`,
      `#code=${code}&code=${code}`,
      '#code=short',
      `#code=${code.toUpperCase()}`,
      ''
    ]) {
      expect(parseOAuthFragment(forged)).toEqual({ kind: 'error', code: 'missing', reason: '' })
    }
  })

  it('reports errors with the backend reason', () => {
    expect(parseOAuthFragment('#error=login_failed&error_message=USER_NOT_ACTIVE')).toEqual({
      kind: 'error',
      code: 'login_failed',
      reason: 'USER_NOT_ACTIVE'
    })
  })
})

describe('outcomeFromExchange', () => {
  it('maps the server-verified outcome and keeps only safe redirects', () => {
    expect(outcomeFromExchange({ kind: 'tokens', access_token: 'at', refresh_token: 'rt', expires_in: 900, redirect: '/usage' }, origin)).toEqual({
      kind: 'tokens',
      accessToken: 'at',
      refreshToken: 'rt',
      expiresIn: 900,
      redirect: '/usage'
    })
    for (const bad of ['//evil.example', '/\\evil.example', 'https://evil.example/x', '/a\r\nb', 'keys']) {
      expect(outcomeFromExchange({ kind: 'tokens', access_token: 'at', refresh_token: 'rt', redirect: bad }, origin)).toMatchObject({ redirect: null })
    }
    expect(outcomeFromExchange({ kind: 'two_factor', temp_token: 'tmp', redirect: '/keys' }, origin)).toEqual({ kind: 'twoFactor', tempToken: 'tmp', redirect: '/keys' })
    expect(outcomeFromExchange({ kind: 'invitation', pending_oauth_token: pending, redirect: '/' }, origin)).toEqual({ kind: 'invitation', pendingToken: pending, redirect: '/' })
    expect(outcomeFromExchange({ kind: 'invitation', pending_oauth_token: 'short' }, origin).kind).toBe('error')
    expect(outcomeFromExchange({ kind: 'tokens', access_token: 'at' }, origin).kind).toBe('error')
  })
})

describe('takeOAuthFragment', () => {
  it('returns the hash and removes it from the address bar', () => {
    window.history.replaceState(null, '', '/auth/oauth/google/callback#access_token=secret')
    expect(takeOAuthFragment()).toBe('#access_token=secret')
    expect(window.location.hash).toBe('')
    expect(window.location.href).not.toContain('secret')
  })
})

describe('isSocialProvider', () => {
  it('accepts only the supported providers', () => {
    expect(isSocialProvider('google')).toBe(true)
    expect(isSocialProvider('github')).toBe(true)
    expect(isSocialProvider('linuxdo')).toBe(false)
  })
})

describe('captureOAuthFragmentAtStartup', () => {
  it('clears the fragment on the callback page and hands it to the view once', async () => {
    const { captureOAuthFragmentAtStartup, takeOAuthFragment: take } = await import('../oauth')
    window.history.replaceState(null, '', '/auth/oauth/github/callback#access_token=early')
    captureOAuthFragmentAtStartup()
    expect(window.location.hash).toBe('')
    expect(take()).toBe('#access_token=early')
    expect(take()).toBe('')
  })

  it('is idempotent before the view takes the fragment', async () => {
    const { captureOAuthFragmentAtStartup, takeOAuthFragment: take } = await import('../oauth')
    window.history.replaceState(null, '', '/auth/oauth/google/callback#access_token=once')
    captureOAuthFragmentAtStartup()
    captureOAuthFragmentAtStartup()
    expect(take()).toBe('#access_token=once')
  })

  it('leaves other pages alone', async () => {
    const { captureOAuthFragmentAtStartup } = await import('../oauth')
    window.history.replaceState(null, '', '/billing#section')
    captureOAuthFragmentAtStartup()
    expect(window.location.hash).toBe('#section')
  })
})
