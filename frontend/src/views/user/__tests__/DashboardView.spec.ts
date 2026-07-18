import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import DashboardView from '../DashboardView.vue'

const mocks = vi.hoisted(() => ({
  refreshUser: vi.fn(),
  listKeys: vi.fn(),
  getDashboardStats: vi.fn(),
  getDashboardTrend: vi.fn(),
  getDashboardModels: vi.fn(),
  getDashboardAPIKeyBreakdown: vi.fn(),
  getByDateRange: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: { balance: 100 },
    isSimpleMode: false,
    refreshUser: mocks.refreshUser
  })
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: mocks.showSuccess,
    showError: mocks.showError
  })
}))

vi.mock('@/api/keys', () => ({
  keysAPI: {
    list: mocks.listKeys
  }
}))

vi.mock('@/api/usage', () => ({
  usageAPI: {
    getDashboardStats: mocks.getDashboardStats,
    getDashboardTrend: mocks.getDashboardTrend,
    getDashboardModels: mocks.getDashboardModels,
    getDashboardAPIKeyBreakdown: mocks.getDashboardAPIKeyBreakdown,
    getByDateRange: mocks.getByDateRange
  }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const dashboardStats = {
  total_api_keys: 1,
  active_api_keys: 1,
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
  rpm: 0,
  tpm: 0
}

const SelectStub = defineComponent({
  props: ['modelValue'],
  emits: ['update:modelValue'],
  template:
    '<button data-test="key-filter" @click="$emit(\'update:modelValue\', 11)">{{ modelValue }}</button>'
})

const mountDashboard = () =>
  mount(DashboardView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        LoadingSpinner: true,
        Select: SelectStub,
        UserDashboardStats: true,
        UserDashboardCharts: true,
        UserDashboardRecentUsage: true,
        UserDashboardQuickActions: true,
        UserDashboardAPIKeyBreakdown: true
      }
    }
  })

describe('user DashboardView API Key analytics', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.refreshUser.mockResolvedValue(undefined)
    mocks.listKeys.mockResolvedValue({
      items: [{ id: 11, name: 'team-a' }],
      total: 1,
      page: 1,
      page_size: 100,
      pages: 1
    })
    mocks.getDashboardStats.mockResolvedValue(dashboardStats)
    mocks.getDashboardTrend.mockResolvedValue({ trend: [] })
    mocks.getDashboardModels.mockResolvedValue({ models: [] })
    mocks.getDashboardAPIKeyBreakdown.mockResolvedValue({
      items: [],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
      summary: { requests: 0, total_tokens: 0, total_cost: 0, actual_cost: 0 }
    })
    mocks.getByDateRange.mockResolvedValue({ items: [] })
  })

  it('loads the generic Key ranking without applying a Key filter', async () => {
    mountDashboard()
    await flushPromises()

    expect(mocks.getDashboardStats).toHaveBeenCalledWith(undefined)
    expect(mocks.getDashboardAPIKeyBreakdown).toHaveBeenCalledWith(
      expect.objectContaining({
        page: 1,
        page_size: 20,
        sort: 'actual_cost_desc'
      })
    )
    expect(mocks.getDashboardTrend).toHaveBeenCalledWith(
      expect.objectContaining({ api_key_id: undefined })
    )
  })

  it('applies the selected Key to cards, charts, and recent usage', async () => {
    const wrapper = mountDashboard()
    await flushPromises()

    await wrapper.get('[data-test="key-filter"]').trigger('click')
    await flushPromises()

    expect(mocks.getDashboardStats).toHaveBeenLastCalledWith(11)
    expect(mocks.getDashboardTrend).toHaveBeenLastCalledWith(
      expect.objectContaining({ api_key_id: 11 })
    )
    expect(mocks.getDashboardModels).toHaveBeenLastCalledWith(
      expect.objectContaining({ api_key_id: 11 })
    )
    expect(mocks.getByDateRange).toHaveBeenLastCalledWith(
      expect.any(String),
      expect.any(String),
      11,
      expect.any(String)
    )
  })
})
