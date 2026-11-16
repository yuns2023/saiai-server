import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount, RouterLinkStub } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { i18n } from '@/i18n'
import type { CryptoDeposit, CryptoOrder, CryptoPayConfig } from '@/api'

const api = vi.hoisted(() => ({ config: vi.fn(), getOrder: vi.fn() }))
vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return { ...actual, cryptoPayApi: { ...actual.cryptoPayApi, ...api } }
})
const routeHolder = vi.hoisted(() => ({ route: null as unknown as { params: { id: string } } }))
vi.mock('vue-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-router')>()
  const { reactive } = await import('vue')
  routeHolder.route = reactive({ params: { id: '7' } })
  return { ...actual, useRoute: () => routeHolder.route }
})
vi.mock('qrcode', () => ({ default: { toDataURL: vi.fn().mockResolvedValue('data:image/png;base64,QR') } }))

import CryptoCheckoutView from '../CryptoCheckoutView.vue'
import { useAuthStore } from '@/stores/auth'

const NOW = new Date('2026-11-16T08:00:00Z').getTime()

const config: CryptoPayConfig = {
  enabled: true,
  min_amount: '1',
  max_amount: '10000',
  order_ttl_minutes: 180,
  networks: [
    {
      network: 'arbitrum-one',
      label: 'Arbitrum',
      assets: [{ symbol: 'USDC', contract: '0xaf88d065e77c8cc2239327c5edb3a432268e5831' }, { symbol: 'USDT', contract: '0xfd086bc7cd5c481dcc9c85ebe478a1c0b69fcbb9' }],
      finality_hint_minutes: 20,
      explorer_tx_url: 'https://arbiscan.io/tx/{tx}',
      explorer_address_url: 'https://arbiscan.io/address/{addr}'
    }
  ]
}

function deposit(over: Partial<CryptoDeposit> = {}): CryptoDeposit {
  return {
    id: 1,
    network: 'arbitrum-one',
    asset: 'USDC',
    amount: '10',
    tx_hash: '0x' + 'ab'.repeat(32),
    status: 'confirming',
    block_time: new Date(NOW - 60_000).toISOString(),
    credited_at: null,
    ...over
  }
}

function order(over: Partial<CryptoOrder> = {}): CryptoOrder {
  return {
    id: 7,
    network: 'arbitrum-one',
    asset: 'USDC',
    amount: '10',
    address: '0x1111111111111111111111111111111111111111',
    status: 'pending',
    created_at: new Date(NOW - 60_000).toISOString(),
    expires_at: new Date(NOW + 10 * 60_000).toISOString(),
    received_amount: '0',
    confirming_amount: '0',
    credited_amount: '0',
    deposits: [],
    deposits_truncated: false,
    ...over
  }
}

async function mountView(o: CryptoOrder) {
  api.getOrder.mockResolvedValue(o)
  const w = mount(CryptoCheckoutView, { global: { plugins: [i18n], stubs: { RouterLink: RouterLinkStub } } })
  await flushPromises()
  return w
}

enableAutoUnmount(afterEach)

describe('CryptoCheckoutView', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(NOW)
    setActivePinia(createPinia())
    vi.spyOn(useAuthStore(), 'refreshUser').mockResolvedValue(undefined as never)
    i18n.global.locale.value = 'zh'
    api.config.mockReset().mockResolvedValue(config)
    api.getOrder.mockReset()
    routeHolder.route.params.id = '7'
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('pending: amount, address, QR, countdown ticking and 5 s polling', async () => {
    const w = await mountView(order())
    expect(w.find('[data-testid=crypto-amount]').text()).toBe('$10.00')
    expect(w.find('[data-testid=crypto-address]').text()).toBe('0x1111111111111111111111111111111111111111')
    expect(w.find('[data-testid=crypto-qr]').attributes('src')).toContain('QR')
    expect(w.text()).toContain('向此地址发送 $10.00 USDC（Arbitrum）')
    expect(w.text()).toContain('Arbitrum 约 20 分钟')
    expect(w.find('[data-testid=crypto-warning]').text()).toContain('只能转入 Arbitrum 链上的 USDC 或 USDT')
    expect(w.find('a[href="https://arbiscan.io/address/0x1111111111111111111111111111111111111111"]').exists()).toBe(true)
    expect(w.findComponent(RouterLinkStub).props('to')).toBe('/billing')
    expect(w.find('[data-testid=crypto-countdown]').text()).toBe('10:00')
    expect(w.find('[data-testid=crypto-status]').text()).toBe('待付款')
    expect(w.find('[data-testid=crypto-remaining]').text()).toContain('$10.00')
    expect(w.find('[data-testid=crypto-expired]').exists()).toBe(false)

    await vi.advanceTimersByTimeAsync(1000)
    expect(w.find('[data-testid=crypto-countdown]').text()).toBe('09:59')
    expect(api.getOrder).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(4000)
    expect(api.getOrder).toHaveBeenCalledTimes(2)
    expect(api.getOrder).toHaveBeenLastCalledWith('7')
  })

  it('warns with every asset the chain address accepts (en)', async () => {
    i18n.global.locale.value = 'en'
    const w = await mountView(order())
    expect(w.find('[data-testid=crypto-warning]').text()).toContain('Only USDC or USDT on Arbitrum')
    expect(w.text()).toContain('View on block explorer')
    i18n.global.locale.value = 'zh'
  })

  it('shows hours when more than an hour is left', async () => {
    const w = await mountView(order({ expires_at: new Date(NOW + 3 * 3600_000 - 1000).toISOString() }))
    expect(w.find('[data-testid=crypto-countdown]').text()).toBe('2:59:59')
  })

  it('confirming: observed but not final', async () => {
    const w = await mountView(order({ confirming_amount: '10', deposits: [deposit()] }))
    expect(w.find('[data-testid=crypto-confirming]').text()).toBe('$10.00')
    expect(w.find('[data-testid=crypto-received]').text()).toBe('$0.00')
    expect(w.find('[data-testid=crypto-status]').text()).toBe('待付款')
    const table = w.find('[data-testid=crypto-deposits]')
    expect(table.text()).toContain('确认中')
    expect(table.find('a').attributes('href')).toBe(`https://arbiscan.io/tx/0x${'ab'.repeat(32)}`)
  })

  it('never claims "paid" before the server does, even if received already covers the amount', async () => {
    const w = await mountView(order({ received_amount: '10', confirming_amount: '10', deposits: [deposit()] }))
    expect(w.find('[data-testid=crypto-status]').text()).toBe('待付款')
    expect(w.find('[data-testid=crypto-paid-full]').exists()).toBe(false)
    expect(w.find('.seal').exists()).toBe(false)
  })

  it('paid: shows credited state and keeps polling for overpayment', async () => {
    const credited = deposit({ amount: '12.345678', status: 'credited', credited_at: new Date(NOW).toISOString() })
    const w = await mountView(order({ status: 'paid', received_amount: '12.345678', credited_amount: '12.345678', deposits: [credited] }))
    expect(w.find('[data-testid=crypto-status]').text()).toBe('已付足')
    expect(w.find('[data-testid=crypto-paid-full]').exists()).toBe(true)
    expect(w.find('[data-testid=crypto-credited]').text()).toBe('$12.345678')
    expect(w.find('.seal').exists()).toBe(true)
    expect(w.find('[data-testid=crypto-remaining]').exists()).toBe(false)

    // A further transfer lands: balance header is refreshed.
    api.getOrder.mockResolvedValue(order({ status: 'paid', received_amount: '13.345678', credited_amount: '13.345678', deposits: [deposit({ id: 2, amount: '1', status: 'credited' }), credited] }))
    await vi.advanceTimersByTimeAsync(5000)
    expect(api.getOrder).toHaveBeenCalledTimes(2)
    expect(w.find('[data-testid=crypto-credited]').text()).toBe('$13.345678')
    expect(useAuthStore().refreshUser).toHaveBeenCalled()
  })

  it('expired with a late deposit: notice, no countdown, 30 s polling', async () => {
    const late = deposit({ status: 'credited', amount: '4', credited_at: new Date(NOW).toISOString() })
    const w = await mountView(
      order({ status: 'expired', expires_at: new Date(NOW - 60_000).toISOString(), received_amount: '4', credited_amount: '4', deposits: [late] })
    )
    expect(w.find('[data-testid=crypto-expired]').text()).toBe('订单已过期，转账仍会按实收入账')
    expect(w.find('[data-testid=crypto-countdown]').exists()).toBe(false)
    expect(w.find('[data-testid=crypto-status]').text()).toBe('已过期')
    expect(w.find('[data-testid=crypto-remaining]').text()).toContain('$6.00')
    expect(w.find('[data-testid=crypto-deposits]').text()).toContain('已入账')

    await vi.advanceTimersByTimeAsync(29_000)
    expect(api.getOrder).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1000)
    expect(api.getOrder).toHaveBeenCalledTimes(2)
  })

  it('a pending order whose deadline passes switches to the expired notice', async () => {
    const w = await mountView(order({ expires_at: new Date(NOW + 2000).toISOString() }))
    expect(w.find('[data-testid=crypto-expired]').exists()).toBe(false)
    await vi.advanceTimersByTimeAsync(2000)
    expect(w.find('[data-testid=crypto-expired]').exists()).toBe(true)
    expect(w.find('[data-testid=crypto-status]').text()).toBe('已过期')
  })

  it('notes when the deposit list is truncated', async () => {
    const w = await mountView(order({ deposits: [deposit()], deposits_truncated: true }))
    expect(w.find('[data-testid=crypto-truncated]').text()).toBe('仅显示最近 1000 笔，金额合计为全部到账')
    const plain = await mountView(order({ deposits: [deposit()] }))
    expect(plain.find('[data-testid=crypto-truncated]').exists()).toBe(false)
  })

  it('held: explains manual review', async () => {
    const w = await mountView(order({ received_amount: '6000', deposits: [deposit({ amount: '6000', status: 'held' })], status: 'paid' }))
    expect(w.find('[data-testid=crypto-held]').exists()).toBe(true)
    expect(w.find('[data-testid=crypto-deposits]').text()).toContain('待审核')
    expect(w.find('[data-testid=crypto-credited]').text()).toBe('$0.00')
    expect(w.find('.seal').exists()).toBe(false)
  })

  it('stops polling on unmount and while hidden', async () => {
    const w = await mountView(order())
    const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(20_000)
    expect(api.getOrder).toHaveBeenCalledTimes(1)
    hidden.mockReturnValue(false)
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(api.getOrder).toHaveBeenCalledTimes(2)
    w.unmount()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(api.getOrder).toHaveBeenCalledTimes(2)
  })

  it('a late failure for the previous order does not poison the new one', async () => {
    let failOld!: (e: unknown) => void
    api.getOrder.mockImplementationOnce(() => new Promise((_, reject) => (failOld = reject)))
    api.getOrder.mockResolvedValue(order({ id: 8, amount: '25' }))
    const w = mount(CryptoCheckoutView, { global: { plugins: [i18n], stubs: { RouterLink: RouterLinkStub } } })
    await flushPromises()
    routeHolder.route.params.id = '8'
    await flushPromises()
    failOld({ status: 404 })
    await flushPromises()
    expect(w.find('[data-testid=crypto-load-error]').exists()).toBe(false)
    expect(w.find('[data-testid=crypto-amount]').text()).toBe('$25.00')
    expect(api.getOrder).toHaveBeenLastCalledWith('8')
    // Polling continues for the new order.
    const calls = api.getOrder.mock.calls.length
    await vi.advanceTimersByTimeAsync(5000)
    expect(api.getOrder.mock.calls.length).toBe(calls + 1)
  })

  it('unmounting during a request (even after a route change) makes no further requests', async () => {
    let finish!: (o: CryptoOrder) => void
    api.getOrder.mockImplementationOnce(() => new Promise((resolve) => (finish = resolve)))
    api.getOrder.mockResolvedValue(order())
    const w = mount(CryptoCheckoutView, { global: { plugins: [i18n], stubs: { RouterLink: RouterLinkStub } } })
    await flushPromises()
    routeHolder.route.params.id = '8'
    await flushPromises()
    w.unmount()
    finish(order())
    await vi.advanceTimersByTimeAsync(60_000)
    expect(api.getOrder).toHaveBeenCalledTimes(1)
  })

  it('a tab opened in the background loads once and then waits', async () => {
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
    const w = await mountView(order())
    expect(w.find('[data-testid=crypto-amount]').exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(api.getOrder).toHaveBeenCalledTimes(1)
  })

  it('reports a missing order without polling', async () => {
    api.getOrder.mockRejectedValue({ status: 404, message: 'not found' })
    const w = mount(CryptoCheckoutView, { global: { plugins: [i18n], stubs: { RouterLink: RouterLinkStub } } })
    await flushPromises()
    expect(w.find('[data-testid=crypto-load-error]').text()).toContain('没有找到这个订单')
    await vi.advanceTimersByTimeAsync(60_000)
    expect(api.getOrder).toHaveBeenCalledTimes(1)
  })
})
