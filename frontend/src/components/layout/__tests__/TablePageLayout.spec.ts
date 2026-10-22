import { afterEach, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import TablePageLayout from '../TablePageLayout.vue'

const originalWidth = window.innerWidth
const setWidth = (width: number) => {
  Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: width })
}

describe('TablePageLayout', () => {
  afterEach(() => setWidth(originalWidth))

  const render = (props: Record<string, unknown> = {}) =>
    mount(TablePageLayout, {
      props,
      slots: {
        actions: '<div data-testid="actions" />',
        filters: '<div data-testid="filters" />',
        table: '<div class="table-wrapper" data-testid="table" />',
        pagination: '<div data-testid="pagination" />',
      },
    })

  it('keeps the comfortable density by default', () => {
    setWidth(1820)
    const root = render().get('[data-testid="table-page-layout"]')
    expect(root.classes()).not.toContain('table-page-layout--dense')
    expect(root.classes()).not.toContain('mobile-mode')
  })

  it('opts into the dense variant and keeps the section order', () => {
    setWidth(1820)
    const wrapper = render({ dense: true })
    expect(wrapper.get('[data-testid="table-page-layout"]').classes()).toContain('table-page-layout--dense')
    const order = wrapper.findAll('[data-testid]').map((node) => node.attributes('data-testid'))
    expect(order).toEqual(['table-page-layout', 'actions', 'filters', 'table', 'pagination'])
    expect(wrapper.get('[data-testid="table"]').element.closest('.layout-section-scrollable')).not.toBeNull()
  })

  it('switches to natural mobile scrolling below 1024px', async () => {
    setWidth(390)
    const wrapper = render({ dense: true })
    await nextTick()
    expect(wrapper.get('[data-testid="table-page-layout"]').classes()).toContain('mobile-mode')
  })
})
