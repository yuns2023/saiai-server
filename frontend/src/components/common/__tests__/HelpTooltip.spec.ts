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

  it('stays open while focused even after the pointer leaves, and closes on Escape', async () => {
    const { wrapper, tooltip } = await open(500)
    await wrapper.trigger('focusin')
    await wrapper.trigger('mouseleave')
    await flushPromises()
    expect(tooltip.style.display).not.toBe('none')
    await wrapper.trigger('keydown', { key: 'Escape' })
    expect(tooltip.style.display).toBe('none')
  })

  it('pins open on tap/click and closes on a second tap or an outside tap', async () => {
    const { wrapper, tooltip } = await open(500)
    await wrapper.trigger('mouseleave')
    await wrapper.trigger('click')
    await flushPromises()
    expect(tooltip.style.display).not.toBe('none')
    await wrapper.trigger('click')
    expect(tooltip.style.display).toBe('none')

    await wrapper.trigger('click')
    expect(tooltip.style.display).not.toBe('none')
    document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await flushPromises()
    expect(tooltip.style.display).toBe('none')
  })

  it('closes a pinned tooltip on Escape anywhere, on scroll, but not when tapping inside it', async () => {
    const { wrapper, tooltip } = await open(500)
    await wrapper.trigger('click')
    tooltip.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await flushPromises()
    expect(tooltip.style.display).not.toBe('none')

    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(tooltip.style.display).toBe('none')

    await wrapper.trigger('click')
    window.dispatchEvent(new Event('scroll'))
    await flushPromises()
    expect(tooltip.style.display).toBe('none')
  })

  it('describes a focusable custom trigger and mounts lazily when asked', async () => {
    const wrapper = mount(HelpTooltip, {
      props: { lazy: true },
      slots: { trigger: '<button type="button">i</button>', default: 'Lazy body' },
      attachTo: document.body,
    })
    const describedBy = wrapper.get('button').attributes('aria-describedby')
    expect(describedBy).toMatch(/^help-tooltip-/)
    expect(document.getElementById(describedBy!)).toBeNull()
    await wrapper.get('button').trigger('focusin')
    await flushPromises()
    expect(document.getElementById(describedBy!)?.textContent).toContain('Lazy body')
    wrapper.unmount()
  })
})
