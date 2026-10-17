import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UsageStatsCards from '../UsageStatsCards.vue'
import type { AdminUsageStatsResponse } from '@/api/admin/usage'

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
  'usage.inputCacheReadRate': 'Input cache read rate',
  'usage.inputCacheReadRateHint': 'Cache read ÷ (uncached input + cache write + cache read), excluding output tokens.',
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
    expect(wrapper.text()).toContain('Input cache read rate 16.7%')
    expect(wrapper.get('[data-testid="input-cache-read-rate"]').attributes('title')).toBe(messages['usage.inputCacheReadRateHint'])
    expect(wrapper.text()).toContain('User billed $6.0000')
    expect(wrapper.text()).toContain('Configured base: $10.0000')
    expect(wrapper.text()).toContain('Account billed: $8.0000')
  })

  it.each([
    { name: 'the reported user totals', input: 3410, write: 369140, read: 1360000, output: 94670, expected: '78.5%' },
    { name: 'current reported totals', input: 478630, write: 5350000, read: 293380000, output: 1390000, expected: '98.1%' },
    { name: 'large outputs', input: 100, write: 25, read: 25, output: 1000000, expected: '16.7%' },
    { name: 'uncached inputs', input: 100, write: 0, read: 0, output: 50, expected: '0.0%' },
    { name: 'writes without reuse', input: 0, write: 100, read: 0, output: 50, expected: '0.0%' },
    { name: 'fully cached inputs', input: 0, write: 0, read: 100, output: 50, expected: '100.0%' },
    { name: 'no input tokens', input: 0, write: 0, read: 0, output: 50, expected: '—' },
    { name: 'empty totals', input: 0, write: 0, read: 0, output: 0, expected: '—' },
  ])('shows the input cache read rate for $name', ({ input, write, read, output, expected }) => {
    const stats: AdminUsageStatsResponse = {
      total_requests: 12,
      total_input_tokens: input,
      total_output_tokens: output,
      total_cache_creation_tokens: write,
      total_cache_read_tokens: read,
      total_cache_tokens: write + read,
      total_tokens: input + write + read + output,
      total_cost: 0,
      total_actual_cost: 0,
      average_duration_ms: 0,
    }
    const wrapper = mount(UsageStatsCards, {
      props: { stats },
      global: { stubs: { Icon: true } },
    })

    expect(wrapper.text()).toContain(`Input cache read rate ${expected}`)
    expect(wrapper.text()).not.toContain('Cache share')
  })

  it('shows no rate while statistics are unavailable', () => {
    const wrapper = mount(UsageStatsCards, {
      props: { stats: null },
      global: { stubs: { Icon: true } },
    })

    expect(wrapper.text()).toContain('Input cache read rate —')
    expect(wrapper.text()).not.toContain('NaN')
  })
})
