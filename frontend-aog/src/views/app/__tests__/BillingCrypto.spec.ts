import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { i18n } from '@/i18n'
import type { CryptoPayConfig } from '@/api'

const crypto = vi.hoisted(() => ({ config: vi.fn(), deposits: vi.fn(), createOrder: vi.fn() }))
const push = vi.hoisted(() => vi.fn())
vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return {
    ...actual,
    cryptoPayApi: { ...actual.cryptoPayApi, ...crypto },
    redeemApi: { redeem: vi.fn(), history: vi.fn().mockResolvedValue([]) },
    subscriptionsApi: { summary: vi.fn().mockResolvedValue(null) },
    paymentAPI: {
      getConfig: vi.fn().mockResolvedValue({
        config: { enabled: false, min_amount: 1, max_amount: 1000, order_timeout_minutes: 30, max_pending_orders: 3, recharge_fee_rate: 0 },
        payment_methods: []
      }),
      listPlans: vi.fn(),
      listMyOrders: vi.fn(),
      createOrder: vi.fn(),
      getOrder: vi.fn(),
      cancelOrder: vi.fn()
    }
  }
})
vi.mock('vue-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-router')>()
  return { ...actual, useRoute: () => ({ query: {} }), useRouter: () => ({ push, replace: vi.fn() }) }
})

import BillingView from '../BillingView.vue'

const enabled: CryptoPayConfig = {
  enabled: true,
  min_amount: '1',
  max_amount: '10000',
  order_ttl_minutes: 180,
  networks: [
    {
      network: 'arbitrum-one',
      label: 'Arbitrum',
      assets: [{ symbol: 'USDC', contract: '0xa' }, { symbol: 'USDT', contract: '0xb' }],
      finality_hint_minutes: 20,
      explorer_tx_url: 'https://arbiscan.io/tx/{tx}',
      explorer_address_url: 'https://arbiscan.io/address/{addr}'
    },
    {
      network: 'solana-mainnet',
      label: 'Solana',
      assets: [{ symbol: 'USDC', contract: 'EP' }, { symbol: 'USDT', contract: 'Es' }],
      finality_hint_minutes: 1,
      explorer_tx_url: 'https://solscan.io/tx/{tx}',
      explorer_address_url: 'https://solscan.io/account/{addr}'
    }
  ]
}
const disabled: CryptoPayConfig = { enabled: false, min_amount: '1', max_amount: '10000', order_ttl_minutes: 180, networks: [] }
const emptyPage = { items: [], total: 0, page: 1, page_size: 10 }

async function mountView() {
  setActivePinia(createPinia())
  i18n.global.locale.value = 'zh'
  const w = mount(BillingView, { global: { plugins: [i18n], stubs: { RouterLink: true, Pager: true } } })
  await flushPromises()
  return w
}

enableAutoUnmount(afterEach)

describe('BillingView stablecoin top-up', () => {
  beforeEach(() => {
    crypto.config.mockReset()
    crypto.deposits.mockReset().mockResolvedValue(emptyPage)
    crypto.createOrder.mockReset()
    push.mockReset()
  })

  it('is hidden when the feature is off and there is no history', async () => {
    crypto.config.mockResolvedValue(disabled)
    const w = await mountView()
    expect(w.find('[data-testid=crypto-topup]').exists()).toBe(false)
    expect(w.find('[data-testid=crypto-deposits-section]').exists()).toBe(false)
    expect(crypto.deposits).toHaveBeenCalledWith(1, 10)
  })

  it('still lists past deposits when the feature is off', async () => {
    crypto.config.mockResolvedValue(disabled)
    crypto.deposits.mockResolvedValue({
      items: [{ id: 1, network: 'solana-mainnet', asset: 'USDT', amount: '3.5', tx_hash: 'sig123', status: 'credited', block_time: '2026-11-16T08:00:00Z', credited_at: '2026-11-16T08:01:00Z' }],
      total: 1,
      page: 1,
      page_size: 10
    })
    const w = await mountView()
    const section = w.find('[data-testid=crypto-deposits-section]')
    expect(section.exists()).toBe(true)
    expect(section.text()).toContain('$3.50')
    expect(section.text()).toContain('已入账')
  })

  it('shows a load failure with retry instead of "no deposits"', async () => {
    crypto.config.mockResolvedValue(disabled)
    crypto.deposits.mockRejectedValueOnce({ status: 0, message: 'network' })
    const w = await mountView()
    const err = w.find('[data-testid=crypto-deposits-error]')
    expect(err.exists()).toBe(true)
    expect(w.text()).not.toContain('还没有稳定币到账')
    await err.find('button').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid=crypto-deposits-error]').exists()).toBe(false)
  })

  it('creates an order with a string amount and opens the checkout page', async () => {
    crypto.config.mockResolvedValue(enabled)
    crypto.createOrder.mockResolvedValue({ id: 42 })
    const w = await mountView()
    const form = w.find('[data-testid=crypto-topup]')
    expect(form.exists()).toBe(true)
    // Native payment is off, so its "not available" line gives way to the stablecoin form.
    expect(w.text()).not.toContain('本站暂未开通在线支付')
    expect(w.find('[data-testid=crypto-deposits-section]').text()).toContain('还没有稳定币到账')

    await form.findAll('button').find((b) => b.text() === 'Solana')!.trigger('click')
    await form.findAll('button').find((b) => b.text() === 'USDT')!.trigger('click')
    await form.find('[data-testid=crypto-amount-input]').setValue('25.50')
    await form.find('form').trigger('submit')
    await flushPromises()
    expect(crypto.createOrder).toHaveBeenCalledWith({ network: 'solana-mainnet', asset: 'USDT', amount: '25.5' })
    expect(push).toHaveBeenCalledWith({ name: 'crypto-checkout', params: { id: '42' } })
  })

  it('explains a rejected amount from the server', async () => {
    crypto.config.mockResolvedValue(enabled)
    crypto.createOrder.mockRejectedValue({ status: 400, reason: 'CRYPTO_PAY_AMOUNT_INVALID', message: 'bad' })
    const w = await mountView()
    await w.find('[data-testid=crypto-topup] form').trigger('submit')
    await flushPromises()
    expect(w.find('[data-testid=crypto-topup] [role=alert]').text()).toContain('金额不正确')
  })

  it('blocks invalid amounts and explains an unavailable address', async () => {
    crypto.config.mockResolvedValue(enabled)
    crypto.createOrder.mockRejectedValue({ status: 409, reason: 'CRYPTO_PAY_ADDRESS_UNAVAILABLE', message: 'x' })
    const w = await mountView()
    const input = w.find('[data-testid=crypto-amount-input]')
    await input.setValue('0.5')
    expect(w.find('[data-testid=crypto-submit]').attributes('disabled')).toBeDefined()
    await input.setValue('10.123')
    expect(w.find('[data-testid=crypto-submit]').attributes('disabled')).toBeDefined()
    await input.setValue('10')
    await w.find('[data-testid=crypto-topup] form').trigger('submit')
    await flushPromises()
    expect(w.find('[data-testid=crypto-topup] [role=alert]').text()).toContain('暂时无法分配充值地址')
    expect(push).not.toHaveBeenCalled()
  })
})
