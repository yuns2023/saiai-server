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
})
