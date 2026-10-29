import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountTodayStatsCell from '../AccountTodayStatsCell.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const base = { requests: 2274, tokens: 341_000_000, cost: 0, standard_cost: 213.37, user_cost: 0 }

describe('AccountTodayStatsCell', () => {
  it('shows the hosted reference amount when hosted requests exist', () => {
    const wrapper = mount(AccountTodayStatsCell, {
      props: { stats: { ...base, hosted_requests: 2274, hosted_cost: 213.37 } }
    })
    expect(wrapper.find('[data-testid="today-stats-hosted"]').text()).toContain('hostedUsage.reference')
    expect(wrapper.text()).toContain('≈$213.37')
    expect(wrapper.find('[data-testid="today-stats-hosted"]').attributes('aria-label')).toContain('≈$213.37')
    // Account and user billed amounts keep their own basis
    expect(wrapper.text()).toContain('usage.accountBilled:$0.00')
  })

  it('keeps the hosted line for hosted requests with a zero reference price', () => {
    const wrapper = mount(AccountTodayStatsCell, {
      props: { stats: { ...base, hosted_requests: 3, hosted_cost: 0 } }
    })
    expect(wrapper.text()).toContain('≈$0.00')
  })

  it.each([
    ['zero hosted requests', { hosted_requests: 0, hosted_cost: 0 }],
    ['an older response without hosted fields', {}]
  ])('omits the hosted line for %s', (_, extra) => {
    const wrapper = mount(AccountTodayStatsCell, { props: { stats: { ...base, ...extra } } })
    expect(wrapper.find('[data-testid="today-stats-hosted"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('≈')
  })
})
