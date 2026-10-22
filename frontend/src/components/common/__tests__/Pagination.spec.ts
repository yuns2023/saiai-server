import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import Pagination from '../Pagination.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const render = (dense?: boolean) =>
  mount(Pagination, {
    props: { total: 412, page: 1, pageSize: 20, ...(dense === undefined ? {} : { dense }) },
    global: { stubs: { Select: true, Icon: true } },
  })

describe('Pagination density', () => {
  it('keeps the default vertical padding', () => {
    const root = render().element as HTMLElement
    expect(root.classList.contains('py-3')).toBe(true)
    expect(root.classList.contains('pagination-dense')).toBe(false)
  })

  it('uses tighter padding in dense mode without changing the controls', () => {
    const wrapper = render(true)
    const root = wrapper.element as HTMLElement
    expect(root.classList.contains('py-2')).toBe(true)
    expect(root.classList.contains('pagination-dense')).toBe(true)
    expect(wrapper.findAll('nav button').length).toBeGreaterThan(2)
    expect(wrapper.get('nav button[aria-current="page"]').text()).toBe('1')
  })
})
