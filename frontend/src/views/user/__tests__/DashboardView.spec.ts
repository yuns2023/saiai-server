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
  getUsageGroups: vi.fn(),
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
    getByDateRange: mocks.getByDateRange,
    getUsageGroups: mocks.getUsageGroups
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
    mocks.getUsageGroups.mockResolvedValue([{ id: 11, name: 'self-hosted' }])
  })

  it('loads the generic Key ranking without applying a Key filter', async () => {
    mountDashboard()
    await flushPromises()

    expect(mocks.getDashboardStats).toHaveBeenCalledWith(undefined, undefined)
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

    expect(mocks.getDashboardStats).toHaveBeenLastCalledWith(11, undefined)
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
      expect.any(String),
      undefined
    )
  })

  it('applies the selected actual group to every widget and the details link', async () => {
    const wrapper = mountDashboard()
    await flushPromises()
    expect(mocks.getUsageGroups).toHaveBeenCalledWith(expect.any(String), expect.any(String), expect.any(String))

    await wrapper.get('[data-testid="dashboard-group-filter"]').trigger('click')
    await flushPromises()

    expect(mocks.getDashboardStats).toHaveBeenLastCalledWith(undefined, 11)
    for (const request of [mocks.getDashboardTrend, mocks.getDashboardModels, mocks.getDashboardAPIKeyBreakdown]) {
      expect(request).toHaveBeenLastCalledWith(expect.objectContaining({ group_id: 11 }))
    }
    expect(mocks.getDashboardAPIKeyBreakdown).toHaveBeenLastCalledWith(expect.objectContaining({ page: 1 }))
    expect(mocks.getByDateRange).toHaveBeenLastCalledWith(
      expect.any(String),
      expect.any(String),
      undefined,
      expect.any(String),
      11
    )
    const recent = wrapper.findComponent({ name: 'UserDashboardRecentUsage' })
    expect(recent.props('viewAllTo')).toMatchObject({ path: '/usage', query: { group_id: '11' } })
  })
  it('keeps the latest group selection when an older response arrives last', async () => {
    const wrapper = mountDashboard()
    await flushPromises()

    let resolveFirst: (value: typeof dashboardStats) => void = () => {}
    mocks.getDashboardStats
      .mockReturnValueOnce(new Promise((resolve) => { resolveFirst = resolve }))
      .mockResolvedValueOnce({ ...dashboardStats, today_requests: 31 })
    const groupFilter = wrapper.findAllComponents(SelectStub)[1]
    groupFilter.vm.$emit('update:modelValue', 12)
    groupFilter.vm.$emit('update:modelValue', 31)
    await flushPromises()
    resolveFirst({ ...dashboardStats, today_requests: 12 })
    await flushPromises()

    expect(mocks.getDashboardStats).toHaveBeenLastCalledWith(undefined, 31)
    expect(wrapper.findComponent({ name: 'UserDashboardStats' }).props('stats')).toMatchObject({ today_requests: 31 })
  })

  it('exports every Key ranking page with the filters frozen at export start', async () => {
    const wrapper = mountDashboard()
    await flushPromises()
    vi.stubGlobal('URL', { ...URL, createObjectURL: vi.fn(() => 'blob:x'), revokeObjectURL: vi.fn() })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    const page = (n: number) => ({
      items: [], total: 101, page: n, page_size: 100, pages: 2,
      summary: { requests: 0, total_tokens: 0, total_cost: 0, actual_cost: 0 }
    })
    let resolveFirstPage: (value: ReturnType<typeof page>) => void = () => {}
    mocks.getDashboardAPIKeyBreakdown.mockReset()
    mocks.getDashboardAPIKeyBreakdown
      .mockReturnValueOnce(new Promise((resolve) => { resolveFirstPage = resolve }))
      .mockResolvedValue(page(2))
    wrapper.findComponent({ name: 'UserDashboardAPIKeyBreakdown' }).vm.$emit('export')
    await flushPromises()
    wrapper.findAllComponents(SelectStub)[1].vm.$emit('update:modelValue', 31)
    resolveFirstPage(page(1))
    await flushPromises()

    const exportCalls = mocks.getDashboardAPIKeyBreakdown.mock.calls
      .map(([params]) => params)
      .filter((params) => params.page_size === 100)
    expect(exportCalls).toHaveLength(2)
    expect(exportCalls.map((params) => params.group_id)).toEqual([undefined, undefined])
    expect(click).toHaveBeenCalledTimes(1)
    click.mockRestore()
    vi.unstubAllGlobals()
  })
})
