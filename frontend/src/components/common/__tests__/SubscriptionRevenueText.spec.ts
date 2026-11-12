import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import SubscriptionRevenueText from '../SubscriptionRevenueText.vue'
import UsageStatsCards from '@/components/admin/usage/UsageStatsCards.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string>) => (params?.cost ? `${key}(${params.cost})` : key),
    }),
  }
})

describe('SubscriptionRevenueText', () => {
  it('shows the priced revenue and reports unpriced consumption separately', () => {
    const wrapper = mount(SubscriptionRevenueText, { props: { revenue: 64, unpricedCost: 30 } })
    const text = wrapper.text().replace(/\s+/g, '')
    expect(text).toContain('usage.subscriptionRevenue≈¥64.00')
    expect(wrapper.get('[data-testid="subscription-revenue-unpriced"]').text()).toBe('usage.subscriptionRevenueUnpriced($30.0000)')
  })

  it('treats a configured free group (0 revenue, nothing unpriced) as ¥0 without a warning', () => {
    const wrapper = mount(SubscriptionRevenueText, { props: { revenue: 0, unpricedCost: 0 } })
    expect(wrapper.text()).toContain('≈¥0.00')
    expect(wrapper.find('[data-testid="subscription-revenue-unpriced"]').exists()).toBe(false)
  })

  it('compact mode keeps the unpriced amount visible and moves the explanation to the title', () => {
    const wrapper = mount(SubscriptionRevenueText, { props: { revenue: 5, unpricedCost: 7, compact: true } })
    expect(wrapper.text().replace(/\s+/g, '')).toBe('≈¥5.00+$7.00?')
    expect(wrapper.attributes('title')).toContain('usage.subscriptionRevenueUnpriced($7.0000)')
  })
})

describe('UsageStatsCards subscription revenue', () => {
  const base = {
    total_requests: 1, total_input_tokens: 0, total_output_tokens: 0, total_cache_creation_tokens: 0,
    total_cache_read_tokens: 0, total_cache_tokens: 0, total_tokens: 0, total_cost: 1, total_actual_cost: 1,
    total_account_cost: 1, average_duration_ms: 0,
  }
  it('renders only when the admin summary carries it', () => {
    const without = mount(UsageStatsCards, { props: { stats: base }, global: { stubs: { Icon: true } } })
    expect(without.find('[data-testid="subscription-revenue"]').exists()).toBe(false)
    const withRevenue = mount(UsageStatsCards, {
      props: { stats: { ...base, total_subscription_revenue: 687.18, subscription_revenue_unpriced_cost: 0 } },
      global: { stubs: { Icon: true } },
    })
    expect(withRevenue.get('[data-testid="subscription-revenue"]').text()).toContain('≈¥687.18')
  })
})
