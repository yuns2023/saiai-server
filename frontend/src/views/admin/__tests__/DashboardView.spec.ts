import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { DashboardStats } from '@/types'
import DashboardView from '../DashboardView.vue'

const { getSnapshotV2, getUserUsageTrend, getUserSpendingRanking } = vi.hoisted(() => ({
  getSnapshotV2: vi.fn(),
  getUserUsageTrend: vi.fn(),
  getUserSpendingRanking: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    dashboard: {
      getSnapshotV2,
      getUserUsageTrend,
      getUserSpendingRanking
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn()
  })
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({
    push: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const createDashboardStats = (): DashboardStats => ({
  total_users: 0,
  today_new_users: 0,
  active_users: 0,
  hourly_active_users: 0,
  stats_updated_at: '',
  stats_stale: false,
  total_api_keys: 0,
  active_api_keys: 0,
  total_accounts: 0,
  normal_accounts: 0,
  error_accounts: 0,
  ratelimit_accounts: 0,
  overload_accounts: 0,
  total_requests: 0,
  total_input_tokens: 0,
  total_output_tokens: 0,
  total_cache_creation_tokens: 0,
  total_cache_read_tokens: 0,
  total_tokens: 0,
  total_cost: 0,
  total_actual_cost: 0,
  today_requests: 0,
  today_input_tokens: 0,
  today_output_tokens: 0,
  today_cache_creation_tokens: 0,
  today_cache_read_tokens: 0,
  today_tokens: 0,
  today_cost: 0,
  today_actual_cost: 0,
  average_duration_ms: 0,
  uptime: 0,
  rpm: 0,
  tpm: 0
})

describe('admin DashboardView', () => {
  beforeEach(() => {
    getSnapshotV2.mockReset()
    getUserUsageTrend.mockReset()
    getUserSpendingRanking.mockReset()

    getSnapshotV2.mockResolvedValue({
      stats: createDashboardStats(),
      trend: [],
      models: []
    })
    getUserUsageTrend.mockResolvedValue({
      trend: [],
      start_date: '',
      end_date: '',
      granularity: 'hour'
    })
    getUserSpendingRanking.mockResolvedValue({
      ranking: [],
      total_cost: 0,
      total_actual_cost: 0,
      total_requests: 0,
      total_tokens: 0,
      start_date: '',
      end_date: ''
    })
  })

  it('uses today as default dashboard range', async () => {
    const wrapper = mount(DashboardView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          LoadingSpinner: true,
          Icon: true,
          DateRangePicker: true,
          Select: true,
          ModelDistributionChart: true,
          TokenUsageTrend: true,
          APIKeySpendingRanking: true,
          Line: true
        }
      }
    })

    await flushPromises()

    expect(getSnapshotV2).toHaveBeenCalledTimes(1)
    const params = getSnapshotV2.mock.calls[0][0]
    const now = new Date()
    const today = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`
    expect(params).toMatchObject({ start_date: today, end_date: today, granularity: 'hour' })
    expect(params.start_time).toBeUndefined()
    expect(params.end_time).toBeUndefined()
    expect(getUserUsageTrend).toHaveBeenCalledWith(expect.objectContaining({ start_date: today, end_date: today }))
    expect(getUserSpendingRanking).toHaveBeenCalledWith(expect.objectContaining({ start_date: today, end_date: today }))
    expect(wrapper.findComponent({ name: 'APIKeySpendingRanking' }).props()).toEqual(expect.objectContaining({
      startDate: today,
      endDate: today,
      startTime: undefined,
      endTime: undefined,
    }))
    wrapper.unmount()
  })

  const mountDashboard = () => mount(DashboardView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        LoadingSpinner: true,
        Icon: true,
        DateRangePicker: true,
        Select: true,
        ModelDistributionChart: true,
        TokenUsageTrend: true,
        APIKeySpendingRanking: true,
        TokenCount: { props: ['value'], template: '<span>{{ value }}</span>' },
        Line: true
      }
    }
  })

  it('requests the hosted-free platform view and matching model drill-down', async () => {
    const wrapper = mountDashboard()
    await flushPromises()

    expect(getSnapshotV2.mock.calls[0][0]).toEqual(expect.objectContaining({ exclude_hosted: true }))
    expect(wrapper.findComponent({ name: 'ModelDistributionChart' }).props('breakdownFilters')).toEqual({ exclude_hosted: true })
    expect(wrapper.find('[data-testid="hosted-usage"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows hosted usage as a separate line', async () => {
    getSnapshotV2.mockResolvedValue({
      stats: {
        ...createDashboardStats(),
        total_requests: 10,
        hosted_total_requests: 1234,
        hosted_total_tokens: 56789,
        hosted_total_metered_cost: 12.5,
        hosted_today_requests: 7,
        hosted_today_tokens: 890,
        hosted_today_metered_cost: 0.25,
        hosted_total_service_fee: 1.25,
        hosted_today_service_fee: 0.025
      },
      trend: [],
      models: []
    })
    const wrapper = mountDashboard()
    await flushPromises()

    const line = wrapper.find('[data-testid="hosted-usage"]')
    expect(line.exists()).toBe(true)
    expect(line.text()).toContain('admin.dashboard.hostedUsage')
    expect(line.find('[title="admin.dashboard.hostedUsageTooltip"]').exists()).toBe(true)
    expect(line.text()).toContain('1,234')
    expect(line.text()).toContain('56789')
    expect(line.text()).toContain('890')
    expect(line.text()).toContain('$12.50')
    expect(line.text()).toContain('$0.250')
    const todayFee = line.get('[data-testid="hosted-service-fee-today"]')
    const totalFee = line.get('[data-testid="hosted-service-fee-total"]')
    expect(todayFee.text()).toBe('admin.dashboard.hostedServiceFee $0.025')
    expect(totalFee.text()).toBe('admin.dashboard.hostedServiceFee $1.25')
    expect(todayFee.attributes('title')).toBe('admin.dashboard.hostedServiceFeeHint')
    expect(totalFee.attributes('title')).toBe('admin.dashboard.hostedServiceFeeHint')
    wrapper.unmount()
  })

  it('shows a zero service fee when the backend predates the fee fields', async () => {
    getSnapshotV2.mockResolvedValue({
      stats: { ...createDashboardStats(), hosted_total_requests: 5, hosted_total_metered_cost: 1, hosted_today_requests: 1 },
      trend: [],
      models: []
    })
    const wrapper = mountDashboard()
    await flushPromises()
    expect(wrapper.get('[data-testid="hosted-service-fee-total"]').text()).toBe('admin.dashboard.hostedServiceFee $0.0000')
    wrapper.unmount()
  })

  it('refresh reloads every panel and the Key ranking with the same range', async () => {
    const wrapper = mount(DashboardView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          LoadingSpinner: true,
          Icon: true,
          DateRangePicker: true,
          Select: true,
          ModelDistributionChart: true,
          TokenUsageTrend: true,
          APIKeySpendingRanking: true,
          Line: true
        }
      }
    })
    await flushPromises()
    const ranking = wrapper.findComponent({ name: 'APIKeySpendingRanking' })
    const token = ranking.props('refreshToken') as number

    await wrapper.get('[data-testid="dashboard-refresh"]').trigger('click')
    await flushPromises()

    expect(getSnapshotV2).toHaveBeenCalledTimes(2)
    expect(getSnapshotV2.mock.calls[1][0]).toMatchObject({ include_stats: true })
    expect(getSnapshotV2.mock.calls[1][0]).toMatchObject({ start_date: getSnapshotV2.mock.calls[0][0].start_date })
    expect(getUserUsageTrend).toHaveBeenCalledTimes(2)
    expect(getUserSpendingRanking).toHaveBeenCalledTimes(2)
    expect(ranking.props('refreshToken')).toBe(token + 1)
    expect(wrapper.get('[data-testid="dashboard-refresh"]').attributes('disabled')).toBeUndefined()
  })

  it('keeps the refreshed top stats when the range changes before the refresh returns', async () => {
    const wrapper = mount(DashboardView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          LoadingSpinner: true,
          Icon: true,
          DateRangePicker: true,
          Select: true,
          ModelDistributionChart: true,
          TokenUsageTrend: true,
          APIKeySpendingRanking: true,
          Line: true
        }
      }
    })
    await flushPromises()

    let resolveRefresh!: (value: unknown) => void
    getSnapshotV2.mockImplementationOnce(() => new Promise(resolve => { resolveRefresh = resolve }))
    getSnapshotV2.mockResolvedValueOnce({ trend: [], models: [] }) // range change: charts only
    await wrapper.get('[data-testid="dashboard-refresh"]').trigger('click')
    wrapper.findComponent({ name: 'DateRangePicker' }).vm.$emit('change', { startDate: '2026-03-01', endDate: '2026-03-03', preset: null })
    await flushPromises()
    expect(getSnapshotV2.mock.calls[2][0]).toMatchObject({ include_stats: false, start_date: '2026-03-01' })

    resolveRefresh({ stats: { ...createDashboardStats(), total_api_keys: 4242 }, trend: [], models: [] })
    await flushPromises()
    expect(wrapper.text()).toContain('4242')
  })
})
