import { beforeEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../client', () => ({ api: http }))

import { cryptoOrderError, cryptoPayApi, explorerUrl, pageCount } from '../cryptoPay'

describe('cryptoPayApi', () => {
  beforeEach(() => {
    http.get.mockReset()
    http.post.mockReset()
  })

  it('GET /crypto-pay/config and tolerates a null network list', async () => {
    http.get.mockResolvedValue({ data: { enabled: false, min_amount: '1', max_amount: '10000', order_ttl_minutes: 180, networks: null } })
    const c = await cryptoPayApi.config()
    expect(http.get).toHaveBeenCalledWith('/crypto-pay/config')
    expect(c.networks).toEqual([])
    expect(c.min_amount).toBe('1')
  })

  it('POST /crypto-pay/orders sends the amount as a string', async () => {
    http.post.mockResolvedValue({ data: { id: 7 } })
    await cryptoPayApi.createOrder({ network: 'arbitrum-one', asset: 'USDC', amount: '10.5' })
    expect(http.post).toHaveBeenCalledWith('/crypto-pay/orders', { network: 'arbitrum-one', asset: 'USDC', amount: '10.5' })
  })

  it('GET /crypto-pay/orders/:id keeps money as strings', async () => {
    http.get.mockResolvedValue({ data: { id: 7, amount: '10.000001', received_amount: '0', deposits: null } })
    const o = await cryptoPayApi.getOrder(7)
    expect(http.get).toHaveBeenCalledWith('/crypto-pay/orders/7')
    expect(o.amount).toBe('10.000001')
    expect(o.deposits).toEqual([])
  })

  it('paginates orders and deposits', async () => {
    http.get.mockResolvedValue({ data: { items: null, total: 0, page: 2, page_size: 10 } })
    const orders = await cryptoPayApi.myOrders(2, 10)
    expect(http.get).toHaveBeenLastCalledWith('/crypto-pay/orders/my', { params: { page: 2, page_size: 10 } })
    expect(orders.items).toEqual([])
    await cryptoPayApi.deposits(3, 20)
    expect(http.get).toHaveBeenLastCalledWith('/crypto-pay/deposits', { params: { page: 3, page_size: 20 } })
  })

  it('GET /crypto-pay/addresses', async () => {
    http.get.mockResolvedValue({ data: [{ network: 'solana-mainnet', address: 'So1' }] })
    expect(await cryptoPayApi.addresses()).toEqual([{ network: 'solana-mainnet', address: 'So1' }])
    expect(http.get).toHaveBeenCalledWith('/crypto-pay/addresses')
  })
})

describe('helpers', () => {
  it('explorerUrl fills templates and only allows https', () => {
    expect(explorerUrl('https://arbiscan.io/tx/{tx}', 'tx', '0xabc')).toBe('https://arbiscan.io/tx/0xabc')
    expect(explorerUrl('https://solscan.io/account/{addr}', 'addr', 'So1')).toBe('https://solscan.io/account/So1')
    expect(explorerUrl('javascript:alert(1)//{tx}', 'tx', 'x')).toBe('')
    expect(explorerUrl('http://example.com/{tx}', 'tx', 'x')).toBe('')
    expect(explorerUrl(undefined, 'tx', 'x')).toBe('')
    expect(explorerUrl('https://arbiscan.io/tx/{tx}', 'tx', 'a/b?c')).toBe('https://arbiscan.io/tx/a%2Fb%3Fc')
  })
  it('pageCount', () => {
    expect(pageCount({ total: 0, page_size: 10 })).toBe(1)
    expect(pageCount({ total: 21, page_size: 10 })).toBe(3)
  })
  it('cryptoOrderError maps reasons and statuses', () => {
    expect(cryptoOrderError({ status: 409, reason: 'CRYPTO_PAY_ADDRESS_UNAVAILABLE' })).toBe('addressUnavailable')
    expect(cryptoOrderError({ status: 503, reason: 'CRYPTO_PAY_DISABLED' })).toBe('disabled')
    expect(cryptoOrderError({ status: 409 })).toBe('addressUnavailable')
    expect(cryptoOrderError({ status: 400, reason: 'OTHER' })).toBeNull()
    expect(cryptoOrderError(undefined)).toBeNull()
  })
})
