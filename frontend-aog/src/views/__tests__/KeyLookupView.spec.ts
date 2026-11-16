import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount, RouterLinkStub } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { i18n } from '@/i18n'
import KeyLookupView from '../KeyLookupView.vue'

const fetchMock = vi.fn()

async function lookup(body: Record<string, unknown>) {
  fetchMock.mockResolvedValue({ ok: true, status: 200, json: () => Promise.resolve(body) })
  setActivePinia(createPinia())
  i18n.global.locale.value = 'zh'
  const w = mount(KeyLookupView, { global: { plugins: [i18n], stubs: { RouterLink: RouterLinkStub } } })
  await w.find('input').setValue('sk-test')
  await w.find('form').trigger('submit')
  await flushPromises()
  return w
}

const usage = { range: { requests: 4, actual_cost: 0.06, cost: 0.05, usage_cost: 0.05 } }

enableAutoUnmount(afterEach)

describe('KeyLookupView hosted keys', () => {
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('shows the hosting fee rate and the wallet for a charged hosted key', async () => {
    const w = await lookup({
      isValid: true, status: 'active', mode: 'unrestricted', billing_mode: 'balance',
      planName: '自有账号托管', usage_basis: 'standard_reference', hosted_fee_rate: 1.2,
      billing: { type: 'wallet', available: true, balance_visible: true, balance: 12.5 },
      usage
    })
    expect(w.find('[data-testid=lookup-plan]').text()).toBe('自有账号托管 · 服务费 1.2× 标准价')
    expect(w.find('[data-testid=lookup-remaining]').text()).toBe('$12.50')
    expect(w.find('[data-testid=lookup-hosted-standard]').text()).toContain('$0.0500')
    expect(w.text()).not.toContain('不扣费')
  })

  it('keeps a free hosted key (charge_exempt) as not charged, without a balance', async () => {
    const w = await lookup({
      isValid: true, status: 'active', mode: 'unrestricted', billing_mode: 'balance',
      planName: '自有账号托管', usage_basis: 'standard_reference',
      billing: { type: 'wallet', available: true, balance_visible: false, charge_exempt: true },
      usage
    })
    expect(w.find('[data-testid=lookup-remaining]').text()).toBe('自有账号托管 · 不扣费')
    expect(w.find('[data-testid=lookup-plan]').exists()).toBe(false)
    expect(w.text().match(/不扣费/g)).toHaveLength(1)
    expect(w.find('[data-testid=lookup-hosted-standard]').exists()).toBe(true)
  })

  it('keeps the key quota and still names free hosting when the key has a quota', async () => {
    const w = await lookup({
      isValid: true, status: 'active', mode: 'quota_limited', billing_mode: 'balance',
      planName: '自有账号托管', usage_basis: 'standard_reference',
      billing: { type: 'wallet', available: true, balance_visible: false, charge_exempt: true },
      quota: { limit: 10, used: 4, remaining: 6 },
      usage
    })
    expect(w.text()).toContain('Key 剩余额度')
    expect(w.find('[data-testid=lookup-remaining]').text()).toBe('$6.00')
    expect(w.find('[data-testid=lookup-plan]').text()).toBe('自有账号托管 · 不扣费')
  })

  it('shows the fee with a key quota, and ignores a missing or non-numeric fee rate', async () => {
    const charged = await lookup({
      isValid: true, status: 'active', mode: 'quota_limited', billing_mode: 'balance', planName: '自有账号托管',
      hosted_fee_rate: 2, billing: { type: 'wallet', available: true, balance_visible: false },
      quota: { limit: 10, used: 1, remaining: 9 }, usage
    })
    expect(charged.find('[data-testid=lookup-remaining]').text()).toBe('$9.00')
    expect(charged.find('[data-testid=lookup-plan]').text()).toBe('自有账号托管 · 服务费 2× 标准价')
    // Period charge is the fee actually charged; the standard-price line is separate.
    expect(charged.text()).toContain('$0.0600')
    expect(charged.find('[data-testid=lookup-hosted-standard]').text()).toContain('$0.0500')

    const odd = await lookup({
      isValid: true, status: 'active', mode: 'unrestricted', billing_mode: 'balance', planName: '钱包余额',
      hosted_fee_rate: 'x', billing: { type: 'wallet', available: true, balance_visible: true, balance: 1 }, usage
    })
    expect(odd.find('[data-testid=lookup-plan]').text()).toBe('钱包余额')
    expect(odd.find('[data-testid=lookup-hosted-standard]').exists()).toBe(false)
  })

  it('leaves ordinary wallet keys as before', async () => {
    const w = await lookup({
      isValid: true, status: 'active', mode: 'unrestricted', billing_mode: 'balance', planName: '钱包余额',
      billing: { type: 'wallet', available: true, balance_visible: true, balance: 3 },
      usage: { range: { requests: 1, actual_cost: 0.01 } }
    })
    expect(w.find('[data-testid=lookup-plan]').text()).toBe('钱包余额')
    expect(w.find('[data-testid=lookup-remaining]').text()).toBe('$3.00')
    expect(w.find('[data-testid=lookup-hosted-standard]').exists()).toBe(false)
    expect(w.text()).not.toContain('托管')
  })
})
