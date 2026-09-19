import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UsageStatsCards from '../UsageStatsCards.vue'

const messages: Record<string, string> = {
  'usage.totalRequests': 'Total Requests',
  'usage.totalTokens': 'Total Tokens',
  'usage.in': 'In',
  'usage.out': 'Out',
  'usage.cacheWrite': 'Cache write',
  'usage.cacheRead': 'Cache read',
  'usage.cacheShare': 'Cache share',
  'usage.userBilled': 'User billed',
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
    expect(wrapper.text()).toContain('Cache share 25.0%')
    expect(wrapper.text()).toContain('User billed $6.0000')
    expect(wrapper.text()).toContain('Configured base: $10.0000')
    expect(wrapper.text()).toContain('Account billed: $8.0000')
  })
})
