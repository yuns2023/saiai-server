import { describe, expect, it } from 'vitest'
import { isMarketTierEligible, marketErrorCode, marketErrorText, marketPlanOptions, planTiers, validCutRate, validIntIn, validMarketPlan, validMinConvert } from '../market'

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

  it('plan slug: the server rule ^[a-z0-9][a-z0-9_-]{0,31}$', () => {
    for (const ok of ['pro', 'max', 'ultra', 'max-2', 'a_b', '0', 'a'.repeat(32)]) expect(validMarketPlan(ok), ok).toBe(true)
    for (const bad of ['', 'Max', '-max', '_max', 'max plan', 'a'.repeat(33), ' max', null, 3]) expect(validMarketPlan(bad), String(bad)).toBe(false)
  })

  it('plan choices: distinct plans of active market tiers (per platform), sorted', () => {
    const g = (id: number, over: Record<string, unknown>) => ({ id, name: `g${id}`, platform: 'anthropic', status: 'active', rate_multiplier: 1, market_tier: true, market_plan: 'max', ...over }) as never
    const groups = [
      g(1, {}),
      g(2, { market_plan: 'pro' }),
      g(3, {}),
      g(4, { status: 'inactive', market_plan: 'ultra' }),
      g(5, { market_plan: null }),
      g(6, { market_tier: false, market_plan: 'stale' }),
      g(7, { platform: 'openai', market_plan: 'oai' })
    ]
    expect(marketPlanOptions(groups, 'anthropic')).toEqual(['max', 'pro'])
    expect(marketPlanOptions(groups)).toEqual(['max', 'oai', 'pro'])
    expect(planTiers(groups, 'anthropic').map((x: { id: number }) => x.id)).toEqual([1, 2, 3])
  })

  it('maps the plan errors', () => {
    expect(marketErrorText({ reason: 'MARKET_PLAN_REQUIRES_TIER', message: 'x' }, t, 'f')).toBe('market.errors.MARKET_PLAN_REQUIRES_TIER')
    expect(marketErrorText({ reason: 'MARKET_PLAN_UNKNOWN', message: 'x' }, t, 'f')).toBe('market.errors.MARKET_PLAN_UNKNOWN')
    expect(marketErrorText({ reason: 'MARKET_PLAN_INVALID', message: 'x' }, t, 'f')).toBe('market.errors.MARKET_PLAN_INVALID')
  })
})
