import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type {
  UsageAnalyticsItem,
  UsageAnalyticsResponse,
  UsageAnalyticsSummary
} from '@/api/usage'
import Select from '@/components/common/Select.vue'
import UserUsageAnalytics from '../UserUsageAnalytics.vue'

const mocks = vi.hoisted(() => ({
  getUsageAnalytics: vi.fn(),
  showSuccess: vi.fn(),
  showWarning: vi.fn(),
  showError: vi.fn(),
  copyToClipboard: vi.fn()
}))

vi.mock('@/api/usage', () => ({
  usageAPI: { getUsageAnalytics: mocks.getUsageAnalytics },
  usageAnalyticsEndpointURL: () => 'https://example.test/api/v1/usage/analytics'
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: mocks.showSuccess,
    showWarning: mocks.showWarning,
    showError: mocks.showError
  })
}))

vi.mock('@/composables/useClipboard', async () => {
  const { ref } = await vi.importActual<typeof import('vue')>('vue')
  return { useClipboard: () => ({ copied: ref(false), copyToClipboard: mocks.copyToClipboard }) }
})

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key })
}))

const summary = (overrides: Partial<UsageAnalyticsSummary> = {}): UsageAnalyticsSummary => ({
  requests: 10,
  input_tokens: 100,
  output_tokens: 50,
  cache_creation_tokens: 25,
  cache_read_tokens: 25,
  total_tokens: 200,
  cost: 2,
  actual_cost: 1,
  usage_cost: 1,
  token_mix: { input: 0.5, output: 0.25, cache_creation: 0.125, cache_read: 0.125 },
  ...overrides
})

const item = (overrides: Partial<UsageAnalyticsItem> = {}): UsageAnalyticsItem => ({
  requests: 10,
  input_tokens: 100,
  output_tokens: 50,
  cache_creation_tokens: 25,
  cache_read_tokens: 25,
  total_tokens: 200,
  cost: 2,
  actual_cost: 1,
  usage_cost: 1,
  share: { requests: 1, tokens: 1, cost: 1, actual_cost: 1, usage_cost: 1 },
  token_mix: { input: 0.5, output: 0.25, cache_creation: 0.125, cache_read: 0.125 },
  ...overrides
})

const response = (overrides: Partial<UsageAnalyticsResponse> = {}): UsageAnalyticsResponse => ({
  group_by: 'model',
  sort: 'actual_cost_desc',
  start_date: '2026-10-01',
  end_date: '2026-10-29',
  timezone: 'UTC',
  start: '2026-10-01T00:00:00Z',
  end_exclusive: '2026-10-30T00:00:00Z',
  total: 1,
  page: 1,
  page_size: 50,
  pages: 1,
  summary: summary(),
  items: [item({ model: 'gpt-x' })],
  ...overrides
})

const wrappers: ReturnType<typeof mount>[] = []
afterEach(() => wrappers.splice(0).forEach((wrapper) => wrapper.unmount()))

const mountAnalytics = (props: Record<string, unknown> = {}) => {
  const wrapper = mount(UserUsageAnalytics, {
    props: {
      startDate: '2026-10-01',
      endDate: '2026-10-29',
      apiKeyId: 11,
      groupId: null,
      timezone: 'UTC',
      models: ['gpt-x'],
      ...props
    },
    global: {
      stubs: {
        Select: true,
        Pagination: true,
        LoadingSpinner: true,
        EmptyState: { props: ['title'], template: '<div data-test="empty">{{ title }}</div>' },
        HelpTooltip: { template: '<span><slot name="trigger" /></span>' },
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
        RouterLink: { props: ['to'], template: '<a><slot /></a>' }
      }
    }
  })
  wrappers.push(wrapper)
  return wrapper
}

const lastParams = () => mocks.getUsageAnalytics.mock.calls.at(-1)?.[0]

describe('UserUsageAnalytics', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.getUsageAnalytics.mockResolvedValue(response())
  })

  it('loads with dashboard filters and sends group_by when switching tabs', async () => {
    const wrapper = mountAnalytics()
    await flushPromises()
    expect(lastParams()).toEqual(
      expect.objectContaining({
        start_date: '2026-10-01',
        end_date: '2026-10-29',
        timezone: 'UTC',
        api_key_id: 11,
        group_id: undefined,
        group_by: 'model',
        sort: 'actual_cost_desc',
        page: 1,
        page_size: 50
      })
    )

    mocks.getUsageAnalytics.mockResolvedValue(
      response({ group_by: 'api_key_model', items: [item({ api_key_id: 11, api_key_name: 'team-a', api_key_deleted: true, model: 'gpt-x' })] })
    )
    await wrapper.get('[data-testid="analytics-tab-api_key_model"]').trigger('click')
    await flushPromises()
    expect(lastParams()).toEqual(expect.objectContaining({ group_by: 'api_key_model', page: 1 }))
    const rowText = wrapper.get('[data-testid="analytics-rows"]').text()
    expect(rowText).toContain('team-a')
    expect(rowText).toContain('gpt-x')
    expect(wrapper.find('[data-testid="analytics-key-deleted"]').exists()).toBe(true)

    await wrapper.get('[data-testid="analytics-tab-reasoning_effort"]').trigger('click')
    await flushPromises()
    expect(lastParams()).toEqual(expect.objectContaining({ group_by: 'reasoning_effort' }))
  })

  it('falls back to reference-usage shares for hosted-only results', async () => {
    mocks.getUsageAnalytics.mockResolvedValue(
      response({
        summary: summary({ actual_cost: 0, usage_cost: 8 }),
        items: [
          item({
            model: 'hosted-model',
            actual_cost: 0,
            usage_cost: 2,
            share: { requests: 0.5, tokens: 0.5, cost: 0.5, actual_cost: null, usage_cost: 0.25 }
          })
        ]
      })
    )
    const wrapper = mountAnalytics()
    await flushPromises()
    expect(wrapper.get('[data-testid="analytics-share"]').text()).toBe('25.00%')
    expect(wrapper.text()).toContain('dashboard.analytics.shareUsageCost')
    expect(wrapper.find('[data-testid="analytics-usage-cost"]').exists()).toBe(true)
  })

  it('renders null shares as a dash', async () => {
    mocks.getUsageAnalytics.mockResolvedValue(
      response({
        summary: summary({ total_tokens: 0, cost: 0, actual_cost: 0, usage_cost: 0 }),
        items: [
          item({
            model: 'free',
            share: { requests: 1, tokens: null, cost: null, actual_cost: null, usage_cost: null }
          })
        ]
      })
    )
    const wrapper = mountAnalytics()
    await flushPromises()
    expect(wrapper.get('[data-testid="analytics-share"]').text()).toBe('—')
    expect(wrapper.find('[data-testid="analytics-usage-cost"]').exists()).toBe(false)

    await wrapper.get('[data-testid="analytics-metric-requests"]').trigger('click')
    expect(wrapper.get('[data-testid="analytics-share"]').text()).toBe('100.00%')
  })

  it('keeps server order in the table and lists "Not recorded" last only in the chart', async () => {
    mocks.getUsageAnalytics.mockResolvedValue(
      response({
        group_by: 'reasoning_effort',
        total: 2,
        items: [
          item({ reasoning_effort_missing: true }),
          item({ reasoning_effort: 'none' })
        ]
      })
    )
    const wrapper = mountAnalytics()
    await flushPromises()
    const labels = wrapper.findAll('[data-testid="analytics-effort-label"]').map((node) => node.text())
    expect(labels).toEqual(['dashboard.analytics.effortMissing', 'None'])
    const chartLabels = wrapper
      .findAll('[data-testid="analytics-effort-chart-label"]')
      .map((node) => node.text())
    expect(chartLabels).toEqual(['None', 'dashboard.analytics.effortMissing'])
    expect(wrapper.find('[data-testid="analytics-effort-distribution"]').exists()).toBe(true)
  })

  it('ignores stale responses after filters change', async () => {
    let resolveFirst: (value: UsageAnalyticsResponse) => void = () => {}
    mocks.getUsageAnalytics
      .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve }))
      .mockResolvedValueOnce(response({ items: [item({ model: 'fresh-model' })] }))
    const wrapper = mountAnalytics()
    await wrapper.setProps({ startDate: '2026-10-15' })
    await flushPromises()

    const firstSignal = mocks.getUsageAnalytics.mock.calls[0][1].signal as AbortSignal
    expect(firstSignal.aborted).toBe(true)
    expect(lastParams()).toEqual(expect.objectContaining({ start_date: '2026-10-15', page: 1 }))

    resolveFirst(response({ items: [item({ model: 'stale-model' })] }))
    await flushPromises()
    const text = wrapper.get('[data-testid="analytics-rows"]').text()
    expect(text).toContain('fresh-model')
    expect(text).not.toContain('stale-model')
  })

  it('shows the API error message', async () => {
    mocks.getUsageAnalytics.mockRejectedValue({ status: 400, message: 'date range exceeds 90 days' })
    const wrapper = mountAnalytics()
    await flushPromises()
    expect(wrapper.get('[data-testid="analytics-error"]').text()).toContain('date range exceeds 90 days')
  })

  it('shows a curl example with only a placeholder token', async () => {
    const wrapper = mountAnalytics()
    await flushPromises()
    await wrapper.get('[data-testid="analytics-api-example"]').trigger('click')
    const curl = wrapper.get('[data-testid="analytics-curl"]').text()
    expect(curl).toContain('https://example.test/api/v1/usage/analytics?start_date=2026-10-01')
    expect(curl).toContain('api_key_id=11')
    expect(curl).toContain('Authorization: Bearer <YOUR_LOGIN_TOKEN>')
  })

  it('exports every page and de-duplicates rows repeated across pages', async () => {
    const createObjectURL = vi.fn((_blob: Blob) => 'blob:analytics')
    const revokeObjectURL = vi.fn()
    Object.assign(window.URL, { createObjectURL, revokeObjectURL })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    const wrapper = mountAnalytics()
    await flushPromises()
    mocks.getUsageAnalytics.mockClear()
    mocks.getUsageAnalytics
      .mockResolvedValueOnce(response({ pages: 2, items: [item({ model: 'model-a' }), item({ model: 'model-b' })] }))
      .mockResolvedValueOnce(response({ pages: 2, items: [item({ model: 'model-b' }), item({ model: 'model-c' })] }))
    await wrapper.get('[data-testid="analytics-export"]').trigger('click')
    await flushPromises()

    expect(mocks.getUsageAnalytics).toHaveBeenCalledTimes(2)
    expect(mocks.getUsageAnalytics.mock.calls.map((call) => [call[0].page, call[0].page_size])).toEqual([
      [1, 1000],
      [2, 1000]
    ])
    const blob = createObjectURL.mock.calls[0][0]
    const bytes = await new Promise<Uint8Array>((resolve) => {
      const reader = new FileReader()
      reader.onload = () => resolve(new Uint8Array(reader.result as ArrayBuffer))
      reader.readAsArrayBuffer(blob)
    })
    expect([...bytes.slice(0, 3)]).toEqual([0xef, 0xbb, 0xbf])
    const csv = new TextDecoder().decode(bytes.slice(3))
    const models = csv.split('\n').slice(1).map((line) => line.split(',')[0])
    expect(models).toEqual(['"model-a"', '"model-b"', '"model-c"'])
    expect(mocks.showWarning).toHaveBeenCalledWith('dashboard.analytics.exportMultiPage')
    expect(mocks.getUsageAnalytics.mock.calls[0][0]).toEqual(
      expect.objectContaining({ group_by: 'model', api_key_id: 11 })
    )
    expect(createObjectURL).toHaveBeenCalledTimes(1)
    expect(click).toHaveBeenCalledTimes(1)
    expect(mocks.showSuccess).not.toHaveBeenCalled()
    click.mockRestore()
  })

  it('treats an export as multi-page by pages fetched, even if the total shrinks', async () => {
    const createObjectURL = vi.fn((_blob: Blob) => 'blob:analytics')
    Object.assign(window.URL, { createObjectURL, revokeObjectURL: vi.fn() })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const wrapper = mountAnalytics()
    await flushPromises()
    mocks.getUsageAnalytics.mockClear()
    mocks.getUsageAnalytics
      .mockResolvedValueOnce(response({ pages: 2, items: [item({ model: 'model-a' })] }))
      .mockResolvedValueOnce(response({ pages: 1, items: [item({ model: 'model-a' })] }))
    await wrapper.get('[data-testid="analytics-export"]').trigger('click')
    await flushPromises()

    expect(mocks.getUsageAnalytics).toHaveBeenCalledTimes(2)
    const csv = await new Promise<string>((resolve) => {
      const reader = new FileReader()
      reader.onload = () => resolve(reader.result as string)
      reader.readAsText(createObjectURL.mock.calls[0][0] as Blob)
    })
    expect(csv.trim().split('\n')).toHaveLength(2) // header + one de-duplicated row
    expect(mocks.showWarning).toHaveBeenCalledWith('dashboard.analytics.exportMultiPage')
    expect(mocks.showSuccess).not.toHaveBeenCalled()
    click.mockRestore()
  })

  it('keeps export filters frozen when the view changes mid-export', async () => {
    Object.assign(window.URL, { createObjectURL: vi.fn(() => 'blob:analytics'), revokeObjectURL: vi.fn() })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const wrapper = mountAnalytics()
    await flushPromises()

    let resolveFirstPage: (value: UsageAnalyticsResponse) => void = () => {}
    mocks.getUsageAnalytics.mockClear()
    mocks.getUsageAnalytics
      .mockImplementationOnce(() => new Promise((resolve) => { resolveFirstPage = resolve }))
      .mockResolvedValue(response({ pages: 2, items: [item({ model: 'b' })] }))
    await wrapper.get('[data-testid="analytics-export"]').trigger('click')
    await wrapper.setProps({ startDate: '2026-10-20', apiKeyId: 12 })
    await wrapper.get('[data-testid="analytics-tab-reasoning_effort"]').trigger('click')
    resolveFirstPage(response({ pages: 2, items: [item({ model: 'a' })] }))
    await flushPromises()

    const exportCalls = mocks.getUsageAnalytics.mock.calls
      .map((call) => call[0])
      .filter((params) => params.page_size === 1000)
    expect(exportCalls).toHaveLength(2)
    for (const params of exportCalls) {
      expect(params).toEqual(
        expect.objectContaining({ group_by: 'model', start_date: '2026-10-01', api_key_id: 11 })
      )
    }
    click.mockRestore()
  })

  it('stops exporting at 10 pages and warns', async () => {
    Object.assign(window.URL, { createObjectURL: vi.fn(() => 'blob:analytics'), revokeObjectURL: vi.fn() })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const wrapper = mountAnalytics()
    await flushPromises()
    mocks.getUsageAnalytics.mockClear()
    mocks.getUsageAnalytics.mockResolvedValue(response({ pages: 80, total: 80000 }))
    await wrapper.get('[data-testid="analytics-export"]').trigger('click')
    await flushPromises()

    expect(mocks.getUsageAnalytics).toHaveBeenCalledTimes(10)
    expect(mocks.getUsageAnalytics.mock.calls.at(-1)?.[0].page).toBe(10)
    expect(mocks.showWarning).toHaveBeenCalledWith('dashboard.analytics.exportTruncated')
    expect(mocks.showSuccess).not.toHaveBeenCalled()
    click.mockRestore()
  })

  const selectByLabel = (wrapper: ReturnType<typeof mountAnalytics>, label: string) => {
    const select = wrapper.findAllComponents(Select).find((node) => node.props('ariaLabel') === label)
    if (!select) throw new Error(`no Select labelled ${label}`)
    return select
  }

  it('sends billing_type=0 instead of dropping it as falsy', async () => {
    const wrapper = mountAnalytics()
    await flushPromises()
    selectByLabel(wrapper, 'dashboard.analytics.billingType').vm.$emit('update:modelValue', 0)
    await flushPromises()
    expect(lastParams()).toEqual(expect.objectContaining({ billing_type: 0, page: 1 }))
  })

  it('distinguishes "Not recorded" from the literal "none" effort', async () => {
    const wrapper = mountAnalytics()
    await flushPromises()
    const effort = selectByLabel(wrapper, 'dashboard.analytics.effortFilter')

    effort.vm.$emit('update:modelValue', '__missing__')
    await flushPromises()
    expect(lastParams()).toEqual(expect.objectContaining({ reasoning_effort_missing: true, reasoning_effort: undefined }))

    effort.vm.$emit('update:modelValue', 'none')
    await flushPromises()
    expect(lastParams()).toEqual(expect.objectContaining({ reasoning_effort: 'none', reasoning_effort_missing: undefined }))
  })

  it('sends exactly one request per dimension or page change and none for the metric toggle', async () => {
    mocks.getUsageAnalytics.mockResolvedValue(response({ total: 120, pages: 3 }))
    const wrapper = mountAnalytics()
    await flushPromises()
    expect(mocks.getUsageAnalytics).toHaveBeenCalledTimes(1)

    await wrapper.get('[data-testid="analytics-metric-tokens"]').trigger('click')
    await wrapper.get('[data-testid="analytics-metric-requests"]').trigger('click')
    await flushPromises()
    expect(mocks.getUsageAnalytics).toHaveBeenCalledTimes(1)

    await wrapper.get('[data-testid="analytics-tab-api_key"]').trigger('click')
    await flushPromises()
    expect(mocks.getUsageAnalytics).toHaveBeenCalledTimes(2)

    wrapper.findComponent({ name: 'Pagination' }).vm.$emit('update:page', 2)
    await flushPromises()
    expect(mocks.getUsageAnalytics).toHaveBeenCalledTimes(3)
    expect(lastParams()).toEqual(expect.objectContaining({ group_by: 'api_key', page: 2 }))

    // Never past the backend page limit.
    wrapper.findComponent({ name: 'Pagination' }).vm.$emit('update:page', 20000)
    await flushPromises()
    expect(lastParams()).toEqual(expect.objectContaining({ page: 10000 }))
  })

  it('aborts the in-flight request on unmount', async () => {
    mocks.getUsageAnalytics.mockImplementation(() => new Promise(() => {}))
    const wrapper = mountAnalytics()
    await flushPromises()
    const signal = mocks.getUsageAnalytics.mock.calls[0][1].signal as AbortSignal
    expect(signal.aborted).toBe(false)
    wrapper.unmount()
    wrappers.splice(wrappers.indexOf(wrapper), 1)
    expect(signal.aborted).toBe(true)
  })

  it('labels Key record links honestly with the Key (and row model) as accessible name', async () => {
    mocks.getUsageAnalytics.mockResolvedValue(
      response({ group_by: 'api_key_model', items: [item({ api_key_id: 11, api_key_name: 'team-a', model: 'gpt-x' })] })
    )
    const wrapper = mountAnalytics()
    await flushPromises()
    const link = wrapper.get('[data-testid="analytics-key-records"]')
    expect(link.text()).toBe('dashboard.analytics.viewKeyRecords')
    expect(link.attributes('aria-label')).toBe('dashboard.analytics.viewKeyRecordsForModelRow')
  })
})
