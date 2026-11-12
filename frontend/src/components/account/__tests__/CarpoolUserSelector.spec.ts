import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import CarpoolUserSelector from '../CarpoolUserSelector.vue'
import { resetUserIdentityCache } from '@/components/admin/user/userIdentityCache'

const { getByIdMock } = vi.hoisted(() => ({ getByIdMock: vi.fn() }))

vi.mock('@/api/admin/users', () => ({ getById: getByIdMock }))
vi.mock('@/api/admin', () => ({ adminAPI: { users: { list: vi.fn() } } }))
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
})
