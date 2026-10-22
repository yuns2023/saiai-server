import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import UsageView from '../UsageView.vue'

const { query, getStatsByDateRange, list, showError, showWarning, showSuccess, showInfo } = vi.hoisted(() => ({
  query: vi.fn(),
  getStatsByDateRange: vi.fn(),
  list: vi.fn(),
  showError: vi.fn(),
  showWarning: vi.fn(),
  showSuccess: vi.fn(),
  showInfo: vi.fn(),
}))

const messages: Record<string, string> = {
  'usage.costDetails': 'Cost Breakdown',
  'admin.usage.inputCost': 'Input Cost',
  'admin.usage.outputCost': 'Output Cost',
  'admin.usage.cacheCreationCost': 'Cache Creation Cost',
  'admin.usage.cacheReadCost': 'Cache Read Cost',
  'admin.usage.cacheCreation5mCost': 'Cache Creation Cost (5m)',
  'admin.usage.cacheCreation1hCost': 'Cache Creation Cost (1h)',
  'admin.usage.cacheCreation5mPrice': 'Cache Creation Price (5m)',
  'admin.usage.cacheCreation1hPrice': 'Cache Creation Price (1h)',
  'admin.usage.cacheReadPrice': 'Cache Read Price',
  'admin.usage.cacheCreationPrice': 'Cache Creation Price',
  'usage.inputTokenPrice': 'Input price',
  'usage.outputTokenPrice': 'Output price',
  'usage.perMillionTokens': '/ 1M tokens',
  'usage.serviceTier': 'Service tier',
  'usage.serviceTierPriority': 'Fast',
  'usage.serviceTierFlex': 'Flex',
  'usage.serviceTierStandard': 'Standard',
  'usage.rate': 'Rate',
  'usage.groupModelMultiplier': 'Group model rate',
  'usage.paygUserDiscount': 'Payg discount',
  'usage.legacyPaygAccountDiscount': 'Legacy account discount',
  'usage.original': 'Original',
  'usage.billed': 'Billed',
  'usage.allApiKeys': 'All API Keys',
  'usage.apiKeyFilter': 'API Key',
  'usage.model': 'Model',
  'usage.reasoningEffort': 'Reasoning Effort',
  'usage.type': 'Type',
  'usage.tokens': 'Tokens',
  'usage.cost': 'Cost',
  'usage.firstToken': 'First Token',
  'usage.duration': 'Duration',
  'usage.time': 'Time',
  'usage.userAgent': 'User Agent',
}

vi.mock('@/api', () => ({
  usageAPI: {
    query,
    getStatsByDateRange,
  },
  keysAPI: {
    list,
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showWarning, showSuccess, showInfo }),
}))

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return {
    ...actual,
    useRoute: () => ({ query: {} }),
  }
})

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

const AppLayoutStub = { template: '<div><slot /></div>' }
const TablePageLayoutStub = {
  template: '<div><slot name="actions" /><slot name="filters" /><slot name="table" /><slot /></div>',
}

describe('user UsageView tooltip', () => {
  beforeEach(() => {
    query.mockReset()
    getStatsByDateRange.mockReset()
    list.mockReset()
    showError.mockReset()
    showWarning.mockReset()
    showSuccess.mockReset()
    showInfo.mockReset()

    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
      x: 0,
      y: 0,
      top: 20,
      left: 20,
      right: 120,
      bottom: 40,
      width: 100,
      height: 20,
      toJSON: () => ({}),
    } as DOMRect)

    ;(globalThis as any).ResizeObserver = class {
      observe() {}
      disconnect() {}
    }
  })

  it('shows fast service tier and unit prices in user tooltip', async () => {
    query.mockResolvedValue({
      items: [
        {
          request_id: 'req-user-1',
          actual_cost: 0.092883,
        billing_type: 1,
        subscription_id: null,
          total_cost: 0.092883,
          rate_multiplier: 1,
          model_rate_multiplier: 0.8,
          account_payg_discount_multiplier: 0.75,
          user_payg_discount_multiplier: 0.8,
          service_tier: 'priority',
          input_cost: 0.020285,
          output_cost: 0.00303,
          cache_creation_cost: 0,
          cache_creation_5m_cost: 0,
          cache_creation_1h_cost: 0,
          cache_read_cost: 0.069568,
          input_tokens: 4057,
          output_tokens: 101,
          cache_creation_tokens: 0,
          cache_read_tokens: 278272,
          cache_creation_5m_tokens: 0,
          cache_creation_1h_tokens: 0,
          image_count: 0,
          image_size: null,
          first_token_ms: null,
          duration_ms: 1,
          created_at: '2026-03-08T00:00:00Z',
        },
      ],
      total: 1,
      pages: 1,
    })
    getStatsByDateRange.mockResolvedValue({
      total_requests: 1,
      total_tokens: 100,
      total_cost: 0.1,
      avg_duration_ms: 1,
    })
    list.mockResolvedValue({ items: [] })

    const wrapper = mount(UsageView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          TablePageLayout: TablePageLayoutStub,
          Pagination: true,
          EmptyState: true,
          Select: true,
          DateRangePicker: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    await flushPromises()
    await nextTick()

    expect(wrapper.text()).toContain('Fast')

    const setupState = (wrapper.vm as any).$?.setupState
    setupState.tooltipData = {
      request_id: 'req-user-1',
      actual_cost: 0.092883,
        billing_type: 1,
        subscription_id: null,
      total_cost: 0.092883,
      rate_multiplier: 1,
      model_rate_multiplier: 0.8,
      account_payg_discount_multiplier: 0.75,
      user_payg_discount_multiplier: 0.8,
      service_tier: 'priority',
      input_cost: 0.020285,
      output_cost: 0.00303,
      cache_creation_cost: 0,
      cache_creation_5m_cost: 0,
      cache_creation_1h_cost: 0,
      cache_read_cost: 0.069568,
      input_tokens: 4057,
      output_tokens: 101,
      cache_read_tokens: 278272,
    }
    setupState.tooltipVisible = true
    await nextTick()

    const text = wrapper.text()
    expect(text).toContain('Service tier')
    expect(text).toContain('Fast')
    expect(text).toContain('Rate')
    expect(text).toContain('1.00x')
    expect(text).toContain('Group model rate')
    expect(text).toContain('Payg discount')
    expect(text).toContain('0.8000x')
    expect(text).toContain('0.7500x')
    expect(text).toContain('usage.subscriptionUsage')
    expect(text).toContain('$0.092883')
    expect(text).toContain('$5.0000 / 1M tokens')
    expect(text).toContain('$30.0000 / 1M tokens')
    expect(text).toContain('Cache Read Price')
    expect(text).toContain('$0.2500 / 1M tokens')
  })

  it('exports csv with input and output unit price columns', async () => {
    const exportedLogs = [
      {
        request_id: 'req-user-export',
        actual_cost: 0.092883,
        billing_type: 1,
        subscription_id: null,
        total_cost: 0.092883,
        rate_multiplier: 1,
        service_tier: 'priority',
        input_cost: 0.020285,
        output_cost: 0.00303,
        cache_creation_cost: 0.000001,
        cache_read_cost: 0.069568,
        input_tokens: 4057,
        output_tokens: 101,
        cache_creation_tokens: 4,
        cache_read_tokens: 278272,
        cache_creation_5m_tokens: 0,
        cache_creation_1h_tokens: 0,
        image_count: 0,
        image_size: null,
        first_token_ms: 12,
        duration_ms: 345,
        created_at: '2026-03-08T00:00:00Z',
        model: 'gpt-5.4',
        reasoning_effort: null,
        api_key: { name: 'demo-key' },
      },
    ]

    query.mockResolvedValue({
      items: exportedLogs,
      total: 1,
      pages: 1,
    })
    getStatsByDateRange.mockResolvedValue({
      total_requests: 1,
      total_tokens: 100,
      total_cost: 0.1,
      avg_duration_ms: 1,
    })
    list.mockResolvedValue({ items: [] })

    let exportedBlob: Blob | null = null
    const originalCreateObjectURL = window.URL.createObjectURL
    const originalRevokeObjectURL = window.URL.revokeObjectURL
    window.URL.createObjectURL = vi.fn((blob: Blob | MediaSource) => {
      exportedBlob = blob as Blob
      return 'blob:usage-export'
    }) as typeof window.URL.createObjectURL
    window.URL.revokeObjectURL = vi.fn(() => {}) as typeof window.URL.revokeObjectURL
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    const wrapper = mount(UsageView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          TablePageLayout: TablePageLayoutStub,
          Pagination: true,
          EmptyState: true,
          Select: true,
          DateRangePicker: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    await flushPromises()

    const setupState = (wrapper.vm as any).$?.setupState
    await setupState.exportToCSV()

    expect(exportedBlob).not.toBeNull()
    const csv = await new Promise<string>((resolve, reject) => {
      const reader = new FileReader()
      reader.onload = () => resolve(String(reader.result))
      reader.onerror = reject
      reader.readAsText(exportedBlob!)
    })
    const [header, row] = csv.split('\n').map(line => line.split(','))
    const sourceIndex = header.findIndex(cell => cell.includes('usage.billingSource'))
    expect(sourceIndex).toBeGreaterThan(-1)
    expect(row[sourceIndex]).toContain('usage.subscriptionUsage')
    expect(row[sourceIndex + 1]).toContain('0.09288300')
    expect(row.length).toBe(header.length)
    expect(clickSpy).toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalled()

    window.URL.createObjectURL = originalCreateObjectURL
    window.URL.revokeObjectURL = originalRevokeObjectURL
    clickSpy.mockRestore()
  })
})

describe('user UsageView density layout', () => {
  const TablePageLayoutPropsStub = {
    props: { dense: Boolean },
    template: '<div data-testid="layout" :data-dense="String(dense)"><slot name="actions" /><slot name="filters" /><slot name="table" /></div>',
  }
  const mountView = () =>
    mount(UsageView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          TablePageLayout: TablePageLayoutPropsStub,
          Pagination: true,
          EmptyState: true,
          Select: true,
          DateRangePicker: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

  beforeEach(() => {
    localStorage.clear()
    query.mockReset()
    getStatsByDateRange.mockReset()
    list.mockReset()
    ;(globalThis as any).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    query.mockResolvedValue({
      items: [
        {
          request_id: 'req-dense-1',
          actual_cost: 0.01,
          billing_type: 0,
          total_cost: 0.01,
          rate_multiplier: 1,
          input_cost: 0,
          output_cost: 0,
          cache_creation_cost: 0,
          cache_read_cost: 0,
          input_tokens: 1200,
          output_tokens: 80,
          cache_creation_tokens: 2048,
          cache_read_tokens: 55900,
          cache_creation_5m_tokens: 2048,
          cache_creation_1h_tokens: 0,
          image_count: 0,
          image_size: null,
          first_token_ms: 900,
          duration_ms: 4200,
          created_at: '2026-10-23T00:00:00Z',
          model: 'gpt-6-astra',
          user_agent: 'codex_cli_rs/0.140.0 (Ubuntu 24.4.0; x86_64) xterm-256color',
        },
      ],
      total: 1,
      pages: 1,
    })
    getStatsByDateRange.mockResolvedValue({ total_requests: 1, total_cost: 0.1, total_balance_cost: 0.1, total_subscription_cost: 0 })
    list.mockResolvedValue({ items: [] })
  })

  it('uses the dense table layout and renders token counts on a single line', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="layout"]').attributes('data-dense')).toBe('true')
    const tokenCell = wrapper.get('[data-testid="usage-token-cell"]')
    expect(tokenCell.classes()).toContain('md:whitespace-nowrap')
    const text = tokenCell.text()
    expect(text).toContain('1.2K')
    expect(text).toContain('80')
    expect(text).toContain('55.9K')
    expect(text).toContain('2.05K')
    const ua = wrapper.find('[title^="codex_cli_rs/0.140.0"]')
    expect(ua.classes()).toContain('md:truncate')
    expect(ua.classes()).toContain('break-all')
  })

  it('collapses the stat cards and remembers the choice', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-testid="usage-stats-cards"]').exists()).toBe(true)
    const toggle = wrapper.get('[data-testid="usage-stats-toggle"]')
    expect(toggle.attributes('aria-expanded')).toBe('true')
    await toggle.trigger('click')

    expect(wrapper.find('[data-testid="usage-stats-cards"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="usage-stats-toggle"]').attributes('aria-expanded')).toBe('false')
    expect(localStorage.getItem('user-usage-stats-collapsed')).toBe('true')
    wrapper.unmount()

    const remounted = mountView()
    await flushPromises()
    expect(remounted.find('[data-testid="usage-stats-cards"]').exists()).toBe(false)
    await remounted.get('[data-testid="usage-stats-toggle"]').trigger('click')
    expect(remounted.find('[data-testid="usage-stats-cards"]').exists()).toBe(true)
    expect(localStorage.getItem('user-usage-stats-collapsed')).toBe('false')
  })
})
