import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountTodayStatsCell from '../AccountTodayStatsCell.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

// Account in a hosted group plus a 0.5x pay-as-you-go group (prod #295 on 10-29)
const mixed = { requests: 1482, tokens: 185_100_000, cost: 0.72, standard_cost: 106.46, user_cost: 0.36 }

describe('AccountTodayStatsCell', () => {
  it('shows standard usage with the hosted share for mixed accounts', () => {
    const wrapper = mount(AccountTodayStatsCell, {
      props: { stats: { ...mixed, hosted_requests: 1474, hosted_cost: 105.74 } }
    })
    const standard = wrapper.find('[data-testid="today-stats-standard"]')
    expect(standard.text()).toContain('admin.accounts.stats.standardUsage')
    expect(standard.attributes('aria-label')).toContain('$106.46')
    expect(wrapper.text()).toContain('admin.accounts.stats.standardUsage: $106.46')
    expect(wrapper.find('[data-testid="today-stats-hosted"]').text()).toBe('└ admin.accounts.stats.hostedIncluded ≈$105.74')
    // Account and user billed amounts keep their own basis
    expect(wrapper.text()).toContain('usage.accountBilled:$0.72')
    expect(wrapper.text()).toContain('usage.userBilled:$0.36')
  })

  it('keeps the hosted share for hosted requests with a zero reference price', () => {
    const wrapper = mount(AccountTodayStatsCell, {
      props: { stats: { ...mixed, hosted_requests: 3, hosted_cost: 0 } }
    })
    expect(wrapper.find('[data-testid="today-stats-hosted"]').text()).toContain('≈$0.00')
  })

  it.each([
    ['zero hosted requests', { hosted_requests: 0, hosted_cost: 0 }],
    ['an older response without hosted fields', {}]
  ])('omits the hosted share for %s', (_, extra) => {
    const wrapper = mount(AccountTodayStatsCell, { props: { stats: { ...mixed, ...extra } } })
    expect(wrapper.find('[data-testid="today-stats-standard"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="today-stats-hosted"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('≈')
  })
})
