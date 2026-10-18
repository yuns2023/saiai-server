import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import UsageView from '../UsageView.vue'

const { list, getStats, getSnapshotV2, getModelStats, getById, routeQuery } = vi.hoisted(() => {
  vi.stubGlobal('localStorage', {
    getItem: vi.fn(() => null),
    setItem: vi.fn(),
    removeItem: vi.fn(),
  })

  return {
    routeQuery: {} as Record<string, string>,
    list: vi.fn(),
    getStats: vi.fn(),
    getSnapshotV2: vi.fn(),
    getModelStats: vi.fn(),
    getById: vi.fn(),
  }
})

const messages: Record<string, string> = {
  'admin.dashboard.timeRange': 'Time Range',
  'admin.dashboard.day': 'Day',
  'admin.dashboard.hour': 'Hour',
  'admin.usage.failedToLoadUser': 'Failed to load user',
}

vi.mock('@/api/admin', () => ({
  adminAPI: {
    usage: {
      list,
      getStats,
    },
    dashboard: {
      getSnapshotV2,
      getModelStats,
    },
    users: {
      getById,
    },
  },
}))

vi.mock('@/api/admin/usage', () => ({
  adminUsageAPI: {
    list: vi.fn(),
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showWarning: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn(),
  }),
}))

vi.mock('@/utils/format', () => ({
  formatReasoningEffort: (value: string | null | undefined) => value ?? '-',
  getEffectiveReasoningEffort: (log: { reasoning_effort?: string | null; reasoning_effort_effective?: string | null }) =>
    log.reasoning_effort_effective ?? log.reasoning_effort,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

vi.mock('vue-router', () => ({
  useRoute: () => ({
    query: routeQuery
  })
}))

const AppLayoutStub = { template: '<div><slot /></div>' }
const UsageFiltersStub = {
  props: ['modelValue'],
  emits: ['update:modelValue', 'change'],
  template: '<div><button data-test="filter-user" @click="$emit(\'update:modelValue\', { ...modelValue, user_id: 42, model: \'claude-opus\' }); $emit(\'change\')">filter user</button><slot name="after-reset" /></div>'
}
const ModelDistributionChartStub = {
  props: ['metric', 'breakdownFilters'],
  emits: ['update:metric'],
  template: `
    <div data-test="model-chart">
      <span class="metric">{{ metric }}</span>
      <span class="breakdown-user">{{ breakdownFilters?.user_id }}</span>
      <button class="switch-metric" @click="$emit('update:metric', 'actual_cost')">switch</button>
    </div>
  `,
}
const GroupDistributionChartStub = {
  props: ['metric', 'breakdownFilters'],
  emits: ['update:metric'],
  template: `
    <div data-test="group-chart">
      <span class="metric">{{ metric }}</span>
      <span class="breakdown-user">{{ breakdownFilters?.user_id }}</span>
      <button class="switch-metric" @click="$emit('update:metric', 'actual_cost')">switch</button>
    </div>
  `,
}
const EndpointDistributionChartStub = {
  props: ['breakdownFilters'],
  template: '<div data-test="endpoint-chart" />',
}

describe('admin UsageView distribution metric toggles', () => {
  beforeEach(() => {
    Object.keys(routeQuery).forEach(key => delete routeQuery[key])
    vi.useFakeTimers()
    list.mockReset()
    getStats.mockReset()
    getSnapshotV2.mockReset()
    getModelStats.mockReset()
    getById.mockReset()

    list.mockResolvedValue({
      items: [],
      total: 0,
      pages: 0,
    })
    getStats.mockResolvedValue({
      total_requests: 0,
      total_input_tokens: 0,
      total_output_tokens: 0,
      total_cache_creation_tokens: 0,
      total_cache_read_tokens: 0,
      total_cache_tokens: 0,
      total_tokens: 0,
      total_cost: 0,
      total_actual_cost: 0,
      average_duration_ms: 0,
    })
    getSnapshotV2.mockResolvedValue({
      trend: [],
      models: [],
      groups: [],
    })
    getModelStats.mockResolvedValue({
      models: [],
      start_date: '',
      end_date: '',
    })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('keeps the exact user alert window and filter when refreshing', async () => {
    Object.assign(routeQuery, { user_id: '42', moderation_flagged: 'true', start_time: '2026-09-18T12:00:00Z', end_time: '2026-10-18T12:00:00Z' })
    const wrapper = mount(UsageView, { global: { stubs: {
      AppLayout: AppLayoutStub, UsageStatsCards: true, UsageFilters: true, UsageTable: true,
      InputRiskAlerts: true, UsageExportProgress: true, UsageCleanupDialog: true,
      UserBalanceHistoryModal: true, Pagination: true, Select: true, DateRangePicker: true, Icon: true,
      ModelDistributionChart: true, GroupDistributionChart: true, EndpointDistributionChart: true, TokenUsageTrend: true
    } } })
    vi.advanceTimersByTime(120)
    await flushPromises()
    const expected = { user_id: 42, moderation_flagged: true, start_time: routeQuery.start_time, end_time: routeQuery.end_time }
    expect(list.mock.lastCall?.[0]).toMatchObject(expected)
    expect(getStats.mock.lastCall?.[0]).toMatchObject(expected)
    expect(getSnapshotV2).not.toHaveBeenCalled()
    expect(getModelStats).not.toHaveBeenCalled()
    wrapper.findComponent({ name: 'UsageFilters' }).vm.$emit('refresh')
    await flushPromises()
    expect(list.mock.lastCall?.[0]).toMatchObject(expected)
    wrapper.unmount()
  })

  it('keeps model and group metric toggles independent without refetching chart data', async () => {
    const wrapper = mount(UsageView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          UsageStatsCards: true,
          UsageFilters: UsageFiltersStub,
          UsageTable: true,
          UsageExportProgress: true,
          UsageCleanupDialog: true,
          UserBalanceHistoryModal: true,
          Pagination: true,
          Select: true,
          DateTimeRangePicker: true,
          Icon: true,
          TokenUsageTrend: true,
          ModelDistributionChart: ModelDistributionChartStub,
          GroupDistributionChart: GroupDistributionChartStub,
        },
      },
    })

    vi.advanceTimersByTime(120)
    await flushPromises()

    expect(getSnapshotV2).toHaveBeenCalledTimes(1)
    expect(getModelStats).toHaveBeenCalledTimes(1)
    const snapshotParams = getSnapshotV2.mock.calls[0][0]
    expect(snapshotParams.start_date).toBeUndefined()
    expect(snapshotParams.end_date).toBeUndefined()
    expect(snapshotParams.granularity).toBe('hour')
    expect(new Date(snapshotParams.end_time).getTime() - new Date(snapshotParams.start_time).getTime()).toBe(24 * 60 * 60 * 1000)
    expect(getStats).toHaveBeenCalledWith(expect.objectContaining({
      start_time: snapshotParams.start_time,
      end_time: snapshotParams.end_time
    }))

    const modelChart = wrapper.find('[data-test="model-chart"]')
    const groupChart = wrapper.find('[data-test="group-chart"]')

    expect(modelChart.find('.metric').text()).toBe('tokens')
    expect(groupChart.find('.metric').text()).toBe('tokens')

    await modelChart.find('.switch-metric').trigger('click')
    await flushPromises()

    expect(modelChart.find('.metric').text()).toBe('actual_cost')
    expect(groupChart.find('.metric').text()).toBe('tokens')
    expect(getSnapshotV2).toHaveBeenCalledTimes(1)

    await groupChart.find('.switch-metric').trigger('click')
    await flushPromises()

    expect(modelChart.find('.metric').text()).toBe('actual_cost')
    expect(groupChart.find('.metric').text()).toBe('actual_cost')
    expect(getSnapshotV2).toHaveBeenCalledTimes(1)
  })

  it('passes the selected user and model to every distribution breakdown', async () => {
    const wrapper = mount(UsageView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          UsageStatsCards: true,
          UsageFilters: UsageFiltersStub,
          UsageTable: true,
          UsageExportProgress: true,
          UsageCleanupDialog: true,
          UserBalanceHistoryModal: true,
          Pagination: true,
          Select: true,
          DateTimeRangePicker: true,
          Icon: true,
          TokenUsageTrend: true,
          ModelDistributionChart: ModelDistributionChartStub,
          GroupDistributionChart: GroupDistributionChartStub,
          EndpointDistributionChart: EndpointDistributionChartStub,
        },
      },
    })

    await wrapper.find('[data-test="filter-user"]').trigger('click')
    await flushPromises()
    expect(getModelStats).toHaveBeenLastCalledWith(expect.objectContaining({ user_id: 42 }))
    expect(wrapper.find('[data-test="model-chart"] .breakdown-user').text()).toBe('42')
    expect(wrapper.find('[data-test="group-chart"] .breakdown-user').text()).toBe('42')
    expect(wrapper.findComponent(EndpointDistributionChartStub).props('breakdownFilters')).toEqual(expect.objectContaining({
      user_id: 42,
      model_filter: 'claude-opus',
    }))
    for (const chart of [ModelDistributionChartStub, GroupDistributionChartStub]) {
      expect(wrapper.findComponent(chart).props('breakdownFilters')).toEqual(expect.objectContaining({
        user_id: 42,
        model_filter: 'claude-opus',
      }))
    }
  })
  it('sends a custom minute range consistently to logs, totals, models and trends', async () => {
    const wrapper = mount(UsageView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          UsageStatsCards: true,
          UsageFilters: UsageFiltersStub,
          UsageTable: true,
          UsageExportProgress: true,
          UsageCleanupDialog: true,
          UserBalanceHistoryModal: true,
          Pagination: true,
          Select: true,
          DateTimeRangePicker: true,
          Icon: true,
          TokenUsageTrend: true,
          ModelDistributionChart: ModelDistributionChartStub,
          GroupDistributionChart: GroupDistributionChartStub,
        },
      },
    })
    vi.advanceTimersByTime(120)
    await flushPromises()
    const range = { startTime: '2026-10-17T10:30:00Z', endTime: '2026-10-17T11:45:00Z', preset: null }
    wrapper.findComponent({ name: 'DateTimeRangePicker' }).vm.$emit('change', range)
    await flushPromises()
    const expected = expect.objectContaining({ start_time: range.startTime, end_time: range.endTime, start_date: undefined, end_date: undefined })
    expect(list.mock.calls.at(-1)![0]).toEqual(expected)
    expect(getStats).toHaveBeenLastCalledWith(expected)
    expect(getModelStats).toHaveBeenLastCalledWith(expected)
    expect(getSnapshotV2).toHaveBeenLastCalledWith(expected)
    expect(getSnapshotV2.mock.calls.at(-1)![0].granularity).toBe('hour')
    wrapper.findComponent({ name: 'DateTimeRangePicker' }).vm.$emit('change', {
      startTime: new Date(2026, 9, 17).toISOString(), endTime: new Date(2026, 9, 18).toISOString(), preset: 'yesterday'
    })
    await flushPromises()
    expect(list.mock.calls.at(-1)![0]).toEqual(expect.objectContaining({
      start_date: '2026-10-17', end_date: '2026-10-17', start_time: undefined, end_time: undefined
    }))
    wrapper.unmount()
  })

})
