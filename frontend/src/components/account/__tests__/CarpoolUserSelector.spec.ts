import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import CarpoolUserSelector from '../CarpoolUserSelector.vue'
import { resetUserIdentityCache } from '@/components/admin/user/userIdentityCache'

const { getByIdMock, listMock } = vi.hoisted(() => ({ getByIdMock: vi.fn(), listMock: vi.fn() }))

vi.mock('@/api/admin/users', () => ({ getById: getByIdMock }))
vi.mock('@/api/admin', () => ({ adminAPI: { users: { list: listMock } } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('CarpoolUserSelector', () => {
  beforeEach(() => {
    resetUserIdentityCache()
    getByIdMock.mockReset()
  })

  it('shows saved users by email instead of a bare ID', async () => {
    getByIdMock.mockImplementation(async (id: number) => {
      if (id === 307) return { id, email: 'alice@example.com', username: 'alice', notes: 'team A' }
      throw new Error('boom')
    })
    const wrapper = mount(CarpoolUserSelector, { props: { modelValue: [307, 312] } })
    await flushPromises()

    const labels = wrapper.findAll('[data-testid="carpool-user-label"]')
    expect(labels.map(l => l.text())).toEqual(['alice@example.com', '#312'])
  })

  it('removes a user with the chip button', async () => {
    getByIdMock.mockResolvedValue({ id: 7, email: 'x@example.com', username: '', notes: '' })
    const wrapper = mount(CarpoolUserSelector, { props: { modelValue: [7, 8] } })
    await flushPromises()
    await wrapper.findAll('[data-testid="carpool-user-chip"]')[0].get('button').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([[8]])
  })

  it('adds to the selection by default but replaces it in single mode', async () => {
    getByIdMock.mockResolvedValue({ id: 7, email: 'x@example.com', username: '', notes: '' })
    listMock.mockResolvedValue({ items: [{ id: 9, email: 'nine@example.com', username: '', notes: '' }] })
    const multi = mount(CarpoolUserSelector, { props: { modelValue: [7] } })
    await multi.get('button').trigger('click')
    await flushPromises()
    expect(multi.get('label input').attributes('type')).toBe('checkbox')
    await multi.get('label input').trigger('change')
    expect(multi.emitted('update:modelValue')?.[0]).toEqual([[7, 9]])

    const single = mount(CarpoolUserSelector, { props: { modelValue: [7], single: true } })
    await single.get('button').trigger('click')
    await flushPromises()
    expect(single.get('label input').attributes('type')).toBe('radio')
    await single.get('label input').trigger('change')
    expect(single.emitted('update:modelValue')?.[0]).toEqual([[9]])
  })

  it('groups single-mode radios per picker and leaves checkboxes unnamed', async () => {
    listMock.mockResolvedValue({ items: [{ id: 1, email: 'a@x', username: '', notes: '' }, { id: 2, email: 'b@x', username: '', notes: '' }] })
    const Pair = defineComponent({
      setup: () => () => h('div', [
        h(CarpoolUserSelector, { modelValue: [], single: true }),
        h(CarpoolUserSelector, { modelValue: [], single: true }),
        h(CarpoolUserSelector, { modelValue: [] })
      ])
    })
    const wrapper = mount(Pair)
    for (const button of wrapper.findAll('button')) await button.trigger('click')
    await flushPromises()
    const names = wrapper.findAllComponents(CarpoolUserSelector).map(picker => picker.findAll('label input').map(input => input.attributes('name')))
    expect(names[0][0]).toBeTruthy()
    expect(names[0][0]).toBe(names[0][1])
    expect(names[1][0]).toBe(names[1][1])
    expect(names[0][0]).not.toBe(names[1][0])
    expect(names[2]).toEqual([undefined, undefined])
  })
})
