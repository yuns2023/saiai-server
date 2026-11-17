import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { i18n } from '@/i18n'
import type { RedeemPurchase, RedeemPurchaseOverview } from '@/api'
import { useAuthStore } from '@/stores/auth'

const purchaseApi = vi.hoisted(() => ({ overview: vi.fn(), purchase: vi.fn(), revoke: vi.fn() }))
const history = vi.hoisted(() => vi.fn())
vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return {
    ...actual,
    authApi: { ...actual.authApi, me: vi.fn().mockRejectedValue({ status: 0 }) },
    redeemPurchaseApi: purchaseApi,
    redeemApi: { redeem: vi.fn(), history },
    subscriptionsApi: { summary: vi.fn().mockResolvedValue(null) },
    cryptoPayApi: {
      ...actual.cryptoPayApi,
      config: vi.fn().mockResolvedValue({ enabled: false, min_amount: '1', max_amount: '1', order_ttl_minutes: 1, networks: [] }),
      deposits: vi.fn().mockResolvedValue({ items: [], total: 0, page: 1, page_size: 10 })
    },
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
  return { ...actual, useRoute: () => ({ query: {} }), useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }
})

import BillingView from '../BillingView.vue'

/** jsdom has no <dialog>.showModal; render the dialog inline while open. */
const DialogStub = defineComponent({
  props: { open: Boolean, title: String, body: String, confirmLabel: String, danger: Boolean, busy: Boolean },
  emits: ['confirm', 'cancel'],
  setup(props, { emit, slots }) {
    return () =>
      props.open
        ? h('div', { 'data-testid': props.danger ? 'dialog-danger' : 'dialog' }, [
            h('p', props.body),
            slots.default?.(),
            h('button', { 'data-testid': 'dialog-confirm', disabled: props.busy, onClick: () => emit('confirm') }, props.confirmLabel),
            h('button', { 'data-testid': 'dialog-cancel', onClick: () => emit('cancel') }, 'cancel')
          ])
        : null
  }
})

const row = (id: number, status: RedeemPurchase['status'], extra: Partial<RedeemPurchase> = {}): RedeemPurchase => ({
  id,
  code: `c0de${id}`.padEnd(32, '0'),
  amount: 10 * id,
  status,
  used_at: status === 'used' ? '2026-11-16T09:00:00Z' : null,
  created_at: '2026-11-16T08:00:00Z',
  revoked_at: status === 'revoked' ? '2026-11-16T10:00:00Z' : null,
  ...extra
})

function overview(enabled: boolean, items: RedeemPurchase[] = [], extra: Partial<RedeemPurchaseOverview['config']> = {}): RedeemPurchaseOverview {
  return {
    config: { enabled, min_amount: 1, max_amount: 500, remaining_amount_24h: 1000, remaining_count_24h: 20, ...extra },
    items,
    pagination: { total: items.length, page: 1, page_size: 10, pages: 1 }
  }
}

async function mountView(balance = 100) {
  setActivePinia(createPinia())
  const auth = useAuthStore()
  auth.user = { id: 7, email: 'a@example.com', balance } as never
  i18n.global.locale.value = 'zh'
  const w = mount(BillingView, { global: { plugins: [i18n], stubs: { RouterLink: true, Pager: true, ConfirmDialog: DialogStub } } })
  await flushPromises()
  return w
}

enableAutoUnmount(afterEach)

describe('BillingView buy a redeem code', () => {
  beforeEach(() => {
    purchaseApi.overview.mockReset()
    purchaseApi.purchase.mockReset()
    purchaseApi.revoke.mockReset()
    history.mockReset().mockResolvedValue([])
  })

  it('is hidden when buying is off and the user never bought a code', async () => {
    purchaseApi.overview.mockResolvedValue(overview(false))
    const w = await mountView()
    expect(w.find('[data-testid=redeem-purchase]').exists()).toBe(false)
    expect(w.find('[data-testid=purchases-section]').exists()).toBe(false)
  })

  it('keeps past codes visible and revocable when buying is off', async () => {
    purchaseApi.overview.mockResolvedValue(overview(false, [row(1, 'unused'), row(2, 'used'), row(3, 'revoked'), row(4, 'unknown')]))
    const w = await mountView()
    expect(w.find('[data-testid=purchase-form]').exists()).toBe(false)
    expect(w.find('[data-testid=redeem-purchase]').text()).toContain('暂停')
    const list = w.find('[data-testid=purchases-section]')
    expect(list.text()).toContain('未使用')
    expect(list.text()).toContain('已使用')
    expect(list.text()).toContain('已撤回')
    expect(list.text()).toContain('状态异常')
    expect(w.find('[data-testid=purchase-revoke-1]').exists()).toBe(true)
    for (const id of [2, 3, 4]) expect(w.find(`[data-testid=purchase-revoke-${id}]`).exists()).toBe(false)
  })

  it('confirms, buys once with one request id per intent and shows the code', async () => {
    purchaseApi.overview.mockResolvedValue(overview(true))
    const w = await mountView()
    await w.find('[data-testid=purchase-amount]').setValue('12.5')
    await w.find('[data-testid=purchase-form]').trigger('submit')
    expect(w.find('[data-testid=purchase-confirm]').text()).toContain('$12.50')
    expect(w.find('[data-testid=purchase-confirm]').text()).toContain('$87.50')

    // First attempt: the response is lost (network). The retry must reuse the request id.
    purchaseApi.purchase.mockRejectedValueOnce({ status: 0, code: 'NETWORK', message: 'network' })
    await w.find('[data-testid=dialog-confirm]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid=purchase-unknown]').exists()).toBe(true)
    const done = { ...row(9, 'unused'), amount: 12.5, replayed: true }
    purchaseApi.purchase.mockResolvedValueOnce(done)
    await w.find('[data-testid=purchase-form]').trigger('submit')
    await w.find('[data-testid=dialog-confirm]').trigger('click')
    await flushPromises()

    expect(purchaseApi.purchase).toHaveBeenCalledTimes(2)
    const [first, second] = purchaseApi.purchase.mock.calls
    expect(first[0]).toBe('12.5')
    expect(first[1]).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
    expect(second).toEqual(first)
    expect(w.find('[data-testid=purchase-done-code]').text()).toBe(done.code)

    // A new purchase after success is a new intent.
    purchaseApi.purchase.mockResolvedValueOnce({ ...row(10, 'unused'), amount: 12.5, replayed: false })
    await w.find('[data-testid=purchase-form]').trigger('submit')
    await w.find('[data-testid=dialog-confirm]').trigger('click')
    await flushPromises()
    expect(purchaseApi.purchase.mock.calls[2][1]).not.toBe(first[1])
  })

  it('after a lost response the same request can be retried even when the form would now block it', async () => {
    purchaseApi.overview.mockResolvedValueOnce(overview(true))
    const w = await mountView(10)
    await w.find('[data-testid=purchase-amount]').setValue('10')
    await w.find('[data-testid=purchase-form]').trigger('submit')
    // Charged on the server, but the answer is lost; the refresh then shows no balance left.
    purchaseApi.purchase.mockRejectedValueOnce({ status: 0, code: 'NETWORK', message: 'network' })
    purchaseApi.overview.mockResolvedValue(overview(true, [row(9, 'unused')], { remaining_count_24h: 0 }))
    await w.find('[data-testid=dialog-confirm]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid=purchase-unknown]').text()).toContain('不会重复扣款')
    expect(w.find('[data-testid=purchase-submit]').attributes('disabled')).toBeDefined()

    purchaseApi.purchase.mockResolvedValueOnce({ ...row(9, 'unused'), amount: 10, replayed: true })
    await w.find('[data-testid=purchase-retry]').trigger('click')
    await flushPromises()
    const [first, second] = purchaseApi.purchase.mock.calls
    expect(second).toEqual(first)
    expect(w.find('[data-testid=purchase-done-code]').text()).toBe(row(9, 'unused').code)
    expect(w.find('[data-testid=purchase-retry]').exists()).toBe(false)
  })

  for (const listed of [true, false]) {
    it(`the retry survives buying being switched off meanwhile (${listed ? 'charged' : 'not charged'})`, async () => {
      purchaseApi.overview.mockResolvedValueOnce(overview(true))
      const w = await mountView()
      await w.find('[data-testid=purchase-amount]').setValue('25')
      await w.find('[data-testid=purchase-form]').trigger('submit')
      purchaseApi.purchase.mockRejectedValueOnce({ status: 502, message: 'bad gateway' })
      purchaseApi.overview.mockResolvedValue(overview(false, listed ? [row(9, 'unused')] : []))
      await w.find('[data-testid=dialog-confirm]').trigger('click')
      await flushPromises()
      expect(w.find('[data-testid=purchase-form]').exists()).toBe(false)
      expect(w.find('[data-testid=purchase-retry]').exists()).toBe(true)

      purchaseApi.purchase.mockRejectedValueOnce({ status: 403, reason: 'REDEEM_PURCHASE_DISABLED', message: 'off' })
      await w.find('[data-testid=purchase-retry]').trigger('click')
      await flushPromises()
      const [first, second] = purchaseApi.purchase.mock.calls
      expect(second).toEqual(first)
      expect(first[0]).toBe('25')
      // A definite answer ends the unknown state.
      expect(w.find('[data-testid=purchase-retry]').exists()).toBe(false)
    })
  }

  it('a definite refusal offers no blind retry', async () => {
    purchaseApi.overview.mockResolvedValue(overview(true))
    purchaseApi.purchase.mockRejectedValueOnce({ status: 429, reason: 'REDEEM_PURCHASE_WINDOW_COUNT_LIMIT', message: 'limit' })
    const w = await mountView()
    await w.find('[data-testid=purchase-form]').trigger('submit')
    await w.find('[data-testid=dialog-confirm]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid=purchase-error]').text()).toContain('次数上限')
    expect(w.find('[data-testid=purchase-retry]').exists()).toBe(false)
  })

  it('a changed amount is a new intent', async () => {
    purchaseApi.overview.mockResolvedValue(overview(true))
    purchaseApi.purchase.mockRejectedValue({ status: 400, reason: 'INSUFFICIENT_BALANCE', message: 'insufficient balance' })
    const w = await mountView()
    await w.find('[data-testid=purchase-amount]').setValue('10')
    await w.find('[data-testid=purchase-form]').trigger('submit')
    await w.find('[data-testid=dialog-confirm]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid=purchase-error]').text()).toBe('余额不足。')
    await w.find('[data-testid=purchase-amount]').setValue('11')
    await w.find('[data-testid=purchase-form]').trigger('submit')
    await w.find('[data-testid=dialog-confirm]').trigger('click')
    await flushPromises()
    const [a, b] = purchaseApi.purchase.mock.calls
    expect(b[0]).toBe('11')
    expect(b[1]).not.toBe(a[1])
  })

  it('rejects more than two decimals and explains client-side blockers', async () => {
    purchaseApi.overview.mockResolvedValue(overview(true, [], { remaining_amount_24h: 15, remaining_count_24h: 3 }))
    const w = await mountView(12)
    const submit = () => w.find('[data-testid=purchase-submit]')
    await w.find('[data-testid=purchase-amount]').setValue('1.005')
    expect(submit().attributes('disabled')).toBeDefined()
    await w.find('[data-testid=purchase-amount]').setValue('10.500')
    expect(submit().attributes('disabled')).toBeDefined()
    await w.find('[data-testid=purchase-amount]').setValue('10.')
    expect(submit().attributes('disabled')).toBeDefined()
    await w.find('[data-testid=purchase-amount]').setValue('20')
    expect(w.find('[data-testid=purchase-blocker]').text()).toContain('24 小时')
    await w.find('[data-testid=purchase-amount]').setValue('13')
    expect(w.find('[data-testid=purchase-blocker]').text()).toBe('余额不足。')
    await w.find('[data-testid=purchase-amount]').setValue('12')
    expect(w.find('[data-testid=purchase-blocker]').exists()).toBe(false)
    expect(submit().attributes('disabled')).toBeUndefined()
    expect(w.find('[data-testid=purchase-remaining]').text()).toContain('$15.00')
  })

  it('revokes an unused code after confirmation', async () => {
    purchaseApi.overview.mockResolvedValue(overview(true, [row(1, 'unused')]))
    purchaseApi.revoke.mockResolvedValue({ ...row(1, 'revoked') })
    const w = await mountView()
    await w.find('[data-testid=purchase-revoke-1]').trigger('click')
    expect(w.find('[data-testid=dialog-danger]').text()).toContain('$10.00')
    await w.find('[data-testid=dialog-danger] [data-testid=dialog-confirm]').trigger('click')
    await flushPromises()
    expect(purchaseApi.revoke).toHaveBeenCalledWith(1)
    expect(purchaseApi.overview).toHaveBeenCalledTimes(2)
  })

  it('shows debit and refund records as history lines, not codes', async () => {
    purchaseApi.overview.mockResolvedValue(overview(false))
    history.mockResolvedValue([
      { id: 1, code: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', type: 'purchase_debit', value: -12.5, status: 'used', used_at: '2026-11-16T08:00:00Z', created_at: '2026-11-16T08:00:00Z' },
      { id: 2, code: 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', type: 'purchase_refund', value: 12.5, status: 'used', used_at: '2026-11-16T09:00:00Z', created_at: '2026-11-16T09:00:00Z' }
    ])
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('购买兑换码')
    expect(text).toContain('−$12.50')
    expect(text).toContain('撤回退款')
    expect(text).toContain('+$12.50')
    expect(text).not.toContain('aaaaaaaa')
    expect(text).not.toContain('bbbbbbbb')
  })

  it('a failed load shows a retry instead of hiding the section', async () => {
    purchaseApi.overview.mockRejectedValueOnce({ status: 0, message: 'network' })
    const w = await mountView()
    const section = w.find('[data-testid=redeem-purchase]')
    expect(section.exists()).toBe(true)
    purchaseApi.overview.mockResolvedValueOnce(overview(true))
    await section.find('button').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid=purchase-form]').exists()).toBe(true)
  })
})
