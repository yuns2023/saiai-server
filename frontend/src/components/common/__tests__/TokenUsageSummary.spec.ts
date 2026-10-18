import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import TokenUsageSummary from '../TokenUsageSummary.vue'
import TokenCount from '../TokenCount.vue'

const testLocale = vi.hoisted(() => ({ value: 'zh' }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({
  t: (key: string) => (testLocale.value === 'zh' ? zh : en).tokens[key.split('.')[1] as keyof typeof en.tokens] || key
}) }))

const counts = { total_input_tokens: 14906, total_output_tokens: 54, total_cache_read_tokens: 6500, total_cache_creation_tokens: 160, total_tokens: 21620 }
const wrappers: ReturnType<typeof mount>[] = []
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()) })
const render = (locale = 'zh') => {
  testLocale.value = locale
  const wrapper = mount(TokenUsageSummary, {
    props: { data: counts, period: 'total' }
  })
  wrappers.push(wrapper)
  return wrapper
}

describe('TokenUsageSummary', () => {
  it('shows all four classes with shared precision and exact values on click', async () => {
    const wrapper = render()
    expect(wrapper.text()).toContain('21.62K')
    expect(wrapper.text()).toContain('非缓存输入: 14.91K')
    expect(wrapper.text()).toContain('输出: 54')
    expect(wrapper.text()).toContain('缓存读取: 6.5K')
    expect(wrapper.text()).toContain('缓存写入: 160')
    expect(wrapper.get('[data-testid=cache-read-rate]').text()).toContain('30.14%')
    await wrapper.get('button').trigger('click')
    await flushPromises()
    const tooltip = document.querySelector('[role="tooltip"]')!
    expect(tooltip.textContent).toContain('21,620')
    expect(tooltip.textContent).toContain('14,906')
    expect(tooltip.textContent).toContain('6,500')
    expect(tooltip.textContent).toContain('总 Token = 非缓存输入 + 输出 + 缓存读取 + 缓存写入')
    expect(wrapper.get('button').attributes('aria-describedby')).toBe(tooltip.id)
    await wrapper.get('button').trigger('keydown', { key: 'Escape' })
    expect(document.querySelector('[role="tooltip"]')).toBeNull()
  })

  it('supports hover and keyboard focus, and preserves zero counts', async () => {
    const wrapper = render('en')
    await wrapper.setProps({ data: { total_tokens: 0 } })
    expect(wrapper.text()).toContain('Cache read: 0')
    await wrapper.get('button').trigger('mouseenter')
    expect(document.querySelector('[role="tooltip"]')).not.toBeNull()
    await wrapper.get('button').trigger('mouseleave')
    expect(document.querySelector('[role="tooltip"]')).toBeNull()
    await wrapper.get('button').trigger('focus')
    expect(document.querySelector('[role="tooltip"]')?.textContent).toContain('Total Tokens (including cache): 0')
    await wrapper.get('button').trigger('blur')
    expect(document.querySelector('[role="tooltip"]')).toBeNull()
  })

  it('represents missing Token data as unavailable rather than zero', () => {
    const wrapper = mount(TokenCount)
    wrappers.push(wrapper)
    expect(wrapper.text()).toBe('—')
  })
})
