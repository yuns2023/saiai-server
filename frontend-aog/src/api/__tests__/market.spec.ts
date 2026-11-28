import { beforeEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn(), post: vi.fn() }))
vi.mock('../client', () => ({ api: http }))

import { marketApi } from '../market'

describe('marketApi (owner side)', () => {
  beforeEach(() => {
    http.get.mockReset()
    http.put.mockReset()
    http.post.mockReset()
  })

  it('GET /hosted/owned-accounts/:id/market normalises nullable fields', async () => {
    http.get.mockResolvedValue({
      data: {
        account_id: 11, market_enabled: true, status: 'not_configured', version: 0, plan: null, mode: 'self_use', cooldown_hours: 24, next_switch_at: null,
        allowed_tiers: null, unavailable_reasons: null, can_list: false, can_unlist: false, upstream_windows: null
      }
    })
    const v = await marketApi.getListing(11)
    expect(http.get).toHaveBeenCalledWith('/hosted/owned-accounts/11/market')
    expect(v.allowed_tiers).toEqual([])
    expect(v.unavailable_reasons).toEqual([])
    expect(v.upstream_windows).toEqual([])
    expect(v.selected_group_id).toBeNull()
    expect(v.plan).toBeNull()
    expect(v.cooldown_hours).toBe(24)
    expect(v.can_list).toBe(false)
    expect(v.can_unlist).toBe(false)
  })

  it('a view without mode / cooldown (older server) derives the mode from the status and shows no cooldown', async () => {
    http.get.mockResolvedValue({ data: { account_id: 11, market_enabled: true, status: 'listed', version: 3, allowed_tiers: [], unavailable_reasons: [], can_list: false } })
    const v = await marketApi.getListing(11)
    expect(v.mode).toBe('shared')
    expect(v.cooldown_hours).toBe(0)
    expect(v.next_switch_at).toBeNull()
  })

  it('PUT sends {listed:true} (group_id only when picked) and {listed:false}', async () => {
    http.put.mockResolvedValue({ data: { account_id: 11, status: 'listed', allowed_tiers: [], unavailable_reasons: [], can_list: true } })
    await marketApi.setListing(11, { listed: true })
    expect(http.put).toHaveBeenLastCalledWith('/hosted/owned-accounts/11/market', { listed: true })
    await marketApi.setListing(11, { listed: true, group_id: 3 })
    expect(http.put).toHaveBeenLastCalledWith('/hosted/owned-accounts/11/market', { listed: true, group_id: 3 })
    await marketApi.setListing(11, { listed: false })
    expect(http.put).toHaveBeenLastCalledWith('/hosted/owned-accounts/11/market', { listed: false })
  })

  it('earnings, entries (keyset) and convert use the documented shapes', async () => {
    http.get.mockResolvedValueOnce({ data: { frozen: '1.2', available: '3.5', converted_total: '2', min_convert: '1', as_of: 'x' } })
    expect((await marketApi.earnings()).available).toBe('3.5')
    expect(http.get).toHaveBeenLastCalledWith('/hosted/earnings')

    http.get.mockResolvedValueOnce({ data: { items: null, next_cursor: null } })
    expect(await marketApi.entries(null)).toEqual({ items: [], next_cursor: null })
    expect(http.get).toHaveBeenLastCalledWith('/hosted/earnings/entries', { params: { limit: 20 } })

    http.get.mockResolvedValueOnce({ data: { items: [{ id: 9 }], next_cursor: 9 } })
    expect((await marketApi.entries(42, 50)).next_cursor).toBe(9)
    expect(http.get).toHaveBeenLastCalledWith('/hosted/earnings/entries', { params: { limit: 50, cursor: 42 } })

    http.post.mockResolvedValue({ data: { entry_id: 4, request_id: 'r', amount: '2.5', created_at: 'x', replayed: false } })
    await marketApi.convert('2.5', '6f1c4a1e-0000-4000-8000-000000000000')
    // The amount stays a decimal string (never a float) and the request id is the idempotency key.
    expect(http.post).toHaveBeenCalledWith('/hosted/earnings/convert', { amount: '2.5', request_id: '6f1c4a1e-0000-4000-8000-000000000000' })
  })
})
