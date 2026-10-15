import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import type { UserDashboardStats } from '@/api/usage'
import type { UsageLog } from '@/types'
import UserDashboardStatsView from '../UserDashboardStats.vue'
import UserDashboardRecentUsage from '../UserDashboardRecentUsage.vue'

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))
const wrappers: ReturnType<typeof mount>[] = []
afterEach(() => wrappers.splice(0).forEach(wrapper => wrapper.unmount()))

describe('dashboard Token totals', () => {
  it('shows cache read and write for both today and cumulative cards', () => {
    const stats = { today_tokens: 21620, today_input_tokens: 14906, today_output_tokens: 54, today_cache_read_tokens: 6500, today_cache_creation_tokens: 160,
      total_tokens: 21620, total_input_tokens: 14906, total_output_tokens: 54, total_cache_read_tokens: 6500, total_cache_creation_tokens: 160 } as UserDashboardStats
    const wrapper = mount(UserDashboardStatsView, { props: { stats, balance: 0, isSimple: false }, global: { stubs: { Icon: true } } })
    wrappers.push(wrapper)
    const cards = wrapper.findAll('[data-testid="token-usage-summary"]')
    expect(cards).toHaveLength(2)
    for (const card of cards) {
      expect(card.text()).toContain('21.62K')
      expect(card.text()).toContain('tokens.input: 14.91K')
      expect(card.text()).toContain('tokens.cacheRead: 6.5K')
      expect(card.text()).toContain('tokens.cacheWrite: 160')
    }
  })

  it('includes cache in recent totals and preserves unavailable native Chat usage', () => {
    const data = [
      { id: 1, model: 'mock-model', input_tokens: 10, output_tokens: 5, cache_read_tokens: 100, cache_creation_tokens: 20, total_cost: .1, actual_cost: .1, created_at: '2026-01-01T00:00:00Z' },
      { id: 2, model: 'mock-chat', billing_unit: 'turn', input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_creation_tokens: 0, total_cost: .1, actual_cost: .1, created_at: '2026-01-01T00:00:00Z' }
    ] as UsageLog[]
    const wrapper = mount(UserDashboardRecentUsage, { props: { data, loading: false }, global: { stubs: { Icon: true, RouterLink: true } } })
    wrappers.push(wrapper)
    expect(wrapper.findAll('[data-testid="token-usage-summary"]')).toHaveLength(1)
    expect(wrapper.get('[data-testid="token-usage-summary"]').text()).toBe('135')
    expect(wrapper.text()).toContain('usage.nativeChatUsageUnknown')
  })
})
