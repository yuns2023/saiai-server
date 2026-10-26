import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import UsageTable from '../UsageTable.vue'
import UsageCostCell from '@/components/common/UsageCostCell.vue'

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
  'usage.accountMultiplier': 'Account rate',
  'usage.groupModelMultiplier': 'Group model rate',
  'usage.paygUserDiscount': 'Payg discount',
  'usage.legacyPaygAccountDiscount': 'Legacy account discount',
  'usage.original': 'Original',
  'usage.userBilled': 'User billed',
  'usage.accountBilled': 'Account billed',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      locale: { value: 'en' },
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

const DataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.request_id">
        <slot name="cell-cost" :row="row" />
      </div>
    </div>
  `,
}

describe('admin UsageTable tooltip', () => {
  it.each([
    { input_tokens: 317880, cache_read_tokens: 0 },
    { input_tokens: 257960, cache_read_tokens: 45310 },
    { input_tokens: 1058, cache_read_tokens: 341248 },
  ])('separates large context from Priority pricing: %o', tokens => {
    const wrapper = mount(UsageTable, {
      props: { data: [{ request_id: 'TEST_ONLY_CONTEXT', model: 'gpt-6.1-sol', ...tokens,
        output_tokens: 405, cache_creation_tokens: 0, service_tier: 'priority', actual_cost: .639806,
        pricing_snapshot: { long_context: { applied: false, threshold: 272000 } } }], loading: false, columns: [] },
      global: { stubs: { DataTable: { props: ['data'], template: '<div><section data-testid="tokens"><slot name="cell-tokens" :row="data[0]" /></section><section data-testid="cost"><slot name="cell-cost" :row="data[0]" /></section></div>' }, Icon: true } }
    })
    expect(wrapper.get('[data-testid="tokens"] [data-testid="input-context-indicator"]').text()).toContain('>272K')
    expect(wrapper.get('[data-testid="cost"]').text()).toContain('Priority / Fast')
    expect(wrapper.find('[data-testid="long-context-indicator"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('$0.639806')
    wrapper.unmount()
  })

  it.each(['pending', 'passed', 'flagged', 'skipped', 'failed'] as const)('shows explicit %s moderation state', async (status) => {
    const wrapper = mount(UsageTable, {
      props: { data: [{ request_id: 'TEST_ONLY_STATUS', moderation_result: { status, scores: status === 'passed' ? { PII: .1 } : undefined, min_confidence: 90, updated_at: '2026-10-18T00:00:00Z' } }], loading: false, columns: [] },
      global: { stubs: { DataTable: { props: ['data'], template: '<div><slot name="cell-moderation" :row="data[0]" /></div>' } } }
    })
    expect(wrapper.text()).toContain('admin.groups.inputModeration.statuses.' + status)
    expect(wrapper.text()).not.toContain('admin.groups.inputModeration.noHit')
    if (status === 'passed') {
      expect(wrapper.get('[data-testid=highest-risk]').text()).toContain('10.0%')
      expect(wrapper.text()).not.toContain('90%')
      await wrapper.get('button[aria-haspopup=dialog]').trigger('click')
      expect(document.body.textContent).toContain('10.0%')
      expect(document.body.textContent).toContain('90%')
    }
    wrapper.unmount()
  })

  it('only shows hit labels using recorded category thresholds and opens full details', async () => {
    const wrapper = mount(UsageTable, {
      props: { data: [{ request_id: 'TEST_ONLY_EXPANDED', moderation_result: {
        status: 'flagged', scores: { PII: .95, violence: .4, sexual: .3 }, min_confidence: 90,
        decision_snapshot: { policy: { rules_version: 'jev-expanded-v1', thresholds: { PII: 90, violence: 40, sexual: 80 } } }
      } }], loading: false, columns: [] },
      global: { stubs: { DataTable: { props: ['data'], template: '<div><slot name="cell-moderation" :row="data[0]" /></div>' } } }
    })
    expect(wrapper.text()).toContain('Privacy abuse')
    expect(wrapper.text()).toContain('Violence')
    expect(wrapper.text()).not.toContain('Explicit sexual content')
    expect(wrapper.text()).not.toContain('95.0%')
    await wrapper.get('button[aria-haspopup=dialog]').trigger('click')
    expect(document.body.textContent).toContain('Explicit sexual content 30.0%')
    wrapper.getComponent({ name: 'BaseDialog' }).vm.$emit('close')
    await nextTick()
    expect(document.body.querySelector('[role=dialog]')).toBeNull()
    wrapper.unmount()
  })

  beforeEach(() => {
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
  })

  it('shows asynchronous hit scores without inventing scores for legacy records', async () => {
    const wrapper = mount(UsageTable, {
      props: { data: [{ request_id: 'TEST_ONLY_MODERATION' }], loading: false, columns: [] },
      global: { stubs: { DataTable: { props: ['data'], template: '<div><slot name="cell-moderation" :row="data[0]" /></div>' } } }
    })
    expect(wrapper.text()).toContain('admin.groups.inputModeration.noHit')
    await wrapper.setProps({ data: [{ request_id: 'TEST_ONLY_MODERATION', moderation_events: [
      { categories: ['PII'], scores: { PII: .95 }, action: 'warning', counted_as_strike: true },
      { categories: ['Jailbreak'], scores: null, action: 'audit', counted_as_strike: false }
    ] }] })
    expect(wrapper.text()).toContain('Privacy abuse')
    expect(wrapper.text()).not.toContain('95.0%')
    await wrapper.get('button[aria-haspopup=dialog]').trigger('click')
    expect(document.body.textContent).toContain('95.0%')
    expect(document.body.textContent).toContain('admin.groups.inputModeration.actions.warning')
    expect(wrapper.text()).toContain('Jailbreak')
    expect(wrapper.text()).not.toContain('100.0%')
    expect(wrapper.text()).not.toContain('admin.groups.inputModeration.noHit')
    wrapper.unmount()
  })

  it('distinguishes unavailable moderation from an empty result and hides stale labels', () => {
    const wrapper = mount(UsageTable, {
      props: { data: [{ request_id: 'TEST_ONLY_UNAVAILABLE', moderation_unavailable: true,
        moderation_events: [{ categories: ['PII'], scores: { PII: .95 }, action: 'warning' }] }], loading: false, columns: [] },
      global: { stubs: { DataTable: { props: ['data'], template: '<div><slot name="cell-moderation" :row="data[0]" /></div>' } } }
    })
    expect(wrapper.text()).toContain('admin.groups.inputModeration.unavailable')
    expect(wrapper.text()).not.toContain('admin.groups.inputModeration.noHit')
    expect(wrapper.text()).not.toContain('Privacy abuse')
    wrapper.unmount()
  })

  it('shows native Chat turns and unknown consumption instead of zero tokens', () => {
    const wrapper = mount(UsageTable, {
      props: { data: [{ request_id: 'TEST_ONLY_CHAT', model: 'gpt-6-pro', billing_unit: 'turn', chat_tier: 'pro',
        input_tokens: 0, output_tokens: 0, image_count: 0 }], loading: false, columns: [] },
      global: { stubs: { DataTable: { props: ['data'], template: '<div><slot name="cell-tokens" :row="data[0]" /><slot name="cell-reasoning_effort" :row="data[0]" /></div>' } } }
    })
    expect(wrapper.text()).toContain('usage.nativeChatTurn')
    expect(wrapper.text()).toContain('usage.nativeChatUsageUnknown')
    expect(wrapper.text()).toContain('usage.chatTiers.pro')
  })

  it.each([0, 2])('keeps native image generation inside its Chat turn with observed count %i', (count) => {
    const wrapper = mount(UsageTable, {
      props: { data: [{ request_id: 'TEST_ONLY_CHAT_IMAGE', model: 'gpt-5-6', billing_unit: 'turn',
        input_tokens: 0, output_tokens: 0, image_count: count, media_type: 'image' }], loading: false, columns: [] },
      global: { stubs: { DataTable: { props: ['data'], template: '<div><slot name="cell-tokens" :row="data[0]" /></div>' } } }
    })
    expect(wrapper.text()).toContain('usage.nativeChatTurn')
    expect(wrapper.text()).toContain(count > 0 ? 'usage.nativeChatImagesObserved' : 'usage.nativeChatImageCountUnknown')
    expect(wrapper.text()).toContain('usage.nativeChatUsageUnknown')
    expect(wrapper.text()).not.toContain('2K')
  })

  it('shows service tier and billing breakdown in cost tooltip', async () => {
    const row = {
      request_id: 'req-admin-1',
      actual_cost: 0.092883,
      billing_type: 0,
      total_cost: 0.092883,
      account_rate_multiplier: 1,
      rate_multiplier: 1,
      model_rate_multiplier: 0.8,
      account_payg_discount_multiplier: 0.75,
      user_payg_discount_multiplier: 0.8,
      service_tier: 'priority',
      input_cost: 0.020285,
      output_cost: 0.00303,
      cache_creation_cost: 0.0009,
      cache_creation_5m_cost: 0.0004,
      cache_creation_1h_cost: 0.0005,
      cache_read_cost: 0.069568,
      input_tokens: 4057,
      output_tokens: 101,
      cache_creation_tokens: 200,
      cache_creation_5m_tokens: 100,
      cache_creation_1h_tokens: 100,
      cache_read_tokens: 278272,
    }

    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    expect(wrapper.get('[data-testid="usage-cost-tier-icon"]').attributes('aria-label')).toContain('Fast')

    await wrapper.get('[data-testid="usage-cost-detail"]').trigger('mouseenter')
    await nextTick()

    const text = wrapper.text()
    expect(text).toContain('Service tier')
    expect(text).toContain('Fast')
    expect(text).toContain('Rate')
    expect(text).toContain('1.00x')
    expect(text).toContain('Account rate')
    expect(text).toContain('Group model rate')
    expect(text).toContain('Payg discount')
    expect(text).toContain('0.8000x')
    expect(text).toContain('0.7500x')
    expect(text).toContain('usage.balanceCharge')
    expect(text).toContain('Account billed')
    expect(text).toContain('$0.092883')
    expect(text).toContain('$5.0000 / 1M tokens')
    expect(text).toContain('$30.0000 / 1M tokens')
    expect(text).toContain('Cache Creation Price (5m)')
    expect(text).toContain('$4.0000 / 1M tokens')
    expect(text).toContain('Cache Creation Price (1h)')
    expect(text).toContain('$5.0000 / 1M tokens')
    expect(text).toContain('Cache Read Price')
    expect(text).toContain('$0.2500 / 1M tokens')
    expect(text).toContain('$0.069568')
  })

  it('shows unknown native image dimensions and stored charge components even without billing_unit', () => {
    const wrapper = mount(UsageTable, {
      props: { data: [{ request_id: 'TEST_ONLY_IMAGE', model: 'gpt-5-6',
        inbound_endpoint: '/chatgpt/backend-api/f/conversation', media_type: 'image', image_count: 1,
        image_size: null, actual_cost: .05, total_cost: .05, output_cost: .04,
        native_chat_turn_cost_usd: .01, native_chat_image_cost_usd: .04 }], loading: false, columns: [] },
      global: { stubs: { DataTable: { props: ['data'], template: '<div><slot name="cell-tokens" :row="data[0]" /><slot name="cell-cost" :row="data[0]" /></div>' } } }
    })
    expect(wrapper.text()).toContain('usage.imageSizeUnknown')
    expect(wrapper.text()).not.toContain('2K')
    expect(wrapper.get('[data-testid=native-chat-charges]').text()).toContain('0.010000')
    expect(wrapper.get('[data-testid=native-chat-charges]').text()).toContain('0.040000')
    expect(wrapper.text()).not.toContain('usage.serviceTierStandard')
  })
  it('shows a visible historical long-context advisory in the fee column without changing stored charges', async () => {
    const wrapper = mount(UsageTable, { props: { data: [{ request_id: 'TEST_ONLY_LONG', model: 'gpt-6-astra',
      input_tokens: 1058, cache_read_tokens: 341248, output_tokens: 424, actual_cost: .294182, total_cost: .735456,
      cache_creation_tokens: 0, service_tier: 'default' }], loading: false, columns: [] },
      global: { stubs: { DataTable: DataTableStub, Teleport: true, Icon: true } } })
    const badge = wrapper.get('[data-testid="long-context-indicator"]')
    expect(badge.text()).toContain('>272K')
    expect(badge.text()).toContain('usage.longContextInferred')
    expect(wrapper.text()).toContain('$0.294182')
    await wrapper.get('[data-testid="usage-cost-detail"]').trigger('mouseenter'); await nextTick()
    expect(wrapper.text()).toContain('usage.longContextWholeRequest')
    expect(wrapper.text()).toContain('usage.longContextInferredHint')
    wrapper.unmount()
  })

  it('lets only the row that owns the shared cost panel close it', async () => {
    const wrapper = mount(UsageTable, {
      props: { data: [{ request_id: 'req-A', actual_cost: 0.111111, total_cost: 0.111111, billing_type: 0, rate_multiplier: 1, input_cost: 0, output_cost: 0, cache_creation_cost: 0, cache_read_cost: 0, input_tokens: 1, output_tokens: 1, cache_creation_tokens: 0, cache_read_tokens: 0, image_count: 0, duration_ms: 1, created_at: '2026-10-23T00:00:00Z', model: 'gpt-6-astra' }, { request_id: 'req-B', actual_cost: 0.222222, total_cost: 0.222222, billing_type: 0, rate_multiplier: 1, input_cost: 0, output_cost: 0, cache_creation_cost: 0, cache_read_cost: 0, input_tokens: 1, output_tokens: 1, cache_creation_tokens: 0, cache_read_tokens: 0, image_count: 0, duration_ms: 1, created_at: '2026-10-23T00:00:00Z', model: 'gpt-6-astra' }], loading: false, columns: [] },
      global: { stubs: { DataTable: {
  props: ['data'],
  template: '<div><div v-for="row in data" :key="row.request_id" :data-row="row.request_id"><slot name="cell-cost" :row="row" /></div></div>',
}, EmptyState: true, Icon: true, Teleport: true } },
      attachTo: document.body,
    })

    const state = () => (wrapper.vm as any).$.setupState
    const detailFor = (id: string) => wrapper.get(`[data-row="${id}"] [data-testid="usage-cost-detail"]`)

    // pin row A
    await detailFor('req-A').trigger('click')
    expect(state().tooltipVisible).toBe(true)
    expect(state().tooltipData.request_id).toBe('req-A')

    // a stray close from a row that does not own the panel is ignored
    wrapper.findAllComponents(UsageCostCell)[1].vm.$emit('hide-detail')
    await nextTick()
    expect(state().tooltipVisible).toBe(true)
    expect(state().tooltipData.request_id).toBe('req-A')

    // hovering B takes the shared panel over; leaving B closes it
    await detailFor('req-B').trigger('mouseenter')
    expect(state().tooltipData.request_id).toBe('req-B')
    await detailFor('req-B').trigger('mouseleave')
    expect(state().tooltipVisible).toBe(false)

    // A gave up ownership, so hovering it again re-opens the panel for A
    await detailFor('req-A').trigger('mouseenter')
    expect(state().tooltipVisible).toBe(true)
    expect(state().tooltipData.request_id).toBe('req-A')
    await detailFor('req-A').trigger('mouseleave')
    expect(state().tooltipVisible).toBe(false)
    wrapper.unmount()
  })

  it.each([
    ['refusal', 'refusal'],
    [null, 'admin.usage.stopReasonNotRecorded'],
  ] as const)('shows upstream stop_reason %s in the token tooltip', async (stopReason, expected) => {
    const wrapper = mount(UsageTable, {
      props: { data: [{ request_id: 'TEST_ONLY_STOP_REASON', model: 'claude-sonnet-5', input_tokens: 3, output_tokens: 0,
        cache_creation_tokens: 200000, cache_read_tokens: 0, image_count: 0, stop_reason: stopReason }], loading: false, columns: [] },
      global: { stubs: { DataTable: { props: ['data'], template: '<div><slot name="cell-tokens" :row="data[0]" /></div>' } } },
      attachTo: document.body,
    })
    await wrapper.get('.group.relative').trigger('mouseenter')
    await nextTick()
    const row = document.body.querySelector('[data-testid=usage-stop-reason]')
    expect(row?.textContent).toContain('admin.usage.stopReason')
    expect(row?.textContent).toContain(expected)
    wrapper.unmount()
  })
})
