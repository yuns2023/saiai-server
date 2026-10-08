import { describe, expect, it } from 'vitest'
import { isNativeChatUsage, nativeChatChargeCosts } from '../nativeChatUsage'

describe('native Chat historical charges', () => {
  it('recognizes native paths when older responses omit billing_unit', () => {
    expect(isNativeChatUsage({ model: 'gpt-5-6', inbound_endpoint: '/chatgpt/backend-api/f/conversation' })).toBe(true)
    expect(isNativeChatUsage({ inbound_endpoint: '/chatgpt/backend-api/f/conversation/resume' })).toBe(true)
    expect(isNativeChatUsage({ inbound_endpoint: '/chatgpt/backend-api/f/conversation/prepare' })).toBe(false)
    expect(isNativeChatUsage({ model: 'gpt-6-luna', inbound_endpoint: '/v1/responses' })).toBe(false)
  })

  it('shows stored effective costs after multipliers without repricing historical rows', () => {
    expect(nativeChatChargeCosts({ actual_cost: 0.075, native_chat_turn_cost_usd: 0.015, native_chat_image_cost_usd: 0.06 })).toEqual({ turn: 0.015, image: 0.06 })
    const fallback = nativeChatChargeCosts({ actual_cost: 0.075, total_cost: 0.05, output_cost: 0.04 })
    expect(fallback.turn).toBeCloseTo(0.015)
    expect(fallback.image).toBeCloseTo(0.06)
    expect(nativeChatChargeCosts({ actual_cost: 0.01 })).toEqual({ turn: 0.01, image: 0 })
  })
})
