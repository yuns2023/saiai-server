import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AccountStatsModal from '../AccountStatsModal.vue'
import type { Account } from '@/types'
const { getStats } = vi.hoisted(() => ({ getStats: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getStats } } }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-chartjs', () => ({ Line: { template: '<div />' } }))

const stats = (requests: number) => ({
  summary: { total_requests: requests, total_cost: 0, total_user_cost: 0, total_standard_cost: 0, avg_daily_cost: 0, avg_daily_user_cost: 0, avg_daily_requests: 0, total_tokens: 0, avg_daily_tokens: 0, avg_duration_ms: 0, actual_days_used: 0, days: 30 },
  history: [], models: [], endpoints: [], upstream_endpoints: []
})
describe('AccountStatsModal', () => {
  it('defaults to 30 days and ignores stale responses after changing the range', async () => {
    getStats.mockResolvedValueOnce(stats(10))
    const wrapper = mount(AccountStatsModal, {
      props: { show: false, account: { id: 1, name: 'Test', status: 'active' } as Account },
      global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, DateTimeRangePicker: true, ModelDistributionChart: true, EndpointDistributionChart: true, LoadingSpinner: true } }
    })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(getStats.mock.calls[0][1]).toEqual(expect.objectContaining({ timezone: expect.any(String), start_time: expect.any(String), end_time: expect.any(String) }))
    expect(wrapper.findComponent({ name: 'DateTimeRangePicker' }).props('preset')).toBe('30days')
    let finishOld!: (value: ReturnType<typeof stats>) => void
    getStats.mockImplementationOnce(() => new Promise(resolve => { finishOld = resolve }))
    const picker = wrapper.findComponent({ name: 'DateTimeRangePicker' })
    picker.vm.$emit('change', { startTime: '2026-10-17T10:00:00Z', endTime: '2026-10-17T11:00:00Z', preset: null })
    await flushPromises()
    getStats.mockResolvedValueOnce(stats(222))
    picker.vm.$emit('change', { startTime: '2026-10-17T12:00:00Z', endTime: '2026-10-17T13:00:00Z', preset: null })
    await flushPromises()
    expect(getStats.mock.calls.at(-1)![1].start_time).toBe('2026-10-17T12:00:00Z')
    finishOld(stats(111))
    await flushPromises()
    expect(wrapper.text()).toContain('222')
    expect(wrapper.text()).not.toContain('111')
    wrapper.unmount()
  })

  it.each([
    ['a priced hosted share', 9.5, '≈$9.50'],
    ['hosted requests with a zero reference price', 0, '≈$0.0000']
  ])('shows account billed without the duplicate standard cost and adds the hosted reference for %s', async (_, hostedCost, expected) => {
    const hosted = stats(5)
    getStats.mockResolvedValueOnce({
      ...hosted,
      summary: { ...hosted.summary, total_standard_cost: hostedCost, total_hosted_requests: 5, total_hosted_cost: hostedCost }
    })
    const wrapper = mount(AccountStatsModal, {
      props: { show: false, account: { id: 2, name: 'Hosted', status: 'active' } as Account },
      global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, DateTimeRangePicker: true, ModelDistributionChart: true, EndpointDistributionChart: true, LoadingSpinner: true } }
    })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.text()).not.toContain('admin.accounts.stats.standardCost')
    expect(wrapper.get('[data-testid="account-stats-hosted-cost"]').text()).toBe(`admin.accounts.stats.hostedReference ${expected}`)
    wrapper.unmount()
  })

  it.each([
    ['explicit zero hosted fields', { total_hosted_requests: 0, total_hosted_cost: 0 }],
    ['an older response without hosted fields', {}]
  ])('omits the hosted reference and scopes drill-downs to the account for %s', async (_, extra) => {
    const plain = stats(5)
    getStats.mockResolvedValueOnce({ ...plain, summary: { ...plain.summary, ...extra }, upstream_endpoints: [{ endpoint: '/v1/messages', requests: 5, total_tokens: 2, cost: 1, actual_cost: 1 }] })
    const wrapper = mount(AccountStatsModal, {
      props: { show: false, account: { id: 3, name: 'Plain', status: 'active' } as Account },
      global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, DateTimeRangePicker: true, ModelDistributionChart: true, EndpointDistributionChart: true, LoadingSpinner: true } }
    })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.find('[data-testid="account-stats-hosted-cost"]').exists()).toBe(false)
    // Distribution tables are told that actual_cost is the account cost here
    const model = wrapper.findComponent({ name: 'ModelDistributionChart' })
    const endpoints = wrapper.findAllComponents({ name: 'EndpointDistributionChart' })
    for (const chart of [model, ...endpoints]) {
      expect(chart.props('accountCostBasis')).toBe(true)
      expect(chart.props('breakdownFilters')).toEqual({ account_id: 3 })
      expect(chart.props('startTime')).toEqual(expect.any(String))
      expect(chart.props('endTime')).toEqual(expect.any(String))
    }
    expect(endpoints[0].props('source')).toBe('inbound')
    expect(endpoints[1].props('source')).toBe('upstream')
    expect(endpoints[1].props('upstreamEndpointStats')).toHaveLength(1)
    wrapper.unmount()
  })

  it('shows revenue (balance + converted subscription) for total, daily average, today and the highest days', async () => {
    const base = stats(5)
    const dayRevenue = { balance_cost: 3, subscription_cost: 40, subscription_revenue: 20, subscription_revenue_unpriced_cost: 0, unclassified_cost: 0 }
    getStats.mockResolvedValueOnce({
      ...base,
      summary: {
        ...base.summary,
        actual_days_used: 2,
        total_user_cost: 52,
        avg_daily_user_cost: 26,
        revenue: { balance_cost: 12, subscription_cost: 40, subscription_revenue: 20, subscription_revenue_unpriced_cost: 6, unclassified_cost: 0 },
        today: { date: '2026-04-05', cost: 43, user_cost: 43, requests: 2, tokens: 20, revenue: dayRevenue },
        highest_cost_day: { date: '2026-04-05', label: '04/05', cost: 43, user_cost: 43, requests: 2, revenue: dayRevenue },
        highest_request_day: { date: '2026-04-05', label: '04/05', cost: 43, user_cost: 43, requests: 2, revenue: dayRevenue },
      },
    })
    const wrapper = mount(AccountStatsModal, {
      props: { show: false, account: { id: 4, name: 'Revenue', status: 'active' } as Account },
      global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, DateTimeRangePicker: true, ModelDistributionChart: true, EndpointDistributionChart: true, LoadingSpinner: true } }
    })
    await wrapper.setProps({ show: true })
    await flushPromises()
    const text = (id: string) => wrapper.get(`[data-testid="${id}"]`).text().replace(/\s+/g, '')
    expect(text('account-stats-total-revenue')).toBe('32.00+$6.00?')
    expect(text('account-stats-avg-revenue')).toBe('16.00+$3.00?') // per day with usage
    expect(text('account-stats-today-revenue')).toBe('23.00')
    expect(text('account-stats-highest-cost-day-revenue')).toBe('23.00')
    expect(text('account-stats-highest-request-day-revenue')).toBe('23.00')
    expect(wrapper.text()).not.toContain('usage.userBilled')
    wrapper.unmount()
  })

  it('shows 0.00 revenue for an older response without revenue parts', async () => {
    getStats.mockResolvedValueOnce(stats(1))
    const wrapper = mount(AccountStatsModal, {
      props: { show: false, account: { id: 5, name: 'Old', status: 'active' } as Account },
      global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, DateTimeRangePicker: true, ModelDistributionChart: true, EndpointDistributionChart: true, LoadingSpinner: true } }
    })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.get('[data-testid="account-stats-total-revenue"]').text()).toBe('0.00')
    wrapper.unmount()
  })
})
