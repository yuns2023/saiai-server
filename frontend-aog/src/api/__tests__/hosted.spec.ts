import { beforeEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('../client', () => ({ api: http }))

import { hostedApi } from '../hosted'

describe('hostedApi', () => {
  beforeEach(() => {
    http.get.mockReset()
  })

  it('GET /hosted/owned-accounts returns the items (null-safe)', async () => {
    http.get.mockResolvedValue({ data: { items: null } })
    expect(await hostedApi.listOwnedAccounts()).toEqual([])
    expect(http.get).toHaveBeenCalledWith('/hosted/owned-accounts')

    http.get.mockResolvedValue({ data: { items: [{ account_id: 3, current: false, windows: null }] } })
    const [a] = await hostedApi.listOwnedAccounts()
    expect(a.account_id).toBe(3)
    expect(a.windows).toEqual([])
  })

  it('GET /hosted/owned-accounts/:id/usage sends the window, limit and cursor', async () => {
    http.get.mockResolvedValue({ data: { items: [], next_cursor: 'c2' } })
    const page = await hostedApi.listOwnedAccountUsage(12, { start: '2026-11-09T16:00:00Z', end: '2026-11-16T16:00:00Z', limit: 50, cursor: 'c1' })
    expect(http.get).toHaveBeenCalledWith('/hosted/owned-accounts/12/usage', {
      params: { start: '2026-11-09T16:00:00Z', end: '2026-11-16T16:00:00Z', limit: 50, cursor: 'c1' },
      signal: undefined
    })
    expect(page.next_cursor).toBe('c2')
  })

  it('leaves out an empty cursor and normalises the last page', async () => {
    http.get.mockResolvedValue({ data: { items: null, next_cursor: '' } })
    const page = await hostedApi.listOwnedAccountUsage(12, { start: 'a', end: 'b', cursor: null })
    expect(http.get.mock.calls[0][1].params).toEqual({ start: 'a', end: 'b' })
    expect(page).toEqual({ items: [], next_cursor: null })
  })
})
