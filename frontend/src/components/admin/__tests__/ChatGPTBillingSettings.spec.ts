import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ChatGPTBillingSettings from '../ChatGPTBillingSettings.vue'

const mocks = vi.hoisted(() => ({ get: vi.fn(), update: vi.fn(), success: vi.fn(), error: vi.fn() }))
vi.mock('@/api/admin/settings', () => ({ getChatGPTBillingSettings: mocks.get, updateChatGPTBillingSettings: mocks.update }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showSuccess: mocks.success, showError: mocks.error }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('ChatGPT billing settings', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    mocks.get.mockResolvedValue({ success_turn_price_usd: 0.02 })
  })

  it('loads the saved price and saves only the new Chat price', async () => {
    const wrapper = mount(ChatGPTBillingSettings)
    await flushPromises()
    expect(wrapper.findAll('input')).toHaveLength(6)
    expect((wrapper.get('[data-tier=instant]').element as HTMLInputElement).value).toBe('0.02')
    await wrapper.get('[data-tier=instant]').setValue('0.03')
    mocks.update.mockResolvedValue({ success_turn_price_usd: 0.03 })
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(mocks.update).toHaveBeenCalledWith({ success_turn_price_usd: 0.02, tier_prices_usd: { instant: 0.03, medium: 0.02, high: 0.02, extreme: 0.02, pro: 0.02 } })
    expect(mocks.success).toHaveBeenCalledOnce()
    expect(wrapper.get('button').attributes('type')).toBe('button')
  })

  it('rejects an empty or negative price and allows explicit zero', async () => {
    const wrapper = mount(ChatGPTBillingSettings)
    await flushPromises()
    for (const price of ['', '-0.02']) {
      await wrapper.get('[data-tier=instant]').setValue(price)
      expect(wrapper.get('button').attributes('disabled')).toBeDefined()
      await wrapper.get('button').trigger('click')
    }
    expect(mocks.update).not.toHaveBeenCalled()
    await wrapper.get('[data-tier=instant]').setValue('0')
    mocks.update.mockResolvedValue({ success_turn_price_usd: 0 })
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(mocks.update).toHaveBeenCalledWith({ success_turn_price_usd: 0.02, tier_prices_usd: { instant: 0, medium: 0.02, high: 0.02, extreme: 0.02, pro: 0.02 } })
  })

  it('requires a successful load before offering a save, and can retry', async () => {
    mocks.get.mockRejectedValueOnce(new Error('read failed'))
    const wrapper = mount(ChatGPTBillingSettings)
    await flushPromises()
    expect(wrapper.find('input').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').exists()).toBe(true)
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(mocks.get).toHaveBeenCalledTimes(2)
    expect(wrapper.find('input').exists()).toBe(true)
    expect(mocks.update).not.toHaveBeenCalled()
  })

  it('shows a failed save and retains the input for retry', async () => {
    const wrapper = mount(ChatGPTBillingSettings)
    await flushPromises()
    await wrapper.get('[data-tier=instant]').setValue('0.03')
    mocks.update.mockRejectedValueOnce(new Error('write failed'))
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(mocks.error).toHaveBeenCalledOnce()
    expect(mocks.success).not.toHaveBeenCalled()
    expect((wrapper.get('[data-tier=instant]').element as HTMLInputElement).value).toBe('0.03')
    expect(wrapper.get('button').attributes('disabled')).toBeUndefined()
  })
  it('loads and saves every tier independently and preserves the fallback', async () => {
    const settings = { success_turn_price_usd: 0.02, tier_prices_usd: { instant: 0.01, medium: 0.02, high: 0.03, extreme: 0.04, pro: 0.05 } }
    mocks.get.mockResolvedValue(settings)
    const wrapper = mount(ChatGPTBillingSettings)
    await flushPromises()
    expect((wrapper.get('[data-tier=pro]').element as HTMLInputElement).value).toBe('0.05')
    await wrapper.get('[data-tier=pro]').setValue('0.06')
    await wrapper.get('[data-tier=default]').setValue('0.025')
    const updated = { success_turn_price_usd: 0.025, tier_prices_usd: { ...settings.tier_prices_usd, pro: 0.06 } }
    mocks.update.mockResolvedValue(updated)
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(mocks.update).toHaveBeenCalledWith(updated)
    expect((wrapper.get('[data-tier=high]').element as HTMLInputElement).value).toBe('0.03')
  })

})
