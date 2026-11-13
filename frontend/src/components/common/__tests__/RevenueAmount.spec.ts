import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import RevenueAmount from '../RevenueAmount.vue'
import UsageStatsCards from '@/components/admin/usage/UsageStatsCards.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string>) =>
        params ? `${key}(${Object.values(params).join(',')})` : key,
    }),
  }
})

describe('RevenueAmount', () => {
  it('adds balance charges and converted subscription without a currency', () => {
    // Balance $932.70 + subscription $808.77 at rate 0.64; rounds to 2 decimals.
    const wrapper = mount(RevenueAmount, {
      props: {
        label: 'U ',
        parts: { balance_cost: 932.7, subscription_cost: 808.77, subscription_revenue: 517.6128, subscription_revenue_unpriced_cost: 0 },
        billedCost: 1741.47,
      },
    })
    expect(wrapper.text()).toBe('U 1450.31')
    const title = wrapper.attributes('title')!
    expect(title).toContain('usage.revenueBalance($932.7000)')
    expect(title).toContain('usage.revenueSubscription($808.7700,517.6128)')
    expect(title).toContain('usage.revenueBilled($1741.4700)')
    expect(title).not.toContain('usage.subscriptionRevenueUnpriced')
    expect(title).not.toContain('usage.revenueUnclassified')
  })

  it('flags unpriced subscription usage and lists unclassified charges without counting them', () => {
    const wrapper = mount(RevenueAmount, {
      props: { parts: { balance_cost: 1, subscription_revenue: 2, subscription_revenue_unpriced_cost: 7, unclassified_cost: 4 } },
    })
    expect(wrapper.text().replace(/\s+/g, '')).toBe('3.00+$7.00?')
    const title = wrapper.attributes('title')!
    expect(title).toContain('usage.subscriptionRevenueUnpriced($7.0000)')
    expect(title).toContain('usage.revenueUnclassified($4.0000)')
    expect(title).not.toContain('usage.revenueBilled')
  })

  it('shows sub-cent revenue as 0.00 but keeps the exact parts in the title', () => {
    const wrapper = mount(RevenueAmount, { props: { parts: { balance_cost: 0.0049 } } })
    expect(wrapper.text()).toBe('0.00')
    expect(wrapper.attributes('title')).toContain('usage.revenueBalance($0.0049)')
  })

  it('treats missing parts as zero', () => {
    const wrapper = mount(RevenueAmount, { props: { parts: {} } })
    expect(wrapper.text()).toBe('0.00')
    expect(wrapper.find('[data-testid="revenue-amount-unpriced"]').exists()).toBe(false)
  })
})

describe('UsageStatsCards revenue', () => {
  const base = {
    total_requests: 1, total_input_tokens: 0, total_output_tokens: 0, total_cache_creation_tokens: 0,
    total_cache_read_tokens: 0, total_cache_tokens: 0, total_tokens: 0, total_cost: 1, total_actual_cost: 1,
    total_account_cost: 1, average_duration_ms: 0,
  }
  it('renders only when the admin summary carries revenue', () => {
    const without = mount(UsageStatsCards, { props: { stats: base }, global: { stubs: { Icon: true } } })
    expect(without.find('[data-testid="usage-stats-revenue"]').exists()).toBe(false)
    const withRevenue = mount(UsageStatsCards, {
      props: {
        stats: {
          ...base, total_balance_cost: 100, total_subscription_cost: 50, total_subscription_revenue: 32,
          subscription_revenue_unpriced_cost: 5,
        },
      },
      global: { stubs: { Icon: true } },
    })
    expect(withRevenue.get('[data-testid="usage-stats-revenue"]').text().replace(/\s+/g, '')).toBe('usage.revenue132.00+$5.00?')
  })
})
