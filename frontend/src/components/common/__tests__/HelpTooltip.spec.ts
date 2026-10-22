import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import HelpTooltip from '../HelpTooltip.vue'

const rect = (top: number, left = 400) =>
  ({ top, bottom: top + 16, left, right: left + 16, width: 16, height: 16, x: left, y: top, toJSON: () => ({}) }) as DOMRect

describe('HelpTooltip', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    document.body.innerHTML = ''
  })

  const open = async (top: number, left?: number, height = 120) => {
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(height)
    const wrapper = mount(HelpTooltip, { props: { content: 'Explanation' }, attachTo: document.body })
    vi.spyOn(wrapper.element as HTMLElement, 'getBoundingClientRect').mockReturnValue(rect(top, left))
    await wrapper.trigger('mouseenter')
    await flushPromises()
    return { wrapper, tooltip: document.body.querySelector('[role="tooltip"]') as HTMLElement }
  }

  it('opens above the trigger in viewport coordinates when there is room', async () => {
    const { tooltip } = await open(500)
    expect(tooltip.textContent).toContain('Explanation')
    expect(tooltip.classList.contains('-translate-y-full')).toBe(true)
    expect(tooltip.style.top).toBe('492px')
  })

  it('flips below the trigger when it would be clipped by the viewport top', async () => {
    const { tooltip } = await open(60)
    expect(tooltip.classList.contains('-translate-y-full')).toBe(false)
    expect(tooltip.style.top).toBe('84px')
  })

  it('keeps the tooltip inside the left edge of the viewport', async () => {
    const { tooltip } = await open(500, 0)
    expect(tooltip.style.left).toBe('136px')
  })

  it('opens on keyboard focus and closes on blur', async () => {
    const { wrapper, tooltip } = await open(500)
    await wrapper.trigger('mouseleave')
    expect(tooltip.style.display).toBe('none')
    await wrapper.trigger('focusin')
    await flushPromises()
    expect(tooltip.style.display).not.toBe('none')
    await wrapper.trigger('focusout')
    expect(tooltip.style.display).toBe('none')
  })
})
