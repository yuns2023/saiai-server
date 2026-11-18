import { describe, expect, it } from 'vitest'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import {
  checkDisplayName,
  checkProxy,
  emptyProxy,
  normalizeAuthCode,
  ONBOARD_ERROR_CODES,
  onboardCorrelationId,
  onboardErrorCode,
  onboardErrorKey,
  proxyPayload,
  submitNeedsNewLink,
  type ProxyForm
} from '../selfOnboard'

function proxy(over: Partial<ProxyForm> = {}): ProxyForm {
  return { ...emptyProxy(), host: 'proxy.example.com', port: '1080', ...over }
}

describe('checkProxy', () => {
  it('accepts a blank form only when the proxy is optional', () => {
    expect(checkProxy(emptyProxy(), false)).toEqual({})
    expect(checkProxy(emptyProxy(), true)).toEqual({ host: 'proxyRequired' })
  })

  it('accepts domains, IPv4 and IPv6 hosts with a valid port', () => {
    expect(checkProxy(proxy(), true)).toEqual({})
    expect(checkProxy(proxy({ host: '203.0.113.7', protocol: 'http' }), true)).toEqual({})
    expect(checkProxy(proxy({ host: '[2001:db8::1]', protocol: 'socks5' }), true)).toEqual({})
    expect(checkProxy(proxy({ port: '65535' }), false)).toEqual({})
    expect(checkProxy(proxy({ port: '1' }), false)).toEqual({})
  })

  it('rejects hosts with a scheme, path, credentials, spaces or control characters', () => {
    for (const host of ['socks5://proxy.example.com', 'proxy.example.com/x', 'user@proxy.example.com', 'a b', 'a\u0000b', 'x'.repeat(254)]) {
      expect(checkProxy(proxy({ host }), false).host, host).toBe('hostInvalid')
    }
    expect(checkProxy(proxy({ host: '  ' , port: '1080' }), false).host).toBe('hostRequired')
  })

  it('rejects ports outside 1..65535 or not whole numbers', () => {
    for (const port of ['0', '65536', '-1', '1.5', 'abc', '123456']) {
      expect(checkProxy(proxy({ port }), false).port, port).toBe('portInvalid')
    }
    expect(checkProxy(proxy({ port: '' }), false).port).toBe('portRequired')
  })

  it('needs username and password together', () => {
    expect(checkProxy(proxy({ username: 'u' }), false).auth).toBe('authPair')
    expect(checkProxy(proxy({ password: 'p' }), false).auth).toBe('authPair')
    expect(checkProxy(proxy({ username: 'u', password: 'p' }), false)).toEqual({})
  })

  it('limits credentials to 100 characters and 255 bytes without control characters', () => {
    expect(checkProxy(proxy({ username: 'u'.repeat(100), password: 'p' }), false)).toEqual({})
    expect(checkProxy(proxy({ username: 'u'.repeat(101), password: 'p' }), false).username).toBe('credInvalid')
    // 4-byte characters: 64 of them = 256 bytes, under 100 characters.
    expect(checkProxy(proxy({ username: 'u', password: '😀'.repeat(64) }), false).password).toBe('credInvalid')
    expect(checkProxy(proxy({ username: 'u\nx', password: 'p' }), false).username).toBe('credInvalid')
    // 86 three-byte characters = 258 bytes.
    expect(checkProxy(proxy({ username: 'u', password: '密'.repeat(86) }), false).password).toBe('credInvalid')
    expect(checkProxy(proxy({ username: 'u', password: '密'.repeat(85) }), false)).toEqual({})
  })

  it('rejects an unknown protocol', () => {
    expect(checkProxy(proxy({ protocol: 'ftp' as ProxyForm['protocol'] }), false).protocol).toBe('protocolInvalid')
  })
})

describe('proxyPayload', () => {
  it('is undefined for a blank form and strips IPv6 brackets', () => {
    expect(proxyPayload(emptyProxy())).toBeUndefined()
    expect(proxyPayload(proxy({ host: ' [2001:db8::1] ', port: ' 8080 ', protocol: 'http' }))).toEqual({ protocol: 'http', host: '2001:db8::1', port: 8080 })
  })

  it('sends credentials only as a pair', () => {
    expect(proxyPayload(proxy({ username: 'u', password: 'p' }))).toEqual({ protocol: 'socks5h', host: 'proxy.example.com', port: 1080, username: 'u', password: 'p' })
  })
})

describe('checkDisplayName', () => {
  it('requires 1..64 characters without control characters', () => {
    expect(checkDisplayName('  ')).toBe('nameRequired')
    expect(checkDisplayName('我的 Max 号')).toBe('')
    expect(checkDisplayName('号'.repeat(64))).toBe('')
    expect(checkDisplayName('号'.repeat(65))).toBe('nameTooLong')
    expect(checkDisplayName('a\tb')).toBe('nameInvalid')
    // Zero-width space / joiner, bidi override and BOM are invisible format characters.
    for (const ch of ['\u200b', '\u200d', '\u202e', '\ufeff']) expect(checkDisplayName(`a${ch}b`), ch).toBe('nameInvalid')
  })
})

describe('normalizeAuthCode', () => {
  it('accepts code#state and trims it', () => {
    expect(normalizeAuthCode('  abc123#st4te \n')).toBe('abc123#st4te')
    const long = 'c'.repeat(3500) + '#' + 's'.repeat(512)
    expect(normalizeAuthCode(long)).toBe(long)
  })

  it('turns a pasted callback URL into code#state', () => {
    expect(normalizeAuthCode('https://console.anthropic.com/oauth/code/callback?code=abc&state=xyz')).toBe('abc#xyz')
  })

  it('rejects anything else', () => {
    for (const raw of ['', 'abc', '#xyz', 'abc#', 'a b#c', 'a#b#c', 'https://example.com/?code=abc', 'ab\u00e9#xyz', 'abc#' + 's'.repeat(513), 'c'.repeat(4000) + '#' + 's'.repeat(100)]) {
      expect(normalizeAuthCode(raw), raw).toBe('')
    }
  })
})

describe('onboardCorrelationId', () => {
  it('reads metadata.correlation_id and ignores anything odd', () => {
    expect(onboardCorrelationId({ metadata: { correlation_id: '9f2c4e1ab03d7781' } })).toBe('9f2c4e1ab03d7781')
    expect(onboardCorrelationId({ metadata: { correlation_id: '<script>' } })).toBe('')
    expect(onboardCorrelationId({ metadata: {} })).toBe('')
    expect(onboardCorrelationId(null)).toBe('')
  })
})

describe('error codes', () => {
  it('reads the code from `reason` (HTTP errors) or a string `code`', () => {
    expect(onboardErrorCode({ status: 409, code: 409, reason: 'SELF_ONBOARD_ALREADY_HOSTED', message: 'x' })).toBe('SELF_ONBOARD_ALREADY_HOSTED')
    expect(onboardErrorCode({ status: 400, code: 'SELF_ONBOARD_PROXY_INVALID' })).toBe('SELF_ONBOARD_PROXY_INVALID')
    expect(onboardErrorCode({ status: 400, reason: 'SOMETHING_ELSE' })).toBeNull()
    expect(onboardErrorCode(undefined)).toBeNull()
  })

  it('maps every code to a Chinese and English message', () => {
    for (const code of ONBOARD_ERROR_CODES) {
      expect(onboardErrorKey({ status: 400, reason: code })).toBe(code)
      expect(zh.onboard.errors[code], code).toBeTruthy()
      expect(en.onboard.errors[code], code).toBeTruthy()
    }
    expect(zh.onboard.errors.SELF_ONBOARD_ALREADY_HOSTED).toBe('该 Claude 号已在托管中或审核中，请联系管理员。')
    expect(onboardErrorKey({ status: 0, code: 'NETWORK' })).toBe('network')
    expect(onboardErrorKey({ status: 429 })).toBe('SELF_ONBOARD_RATE_LIMITED')
    expect(onboardErrorKey({ status: 500, message: 'upstream said something' })).toBe('generic')
  })

  it('keeps the link only for errors raised before the link is used', () => {
    for (const code of ['SELF_ONBOARD_PROXY_REQUIRED', 'SELF_ONBOARD_PROXY_INVALID', 'SELF_ONBOARD_PROXY_UNAVAILABLE', 'SELF_ONBOARD_RATE_LIMITED', 'SELF_ONBOARD_INVALID_INPUT']) {
      expect(submitNeedsNewLink({ reason: code }), code).toBe(false)
    }
    for (const code of ['SELF_ONBOARD_SESSION_INVALID', 'SELF_ONBOARD_EXCHANGE_FAILED', 'SELF_ONBOARD_PENDING_LIMIT', 'SELF_ONBOARD_ALREADY_HOSTED', 'SELF_ONBOARD_UNAVAILABLE']) {
      expect(submitNeedsNewLink({ reason: code }), code).toBe(true)
    }
    expect(submitNeedsNewLink({ status: 0 })).toBe(true)
  })
})
