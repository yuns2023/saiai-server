import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ModelPricingView from '../ModelPricingView.vue'

const { list, preview, update, history, showError } = vi.hoisted(() => ({ list: vi.fn(), preview: vi.fn(), update: vi.fn(), history: vi.fn(), showError: vi.fn() }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<div><slot /></div>' } }))
vi.mock('@/api/client', () => ({ apiClient: {} }))
vi.mock('@/api/admin', () => ({ adminAPI: { modelPricing: { list, preview, update, history } } }))
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
})
describe('Model pricing editor', () => {
  it('preserves explicit free prices, inherits blank fields and keeps overall subscription factors unchanged', async () => {
    const wrapper = mountView(); await flushPromises()
    expect(wrapper.text()).toContain('$10')
    await button(wrapper, 'common.edit').trigger('click')
    expect(wrapper.get('#factor-user_discount').attributes('disabled')).toBeDefined()
    await wrapper.get('#price-cache_read').setValue('0')
    await button(wrapper, 'admin.modelPricing.calculate').trigger('click'); await flushPromises()
    expect(preview).toHaveBeenCalledWith(expect.objectContaining({ override: { cache_read: 0 }, subscription: true, user_discount: 1 }))
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
