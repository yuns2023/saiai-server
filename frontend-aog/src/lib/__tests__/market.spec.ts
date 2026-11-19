import { describe, expect, it } from 'vitest'
import type { OwnerMarketView } from '@/api/market'
import {
  canConvert,
  checkConvertAmount,
  errorReason,
  floorToCents,
  hasEarnings,
  KNOWN_REASONS,
  marketErrorKey,
  marketState,
  newRequestId,
  pickableTiers,
  reasonKey,
  units,
  usdExact,
  visibleReasons
} from '../market'
import zh from '@/i18n/locales/zh'

function view(over: Partial<OwnerMarketView> = {}): OwnerMarketView {
  return {
    account_id: 1,
    market_enabled: true,
    status: 'unlisted',
    selected_group_id: null,
    version: 1,
    allowed_tiers: [{ group_id: 3, name: 'T1', rate_multiplier: 1.2, available: true, admin_member: false }],
    protection: null,
    unavailable_reasons: [],
    can_list: true,
    ...over
  }
}

describe('market reasons', () => {
  it('maps every documented reason to its own text and anything else to the generic one', () => {
    for (const r of KNOWN_REASONS) {
      expect(reasonKey(r)).toBe(`market.reason_${r}`)
      expect((zh.market as Record<string, unknown>)[`reason_${r}`]).toBeTypeOf('string')
    }
    expect(reasonKey('needs_warmup_sample')).toBe('market.reason_other')
    expect(reasonKey('')).toBe('market.reason_other')
    expect(zh.market.reason_proxy_unavailable).toBe('代理不可用')
  })

  it('derives 未获准 / 可上架 / 已上架 / 暂不可售', () => {
    expect(marketState(view({ status: 'not_configured', can_list: false, unavailable_reasons: ['not_configured'] }))).toBe('notAllowed')
    expect(marketState(view({ status: 'revoked', can_list: false, unavailable_reasons: ['not_configured'] }))).toBe('notAllowed')
    expect(marketState(view())).toBe('canList')
    expect(marketState(view({ status: 'listed', selected_group_id: 3 }))).toBe('listed')
    expect(marketState(view({ can_list: false, unavailable_reasons: ['proxy_unavailable'] }))).toBe('unavailable')
    expect(marketState(view({ status: 'listed', can_list: false, unavailable_reasons: ['market_disabled'] }))).toBe('unavailable')
  })

  it('hides the not_configured reason and duplicates', () => {
    expect(visibleReasons(view({ unavailable_reasons: ['not_configured', 'market_disabled', 'market_disabled'] }))).toEqual(['market_disabled'])
  })

  it('only offers live tiers the account is not already an admin member of', () => {
    const v = view({
      allowed_tiers: [
        { group_id: 3, name: 'T1', rate_multiplier: 1, available: true, admin_member: false },
        { group_id: 4, name: 'T2', rate_multiplier: 2, available: false, admin_member: false },
        { group_id: 5, name: 'T3', rate_multiplier: 3, available: true, admin_member: true }
      ]
    })
    expect(pickableTiers(v).map((t) => t.group_id)).toEqual([3])
  })
})

describe('market errors', () => {
  it('maps reasons, then status, then generic', () => {
    expect(marketErrorKey({ status: 409, reason: 'MARKET_DISABLED' })).toBe('market.errors.MARKET_DISABLED')
    expect(marketErrorKey({ status: 409, code: 'MARKET_ADMIN_MEMBERSHIP_CONFLICT' })).toBe('market.errors.MARKET_ADMIN_MEMBERSHIP_CONFLICT')
    expect(marketErrorKey({ status: 400, reason: 'MARKET_GROUP_NOT_ALLOWED' })).toBe('market.errors.MARKET_GROUP_NOT_ALLOWED')
    expect(marketErrorKey({ status: 404 })).toBe('market.errors.HOSTED_ACCOUNT_NOT_FOUND')
    expect(marketErrorKey({ status: 0 })).toBe('market.errors.network')
    expect(marketErrorKey({ status: 500, reason: 'SOMETHING_ELSE' })).toBe('market.errors.generic')
    expect(errorReason({ metadata: { reason: 'proxy_unavailable' } })).toBe('proxy_unavailable')
    expect(errorReason({ metadata: { reason: '<b>x</b>' } })).toBe('')
  })
})

describe('earning amounts', () => {
  it('keeps 10 decimals exactly', () => {
    expect(units('0.0000000001')).toBe(BigInt(1))
    expect(units('1.23456789012')).toBeNull()
    expect(usdExact('0.8')).toBe('$0.80')
    expect(usdExact('-0.0000012')).toBe('-$0.0000012')
    expect(usdExact('nope')).toBe('—')
  })

  it('«全部» floors to whole cents', () => {
    expect(floorToCents('3.4999999999')).toBe('3.49')
    expect(floorToCents('3.5')).toBe('3.5')
    expect(floorToCents('0.009')).toBe('0')
    expect(floorToCents('-1')).toBe('0')
  })

  it('checks a typed amount against min, available and 2 decimals', () => {
    expect(checkConvertAmount('2.50', '1', '3.5')).toEqual({ amount: '2.5' })
    expect(checkConvertAmount('03', '1', '3.5')).toEqual({ amount: '3' })
    expect(checkConvertAmount('3.5', '1', '3.5')).toEqual({ amount: '3.5' })
    expect(checkConvertAmount('3.51', '1', '3.5')).toEqual({ problem: 'aboveAvailable' })
    expect(checkConvertAmount('0.99', '1', '3.5')).toEqual({ problem: 'belowMin' })
    expect(checkConvertAmount('1.001', '1', '3.5')).toEqual({ problem: 'decimals' })
    expect(checkConvertAmount('1.2300', '1', '3.5')).toEqual({ problem: 'decimals' })
    expect(checkConvertAmount('1.', '1', '3.5')).toEqual({ problem: 'invalid' })
    expect(checkConvertAmount('1.20', '1', '3.5')).toEqual({ amount: '1.2' })
    expect(checkConvertAmount('0', '0.01', '3.5')).toEqual({ problem: 'invalid' })
    expect(checkConvertAmount('-1', '1', '3.5')).toEqual({ problem: 'invalid' })
    expect(checkConvertAmount('1e2', '1', '3.5')).toEqual({ problem: 'invalid' })
    expect(checkConvertAmount('', '1', '3.5')).toEqual({ problem: 'invalid' })
  })

  it('knows when there is anything to show or convert', () => {
    const zero = { frozen: '0', available: '0', converted_total: '0', min_convert: '1', as_of: '' }
    expect(hasEarnings(zero)).toBe(false)
    expect(hasEarnings({ ...zero, frozen: '0.0000000001' })).toBe(true)
    expect(hasEarnings({ ...zero, converted_total: '5' })).toBe(true)
    expect(canConvert({ ...zero, available: '0.999' })).toBe(false)
    expect(canConvert({ ...zero, available: '1.009' })).toBe(true)
  })

  it('makes v4 request ids', () => {
    const a = newRequestId()
    expect(a).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
    expect(newRequestId()).not.toBe(a)
  })
})
