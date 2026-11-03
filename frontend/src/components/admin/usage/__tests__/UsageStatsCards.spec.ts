import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UsageStatsCards from '../UsageStatsCards.vue'

const messages: Record<string, string> = {
  'tokens.input': 'Uncached input',
  'tokens.output': 'Output',
  'tokens.cacheWrite': 'Cache write',
  'tokens.cacheRead': 'Cache read',
  'usage.totalRequests': 'Total Requests',
  'usage.totalTokens': 'Total Tokens',
  'usage.in': 'In',
  'usage.out': 'Out',
  'usage.cacheWrite': 'Cache write',
  'usage.cacheRead': 'Cache read',
  'admin.accounts.stats.hostedReference': 'Hosted usage',
  'usage.balanceCharge': 'Balance charged',
  'usage.subscriptionUsage': 'Subscription quota used',
  'usage.standardCost': 'Configured base',
  'usage.accountBilled': 'Account billed',
  'usage.avgDuration': 'Average duration',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

describe('UsageStatsCards', () => {
  it('explains the token total and distinguishes the three cost concepts', () => {
    const wrapper = mount(UsageStatsCards, {
      props: {
        stats: {
          total_requests: 12,
          total_input_tokens: 100,
          total_output_tokens: 50,
          total_cache_creation_tokens: 25,
          total_cache_read_tokens: 25,
          total_cache_tokens: 50,
          total_tokens: 200,
          total_cost: 10,
          total_actual_cost: 6,
          total_balance_cost: 2,
          total_subscription_cost: 4,
          total_account_cost: 8,
          average_duration_ms: 1250,
        },
      },
      global: {
        stubs: { Icon: true },
      },
    })

    expect(wrapper.text()).toContain('Cache write: 25')
    expect(wrapper.text()).toContain('Cache read: 25')
    // One cache read rate (from the token summary), not a second rounded copy
    expect(wrapper.text().match(/%/g)).toHaveLength(1)
    expect(wrapper.text()).toContain('tokens.cacheReadRate: 16.67%')
    const compact = wrapper.text().replace(/\s+/g, '')
    expect(compact).toContain('Balancecharged$2.0000')
    expect(compact).toContain('Subscriptionquotaused$4.0000')
    expect(compact).not.toContain('$6.0000')
    // Configured base equals account billed at 1x; only account billed is shown
    expect(compact).not.toContain('Configuredbase')
    expect(compact).not.toContain('$10.0000')
    expect(compact).toContain('Accountbilled$8.0000')
    expect(wrapper.find('[data-testid="usage-stats-hosted-cost"]').exists()).toBe(false)
  })

  it('shows the hosted reference next to account billed when hosted rows exist', () => {
    const wrapper = mount(UsageStatsCards, {
      props: {
        stats: {
          total_requests: 3, total_input_tokens: 0, total_output_tokens: 0, total_cache_creation_tokens: 0,
          total_cache_read_tokens: 0, total_cache_tokens: 0, total_tokens: 0, total_cost: 5, total_actual_cost: 0,
          total_account_cost: 0, total_metered_cost: 4.5, average_duration_ms: 0,
        },
      },
      global: { stubs: { Icon: true } },
    })
    const compact = wrapper.text().replace(/\s+/g, '')
    expect(compact).toContain('Accountbilled$0.0000')
    expect(wrapper.get('[data-testid="usage-stats-hosted-cost"]').text().replace(/\s+/g, '')).toBe('Hostedusage≈$4.5000')
  })

  it('shows no NaN while statistics are unavailable', () => {
    const wrapper = mount(UsageStatsCards, {
      props: { stats: null },
      global: { stubs: { Icon: true } },
    })

    expect(wrapper.text()).not.toContain('NaN')
  })
})
