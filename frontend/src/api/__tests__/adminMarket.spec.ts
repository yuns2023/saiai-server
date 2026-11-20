import { beforeEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: http }))

import { marketAPI } from '@/api/admin/market'

const view = {
  account_id: 11,
  owner_user_id: 7,
  listing: { account_id: 11, owner_user_id: 7, allowed_group_ids: null, selected_group_id: null, status: 'unlisted', version: 2, protection: {}, listed_at: null, created_at: 'x', updated_at: 'x' },
  allowed_tiers: null,
  ineligible_reason: null,
  default_protection: {},
  market_enabled: false
}

describe('admin marketAPI', () => {
  beforeEach(() => Object.values(http).forEach((f) => f.mockReset()))

  it('reads, writes and revokes /admin/accounts/:id/market-listing (null-safe lists)', async () => {
    http.get.mockResolvedValue({ data: view })
    const v = await marketAPI.getListing(11)
    expect(http.get).toHaveBeenCalledWith('/admin/accounts/11/market-listing')
    expect(v.allowed_tiers).toEqual([])
    expect(v.listing?.allowed_group_ids).toEqual([])
    expect(v.ineligible_reason).toBe('')

    http.put.mockResolvedValue({ data: view })
    const body = { allowed_group_ids: [], reserved_concurrency: 0, reserved_devices: 0, reserved_admissions: 0 }
    await marketAPI.putListing(11, body)
    // allowed_group_ids is always sent (an empty list allows nothing); explicit zeros stay zeros.
    expect(http.put).toHaveBeenCalledWith('/admin/accounts/11/market-listing', body)

    http.delete.mockResolvedValue({ data: { ...view, listing: null } })
    expect((await marketAPI.revokeListing(11)).listing).toBeNull()
    expect(http.delete).toHaveBeenCalledWith('/admin/accounts/11/market-listing')
  })

  it('owners and entries use page / keyset params', async () => {
    http.get.mockResolvedValueOnce({ data: { items: null, total: 0, page: 1, page_size: 20, as_of: 'x' } })
    expect((await marketAPI.owners(1, 20)).items).toEqual([])
    expect(http.get).toHaveBeenLastCalledWith('/admin/market/earnings', { params: { page: 1, page_size: 20 } })
    http.get.mockResolvedValueOnce({ data: { items: [], total: 0, page: 2, page_size: 50, as_of: 'x' } })
    await marketAPI.owners(2, 50, 7)
    expect(http.get).toHaveBeenLastCalledWith('/admin/market/earnings', { params: { page: 2, page_size: 50, owner_user_id: 7 } })

    http.get.mockResolvedValueOnce({ data: { items: null, next_cursor: null } })
    expect(await marketAPI.entries()).toEqual({ items: [], next_cursor: null })
    expect(http.get).toHaveBeenLastCalledWith('/admin/market/earnings/entries', { params: {} })
    http.get.mockResolvedValueOnce({ data: { items: [], next_cursor: 3 } })
    await marketAPI.entries({ ownerUserId: 7, cursor: 9, limit: 50 })
    expect(http.get).toHaveBeenLastCalledWith('/admin/market/earnings/entries', { params: { owner_user_id: 7, cursor: 9, limit: 50 } })
  })

  it('reverse sends the chosen scope, the cutoff (as_of) and the note', async () => {
    const asOf = '2026-11-19T04:00:00.123456Z'
    http.post.mockResolvedValue({ data: { cutoff: asOf, as_of: '2026-11-19T04:00:01Z', reversed_count: 1, reversed_amount: '2', owners: null } })
    const r = await marketAPI.reverse({ owner_user_id: 7, as_of: asOf, note: 'fraud' })
    expect(http.post).toHaveBeenLastCalledWith('/admin/market/earnings/reverse', { as_of: asOf, note: 'fraud', owner_user_id: 7 })
    expect(r.owners).toEqual([])
    expect(r.cutoff).toBe(asOf)
    await marketAPI.reverse({ account_id: 11, as_of: asOf, note: 'chargeback' })
    expect(http.post).toHaveBeenLastCalledWith('/admin/market/earnings/reverse', { as_of: asOf, note: 'chargeback', account_id: 11 })
    // The server contract: note optional, both ids = that owner on that account.
    await marketAPI.reverse({ owner_user_id: 7, account_id: 11, as_of: asOf })
    expect(http.post).toHaveBeenLastCalledWith('/admin/market/earnings/reverse', { as_of: asOf, owner_user_id: 7, account_id: 11 })
  })
})
