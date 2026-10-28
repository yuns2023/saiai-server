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
        hosted_today_metered_cost: 0.25
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
    wrapper.unmount()
  })
})
