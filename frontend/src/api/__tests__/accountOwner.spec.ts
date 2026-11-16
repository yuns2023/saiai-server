import { beforeEach, describe, expect, it, vi } from 'vitest'
import { list, setAccountOwner } from '../admin/accounts'

const client = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn() }))
vi.mock('../client', () => ({ apiClient: client }))

describe('Admin account owner API', () => {
  beforeEach(() => vi.clearAllMocks())

  it('sets the owner with the user ID in the body', async () => {
    const result = { account_id: 12, owner_user_id: 5, owner_email: 'a@b.c' }
    client.put.mockResolvedValue({ data: result })
    expect(await setAccountOwner(12, 5)).toEqual(result)
    expect(client.put).toHaveBeenCalledWith('/admin/accounts/12/owner', { owner_user_id: 5 })
  })

  it('clears the owner by sending the key with null (never omitting it)', async () => {
    client.put.mockResolvedValue({ data: { account_id: 12, owner_user_id: null, owner_email: null } })
    await setAccountOwner(12, null)
    const body = client.put.mock.calls[0][1]
    expect(Object.prototype.hasOwnProperty.call(body, 'owner_user_id')).toBe(true)
    expect(body).toEqual({ owner_user_id: null })
    expect(JSON.stringify(body)).toBe('{"owner_user_id":null}')
  })

  it('passes the owner filter to the account list and omits it when unset', async () => {
    client.get.mockResolvedValue({ data: { items: [], total: 0, page: 1, page_size: 20, pages: 0 } })
    await list(1, 20, { platform: '', owner_user_id: 5 })
    expect(client.get.mock.calls[0][1].params).toMatchObject({ owner_user_id: 5 })
    await list(1, 20, { platform: '', owner_user_id: undefined })
    expect(client.get.mock.calls[1][1].params.owner_user_id).toBeUndefined()
  })
})
