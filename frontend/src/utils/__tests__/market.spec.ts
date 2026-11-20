import { describe, expect, it } from 'vitest'
import { firstBadProtection, isMarketTierEligible, marketErrorCode, marketErrorText, validCutRate, validIntIn, validMinConvert } from '../market'

const t = (key: string) => key

describe('market utils', () => {
  it('cut rate: 0..1, at most 4 decimals, 0 is a value', () => {
    expect(validCutRate(0)).toBe(true)
    expect(validCutRate(0.2)).toBe(true)
    expect(validCutRate(0.1234)).toBe(true)
    expect(validCutRate(1)).toBe(true)
    expect(validCutRate(0.12345)).toBe(false)
    expect(validCutRate(1.0001)).toBe(false)
    expect(validCutRate(-0.1)).toBe(false)
    expect(validCutRate('')).toBe(false)
    expect(validCutRate(NaN)).toBe(false)
    expect(validCutRate(1e-5)).toBe(false)
  })

  it('min convert: 0.01..10000 with 2 decimals; integers in range', () => {
    expect(validMinConvert(1)).toBe(true)
    expect(validMinConvert(0.01)).toBe(true)
    expect(validMinConvert(0)).toBe(false)
    expect(validMinConvert(1.005)).toBe(false)
    expect(validMinConvert(10000.01)).toBe(false)
    expect(validIntIn(0, 0, 90)).toBe(true)
    expect(validIntIn(1.5, 0, 90)).toBe(false)
    expect(validIntIn('', 0, 90)).toBe(false)
  })

  it('tier eligibility mirrors the server rule', () => {
    const ok = { platform: 'anthropic', subscription_type: 'standard', hosted_usage_enabled: false, rate_multiplier: 1.2, fallback_group_id: null }
    expect(isMarketTierEligible(ok)).toBe(true)
    expect(isMarketTierEligible({ ...ok, platform: 'openai' })).toBe(false)
    expect(isMarketTierEligible({ ...ok, subscription_type: 'subscription' })).toBe(false)
    expect(isMarketTierEligible({ ...ok, hosted_usage_enabled: true })).toBe(false)
    expect(isMarketTierEligible({ ...ok, rate_multiplier: 0 })).toBe(false)
    expect(isMarketTierEligible({ ...ok, rate_multiplier: 0.00004 })).toBe(false)
    expect(isMarketTierEligible({ ...ok, rate_multiplier: '' })).toBe(false)
    expect(isMarketTierEligible({ ...ok, fallback_group_id: 5 })).toBe(false)
  })

  it('maps tier errors to plain text and keeps the server bound for invalid params', () => {
    expect(marketErrorCode({ reason: 'MARKET_TIER_RATE_OVERRIDE' })).toBe('MARKET_TIER_RATE_OVERRIDE')
    expect(marketErrorCode({ code: 'MARKET_TIER_FALLBACK' })).toBe('MARKET_TIER_FALLBACK')
    expect(marketErrorCode({ reason: 'OTHER' })).toBe('')
    expect(marketErrorText({ reason: 'MARKET_TIER_FALLBACK', message: 'x' }, t, 'f')).toBe('market.errors.MARKET_TIER_FALLBACK')
    expect(marketErrorText({ reason: 'MARKET_INVALID_PARAMS', message: 'reserved_concurrency must be at most the account concurrency - 1' }, t, 'f')).toBe(
      'market.errors.MARKET_INVALID_PARAMS（reserved_concurrency must be at most the account concurrency - 1）'
    )
    expect(marketErrorText({ message: 'boom' }, t, 'f')).toBe('boom')
    expect(marketErrorText(null, t, 'f')).toBe('f')
  })

  it('protection: first field out of range', () => {
    const p = { reserved_concurrency: 0, reserved_devices: 0, reserved_admissions: 0 }
    expect(firstBadProtection(p)).toBeNull()
    expect(firstBadProtection({ ...p, reserved_devices: 33 })).toBe('reserved_devices')
    expect(firstBadProtection({ ...p, reserved_admissions: '' })).toBe('reserved_admissions')
  })
})
