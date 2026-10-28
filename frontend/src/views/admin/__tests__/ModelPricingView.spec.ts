import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ModelPricingView from '../ModelPricingView.vue'

const { list, preview, update, history, driftNotices, acknowledgeDrift, showError } = vi.hoisted(() => ({ list: vi.fn(), preview: vi.fn(), update: vi.fn(), history: vi.fn(), driftNotices: vi.fn(), acknowledgeDrift: vi.fn(), showError: vi.fn() }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<div><slot /></div>' } }))
vi.mock('@/api/client', () => ({ apiClient: {} }))
vi.mock('@/api/admin', () => ({ adminAPI: { modelPricing: { list, preview, update, history, driftNotices, acknowledgeDrift } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const row = {
  model: 'model-a', alias: 'model-base', version: 'v1', resolved_model: 'model-base', source: 'remote', mode: 'chat', available: true,
  override: {}, reference: { input: 0.00001 }, effective: { input: 0.00001 },
  tiers: { default: { reference: { input: 10, output: 50, cache_read: 1, cache_write_5m: 12.5, cache_write_1h: 20 }, effective: { input: 10, output: 50, cache_read: 1, cache_write_5m: 12.5, cache_write_1h: 20 } } }
}
const cost = { charged_amount: 0.1, subscription: true, cost: { total_cost: 0.1, actual_cost: 0.1, pricing_snapshot: { reference_total_cost: 0.2, resolved_model: 'model-base' } } }
function mountView() {
  return mount(ModelPricingView, { global: { stubs: {
    AppLayout: { template: '<div><slot /></div>' },
    BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' }
  } } })
}
function button(wrapper: ReturnType<typeof mountView>, label: string) { return wrapper.findAll('button').find(b => b.text() === label)! }
beforeEach(() => {
  vi.clearAllMocks()
  list.mockResolvedValue({ items: [structuredClone(row)], total: 1, metadata: { source: 'remote', updated_at: '2026-10-10T00:00:00Z', source_hash: 'fixture' } })
  preview.mockResolvedValue(cost)
  update.mockResolvedValue(row)
  history.mockResolvedValue([])
  driftNotices.mockResolvedValue({ notices: [], unacknowledged: 0 })
})
describe('Model pricing editor', () => {
  it('preserves explicit free prices and previews subscription factors without saving simulated rates', async () => {
    const wrapper = mountView(); await flushPromises()
    expect(wrapper.text()).toContain('$10')
    await button(wrapper, 'common.edit').trigger('click')
    expect(wrapper.find('#factor-group_rate').exists()).toBe(false)
    expect(wrapper.find('#factor-user_discount').exists()).toBe(false)
    await wrapper.get('#factor-model_rate').setValue('2')
    await wrapper.get('#price-cache_read').setValue('0')
    await button(wrapper, 'admin.modelPricing.calculate').trigger('click'); await flushPromises()
    expect(preview).toHaveBeenCalledWith(expect.objectContaining({ override: { cache_read: 0 }, subscription: true, group_rate: 1, model_rate: 2, user_discount: 1 }))
    expect(button(wrapper, 'common.save').attributes('disabled')).toBeUndefined()
    await button(wrapper, 'common.save').trigger('click'); await flushPromises()
    expect(update).toHaveBeenCalledWith({ model: 'model-a', alias: 'model-base', override: { cache_read: 0 }, expected_version: 'v1' })
    wrapper.unmount()
  })
  it('invalidates a preview when prices change and ignores an outstanding stale preview', async () => {
    let resolve!: (value: typeof cost) => void
    preview.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const wrapper = mountView(); await flushPromises(); await button(wrapper, 'common.edit').trigger('click')
    await button(wrapper, 'admin.modelPricing.calculate').trigger('click')
    await wrapper.get('#price-cache_read').setValue('2')
    resolve(cost); await flushPromises()
    expect(button(wrapper, 'common.save').attributes('disabled')).toBeDefined()
    expect(update).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('previews a free model in either billing mode with neutral group and user factors', async () => {
    const wrapper = mountView(); await flushPromises(); await button(wrapper, 'common.edit').trigger('click')
    await wrapper.get('#factor-model_rate').setValue('0')
    await button(wrapper, 'admin.modelPricing.calculate').trigger('click'); await flushPromises()
    expect(preview).toHaveBeenLastCalledWith(expect.objectContaining({ subscription: true, group_rate: 1, model_rate: 0, user_discount: 1 }))
    await wrapper.get('select[aria-label="admin.modelPricing.billingMode"]').setValue('false')
    expect(button(wrapper, 'common.save').attributes('disabled')).toBeDefined()
    await button(wrapper, 'admin.modelPricing.calculate').trigger('click'); await flushPromises()
    expect(preview).toHaveBeenLastCalledWith(expect.objectContaining({ subscription: false, group_rate: 1, model_rate: 0, user_discount: 1 }))
    wrapper.unmount()
  })
  it('reloads after a conflicting price edit without overwriting current prices', async () => {
    update.mockRejectedValue({ response: { status: 409, data: { message: 'Prices changed' } } })
    const wrapper = mountView(); await flushPromises(); await button(wrapper, 'common.edit').trigger('click')
    await button(wrapper, 'admin.modelPricing.calculate').trigger('click'); await flushPromises()
    await button(wrapper, 'common.save').trigger('click'); await flushPromises()
    expect(showError).toHaveBeenCalledWith('Prices changed'); expect(list).toHaveBeenCalledTimes(3)
    expect(wrapper.find('#price-cache_read').exists()).toBe(true)
    expect(button(wrapper, 'common.save').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })
  it('switches context prices and displays preview evidence independently of service tier', async () => {
    const longRow = structuredClone(row) as typeof row & { long_context_tiers: unknown }
    Object.assign(longRow.effective, { long_context_threshold: 272000 })
    longRow.long_context_tiers = { default: { reference: { input: 20, output: 75, cache_read: 2 }, effective: { input: 20, output: 75, cache_read: 2 } } }
    list.mockResolvedValue({ items: [longRow], total: 1 })
    preview.mockResolvedValue({ ...cost, cost: { ...cost.cost, pricing_snapshot: { ...cost.cost.pricing_snapshot,
      effective: { long_context_threshold: 272000 }, long_context: { applied: true, mode: 'whole_request', threshold: 272000,
        total_input_tokens: 341248, input_multiplier: 2, output_multiplier: 1.5, cache_read_multiplier: 2, cache_write_multiplier: 2 } } } })
    const wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid=model-long-context-rule]').exists()).toBe(true)
    await wrapper.get('select[aria-label="admin.modelPricing.contextMode"]').setValue('long')
    expect(wrapper.text()).toContain('$75')
    await wrapper.get('select[aria-label="admin.modelPricing.tier"]').setValue('priority')
    expect(wrapper.text()).toContain('admin.modelPricing.noLongContextTier')
    expect(wrapper.text()).not.toContain('$75')
    await wrapper.get('select[aria-label="admin.modelPricing.tier"]').setValue('default')
    await button(wrapper, 'common.edit').trigger('click')
    expect(wrapper.get('[data-testid=long-context-price-table]').text()).toContain('$75')
    await button(wrapper, 'admin.modelPricing.calculate').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid=long-context-indicator]').text()).toContain('>272K')
    wrapper.unmount()
  })

})
const notice = (id: string, extra: Record<string, unknown> = {}) => ({
  id, fingerprint: `fp-${id}`, reporter: 'east', model: 'gpt-6-sol', old_status: 'priced', new_status: 'priced', old_resolved_model: 'gpt-6-sol', new_resolved_model: 'gpt-6-sol', changes: [],
  old_feed_hash: 'a'.repeat(64), new_feed_hash: 'b'.repeat(64), detected_at: '2026-10-28T09:10:00Z', acknowledged_at: null, acknowledged_by: 0, ...extra
})
const acked = (item: ReturnType<typeof notice>) => ({ ...item, acknowledged_at: '2026-10-28T10:00:00Z', acknowledged_by: 1 })
const priceNotice = notice('n1', { changes: [
  { key: 'default.cache_read', old: 0.2, new: 0.1 }, { key: 'priority.input', old: null, new: 5 }, { key: 'long.flex.output', old: 30, new: 45 },
  { key: 'long.threshold', old: 200000, new: 272000 }, { key: 'image.per_image', old: 0.04, new: 0.05 }, { key: 'image.input', old: 5, new: null }, { key: 'mystery.key', old: 1, new: 2 }
] })
const statusNotice = notice('n2', { model: 'fable-5', old_status: 'priced', new_status: 'unpriced', old_resolved_model: 'fable-5', new_resolved_model: '' })
const oldNotice = acked(notice('n0', { model: 'retired-model' }))
const driftList = () => ({ unacknowledged: 2, notices: [structuredClone(priceNotice), structuredClone(statusNotice), structuredClone(oldNotice)] })
const banner = (wrapper: ReturnType<typeof mountView>) => wrapper.find('[data-testid=pricing-drift-banner]')
describe('Pricing drift notices', () => {
  it('hides the banner when every notice is acknowledged', async () => {
    driftNotices.mockResolvedValue({ unacknowledged: 0, notices: [structuredClone(oldNotice)] })
    const wrapper = mountView(); await flushPromises()
    expect(driftNotices).toHaveBeenCalledTimes(1)
    expect(banner(wrapper).exists()).toBe(false)
    expect(wrapper.text()).not.toContain('retired-model')
    wrapper.unmount()
  })
  it('lists unacknowledged notices with labelled price, threshold, image and status changes', async () => {
    driftNotices.mockResolvedValue(driftList())
    const wrapper = mountView(); await flushPromises()
    expect(banner(wrapper).attributes('role')).toBe('alert')
    expect(banner(wrapper).text()).toContain('admin.modelPricing.drift.title')
    expect(banner(wrapper).text()).not.toContain('retired-model')
    const prices = wrapper.get('[data-testid=pricing-drift-notice-n1]').findAll('li').map(item => item.text())
    expect(prices).toEqual([
      'Standard · admin.modelPricing.fields.cache_read: $0.2 → $0.1 admin.modelPricing.drift.units.tokens',
      'Fast / Priority · admin.modelPricing.fields.input: — → $5 admin.modelPricing.drift.units.tokens',
      'usage.longContext · Flex · admin.modelPricing.fields.output: $30 → $45 admin.modelPricing.drift.units.tokens',
      `admin.modelPricing.drift.keys.longThreshold: ${(200000).toLocaleString()} → ${(272000).toLocaleString()} admin.modelPricing.drift.units.thresholdTokens`,
      'admin.modelPricing.drift.keys.perImage: $0.04 → $0.05 admin.modelPricing.drift.units.image',
      'admin.modelPricing.drift.keys.imageInput: $5 → — admin.modelPricing.drift.units.imageTokens',
      'mystery.key: 1 → 2'
    ])
    const priced = wrapper.get('[data-testid=pricing-drift-notice-n1]').text()
    expect(priced).not.toContain('×')
    expect(priced).not.toContain('admin.modelPricing.drift.status')
    expect(priced).not.toContain('admin.modelPricing.resolved')
    const status = wrapper.get('[data-testid=pricing-drift-notice-n2]')
    expect(status.text()).toContain('admin.modelPricing.drift.status.unpriced')
    expect(status.text()).toContain('admin.modelPricing.resolved: fable-5 → —')
    expect(status.findAll('li')).toHaveLength(0)
    expect(status.text()).not.toContain('×')
    expect(wrapper.text()).toContain('$10')
    wrapper.unmount()
  })
  it('acknowledges a single notice and replaces the list from the response', async () => {
    driftNotices.mockResolvedValue(driftList())
    let resolve!: (value: unknown) => void
    acknowledgeDrift.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid=pricing-drift-notice-n1]').findAll('button').find(b => b.text() === 'admin.modelPricing.drift.acknowledge')!.trigger('click')
    expect(acknowledgeDrift).toHaveBeenCalledWith(['n1'])
    expect(banner(wrapper).findAll('button').filter(b => b.text().startsWith('admin.modelPricing.drift.acknowledge')).every(b => b.attributes('disabled') !== undefined)).toBe(true)
    resolve({ unacknowledged: 1, notices: [structuredClone(statusNotice), acked(priceNotice), structuredClone(oldNotice)] }); await flushPromises()
    expect(wrapper.find('[data-testid=pricing-drift-notice-n1]').exists()).toBe(false)
    expect(wrapper.find('[data-testid=pricing-drift-notice-n2]').exists()).toBe(true)
    expect(button(wrapper, 'admin.modelPricing.drift.acknowledgeAll').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })
  it('acknowledges exactly the displayed unacknowledged notices', async () => {
    driftNotices.mockResolvedValue(driftList())
    acknowledgeDrift.mockResolvedValue({ unacknowledged: 0, notices: [acked(priceNotice), acked(statusNotice), structuredClone(oldNotice)] })
    const wrapper = mountView(); await flushPromises()
    await button(wrapper, 'admin.modelPricing.drift.acknowledgeAll').trigger('click'); await flushPromises()
    expect(acknowledgeDrift).toHaveBeenCalledTimes(1)
    expect(acknowledgeDrift).toHaveBeenCalledWith(['n1', 'n2'])
    expect(banner(wrapper).exists()).toBe(false)
    wrapper.unmount()
  })
  it('reports acknowledge failures and keeps the notices', async () => {
    driftNotices.mockResolvedValue(driftList())
    acknowledgeDrift.mockRejectedValue({ response: { status: 500, data: { message: 'Ack failed' } } })
    const wrapper = mountView(); await flushPromises()
    await button(wrapper, 'admin.modelPricing.drift.acknowledgeAll').trigger('click'); await flushPromises()
    expect(showError).toHaveBeenCalledWith('Ack failed')
    expect(wrapper.find('[data-testid=pricing-drift-notice-n1]').exists()).toBe(true)
    expect(button(wrapper, 'admin.modelPricing.drift.acknowledgeAll').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })
  it('opens the notice model in the price list from the first page', async () => {
    driftNotices.mockResolvedValue(driftList())
    list.mockResolvedValue({ items: [structuredClone(row)], total: 120, metadata: { source: 'remote', updated_at: '2026-10-10T00:00:00Z', source_hash: 'fixture' } })
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('input[type=checkbox]').setValue(true); await flushPromises()
    expect(list).toHaveBeenLastCalledWith({ search: '', configured: true, page: 1 })
    await button(wrapper, 'admin.modelPricing.next').trigger('click'); await flushPromises()
    expect(list).toHaveBeenLastCalledWith({ search: '', configured: true, page: 2 })
    await wrapper.get('[data-testid=pricing-drift-notice-n2] [data-testid=pricing-drift-model]').trigger('click'); await flushPromises()
    expect(list).toHaveBeenLastCalledWith({ search: 'fable-5', configured: false, page: 1 })
    expect((wrapper.get('input[aria-label="admin.modelPricing.search"]').element as HTMLInputElement).value).toBe('fable-5')
    wrapper.unmount()
  })
  it('keeps the price table usable when drift notices fail to load', async () => {
    driftNotices.mockRejectedValueOnce({ response: { status: 500, data: { message: 'boom' } } })
    const wrapper = mountView(); await flushPromises()
    expect(wrapper.text()).toContain('$10')
    expect(wrapper.text()).toContain('model-a')
    expect(banner(wrapper).exists()).toBe(false)
    expect(wrapper.get('[data-testid=pricing-drift-error]').text()).toContain('admin.modelPricing.drift.loadFailed')
    expect(showError).not.toHaveBeenCalled()
    driftNotices.mockResolvedValueOnce(driftList())
    await wrapper.get('[data-testid=pricing-drift-error] button').trigger('click'); await flushPromises()
    expect(banner(wrapper).exists()).toBe(true)
    expect(wrapper.find('[data-testid=pricing-drift-error]').exists()).toBe(false)
    wrapper.unmount()
  })
})
