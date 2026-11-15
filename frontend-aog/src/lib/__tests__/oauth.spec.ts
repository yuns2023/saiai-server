import { describe, expect, it } from 'vitest'
import { isSocialProvider, oauthStartUrl, parseOAuthFragment, takeOAuthFragment } from '../oauth'

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
  it('reads tokens and keeps a safe redirect', () => {
    expect(parseOAuthFragment('#access_token=at&refresh_token=rt&expires_in=900&token_type=Bearer&redirect=%2Fusage', origin)).toEqual({
      kind: 'tokens',
      accessToken: 'at',
      refreshToken: 'rt',
      expiresIn: 900,
      redirect: '/usage'
    })
  })

  it('drops unsafe redirects', () => {
    for (const bad of ['//evil.example', '/\\evil.example', 'https://evil.example/x', '/a\r\nb', 'keys']) {
      const r = parseOAuthFragment(`access_token=at&refresh_token=rt&redirect=${encodeURIComponent(bad)}`, origin)
      expect(r).toMatchObject({ kind: 'tokens', redirect: null })
    }
  })

  it('recognizes two-step verification without tokens', () => {
    expect(parseOAuthFragment('#requires_2fa=1&temp_token=tmp&redirect=%2Fkeys&provider=github', origin)).toEqual({
      kind: 'twoFactor',
      tempToken: 'tmp',
      redirect: '/keys'
    })
  })

  it('recognizes a pending registration and validates the handle', () => {
    expect(parseOAuthFragment(`#error=invitation_required&pending_oauth_token=${pending}&redirect=%2F`, origin)).toEqual({
      kind: 'invitation',
      pendingToken: pending,
      redirect: '/'
    })
    expect(parseOAuthFragment('#error=invitation_required&pending_oauth_token=short', origin)).toEqual({ kind: 'error', code: 'missing', reason: '' })
  })

  it('reports errors with the backend reason', () => {
    expect(parseOAuthFragment('#error=login_failed&error_message=USER_NOT_ACTIVE', origin)).toEqual({
      kind: 'error',
      code: 'login_failed',
      reason: 'USER_NOT_ACTIVE'
    })
    expect(parseOAuthFragment('', origin)).toEqual({ kind: 'error', code: 'missing', reason: '' })
    expect(parseOAuthFragment('#access_token=at', origin).kind).toBe('error')
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
