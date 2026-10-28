import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import UsageView from '../UsageView.vue'
import { presetTimeRange } from '@/utils/dateTimeRange'

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

  it.each(['today', 'last24Hours', '7days', '14days', '30days', 'thisMonth', 'yesterday', 'lastMonth', null])(
    'refreshes %s with the correct window across midnight and month boundaries',
    async (preset) => {
      vi.setSystemTime(new Date(2026, 9, 31, 23, 50))
      const wrapper = mount(UsageView, { global: { stubs: {
        AppLayout: AppLayoutStub, UsageStatsCards: true, UsageFilters: true, UsageTable: true,
        InputRiskAlerts: true, UsageExportProgress: true, UsageCleanupDialog: true,
        UserBalanceHistoryModal: true, Pagination: true, Select: true, DateTimeRangePicker: true,
        Icon: true, ModelDistributionChart: true, GroupDistributionChart: true,
        EndpointDistributionChart: true, TokenUsageTrend: true, CacheUsageTrend: true,
      } } })
      await flushPromises()
      const range = preset === null
        ? { startTime: new Date(2026, 9, 31, 10).toISOString(), endTime: new Date(2026, 9, 31, 11).toISOString(), preset }
        : presetTimeRange(preset)
      wrapper.findComponent({ name: 'DateTimeRangePicker' }).vm.$emit('change', range)
      await flushPromises()
      const before = { ...list.mock.lastCall![0] }
      const moving = preset !== null && !['yesterday', 'lastMonth'].includes(preset)
      vi.setSystemTime(new Date(2026, 10, 1, 0, 10))
      wrapper.findComponent({ name: 'UsageFilters' }).vm.$emit('refresh')
      await flushPromises()
      const updated = moving ? presetTimeRange(preset!) : null
      // "today" is a whole calendar day and is sent as dates.
      const calendarDay = preset === 'today'
      const expected = !updated
        ? { start_time: before.start_time, end_time: before.end_time, start_date: before.start_date, end_date: before.end_date }
        : calendarDay
          ? { start_date: '2026-11-01', end_date: '2026-11-01', start_time: undefined, end_time: undefined }
          : { start_time: updated.startTime, end_time: updated.endTime, start_date: undefined, end_date: undefined }
      for (const request of [list, getStats, getModelStats, getSnapshotV2]) {
        expect(request.mock.lastCall![0]).toMatchObject(expected)
      }
      if (updated) {
        expect(wrapper.findComponent({ name: 'DateTimeRangePicker' }).props()).toMatchObject(updated)
        if (!calendarDay) expect(new Date(expected.end_time!).getTime()).toBeGreaterThan(new Date(range.endTime).getTime())
      }
      wrapper.unmount()
    },
  )

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

  it('applies a Key ranking deep link to logs, totals, and distributions', async () => {
    Object.assign(routeQuery, {
      user_id: '42', api_key_id: '11',
      start_time: '2026-03-01T10:00:00Z', end_time: '2026-03-02T10:00:00Z'
    })
    const wrapper = mount(UsageView, {
      global: { stubs: {
        AppLayout: AppLayoutStub, UsageStatsCards: true, UsageFilters: UsageFiltersStub,
        UsageTable: true, InputRiskAlerts: true, UsageExportProgress: true, UsageCleanupDialog: true,
        UserBalanceHistoryModal: true, Pagination: true, Select: true, DateTimeRangePicker: true,
        Icon: true, TokenUsageTrend: true, ModelDistributionChart: ModelDistributionChartStub,
        GroupDistributionChart: GroupDistributionChartStub, EndpointDistributionChart: EndpointDistributionChartStub,
      } },
    })
    vi.advanceTimersByTime(120)
    await flushPromises()
    const expected = {
      user_id: 42, api_key_id: 11,
      start_time: routeQuery.start_time, end_time: routeQuery.end_time,
    }
    expect(list).toHaveBeenCalledWith(expect.objectContaining(expected), expect.anything())
    expect(getStats).toHaveBeenCalledWith(expect.objectContaining(expected))
    expect(getModelStats).toHaveBeenCalledWith(expect.objectContaining(expected))
    expect(getSnapshotV2).toHaveBeenCalledWith(expect.objectContaining(expected))
    expect(wrapper.findComponent(ModelDistributionChartStub).props('breakdownFilters')).toEqual(expect.objectContaining({
      user_id: 42, api_key_id: 11,
    }))
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
    // Default window: today as a calendar day.
    const snapshotParams = getSnapshotV2.mock.calls[0][0]
    const now = new Date()
    const today = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`
    expect(snapshotParams).toMatchObject({ start_date: today, end_date: today, granularity: 'hour' })
    expect(snapshotParams.start_time).toBeUndefined()
    expect(snapshotParams.end_time).toBeUndefined()
    expect(getStats).toHaveBeenCalledWith(expect.objectContaining({ start_date: today, end_date: today }))

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
