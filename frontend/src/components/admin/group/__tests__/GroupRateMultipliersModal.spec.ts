import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import GroupRateMultipliersModal from '../GroupRateMultipliersModal.vue'
import type { AdminGroup } from '@/types'

const api = vi.hoisted(() => ({ get: vi.fn(), save: vi.fn(), users: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: {
  groups: { getGroupRateMultipliers: api.get, batchSetGroupRateMultipliers: api.save },
  users: { list: api.users }
} }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const entry = { user_id: 7, user_email: 'user@example.com', user_name: '', user_notes: '', user_status: 'active', rate_multiplier: 1 }
const makeModal = async () => {
  const wrapper = mount(GroupRateMultipliersModal, {
    props: { show: false, group: { id: 202, name: 'Test', platform: 'anthropic', rate_multiplier: 1 } as AdminGroup },
    global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, Pagination: true, PlatformIcon: true, Icon: true } }
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}
type Modal = Awaited<ReturnType<typeof makeModal>>
const button = (wrapper: Modal, key: string) => wrapper.findAll('button').find(item => item.text() === key)!
const save = async (wrapper: Modal) => {
  await button(wrapper, 'common.save').trigger('click')
  await flushPromises()
  expect(api.save).toHaveBeenCalledWith(202, [{ user_id: 7, rate_multiplier: 0 }])
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.clearAllMocks()
  api.get.mockResolvedValue([])
  api.save.mockResolvedValue({})
  api.users.mockResolvedValue({ items: [{ id: 7, email: entry.user_email, status: 'active' }] })
})
afterEach(() => { vi.useRealTimers() })

describe('group-specific zero rates', () => {
  it('adds and saves a zero rate', async () => {
    const wrapper = await makeModal()
    await wrapper.get('input[type="text"]').setValue('user')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    await wrapper.get('button').trigger('click')
    const rate = wrapper.get('input[type="number"]')
    const add = button(wrapper, 'common.add')
    expect(add.attributes('disabled')).toBeDefined()
    await rate.setValue('-1')
    expect(add.attributes('disabled')).toBeDefined()
    await rate.setValue('0')
    expect(add.attributes('disabled')).toBeUndefined()
    await add.trigger('click')
    await save(wrapper)
    wrapper.unmount()
  })

  it('marks editing an existing rate to zero as dirty on input', async () => {
    api.get.mockResolvedValue([{ ...entry }])
    const wrapper = await makeModal()
    const rate = wrapper.get('tbody input')
    ;(rate.element as HTMLInputElement).value = '0'
    await rate.trigger('input')
    await save(wrapper)
    wrapper.unmount()
  })

  it('previews, applies and saves a zero batch factor', async () => {
    api.get.mockResolvedValue([{ ...entry }])
    const wrapper = await makeModal()
    const batch = wrapper.get('input[placeholder="0.5"]')
    const apply = button(wrapper, 'admin.groups.applyMultiplier')
    expect(apply.attributes('disabled')).toBeDefined()
    await batch.setValue('-1')
    expect(apply.attributes('disabled')).toBeDefined()
    await batch.setValue('0')
    expect(apply.attributes('disabled')).toBeUndefined()
    expect(wrapper.get('tbody tr').findAll('td')[6].text()).toBe('0')
    await apply.trigger('click')
    await save(wrapper)
    wrapper.unmount()
  })

  it('loads a saved zero without marking it dirty', async () => {
    api.get.mockResolvedValue([{ ...entry, rate_multiplier: 0 }])
    const wrapper = await makeModal()
    expect((wrapper.get('tbody input').element as HTMLInputElement).value).toBe('0')
    expect(button(wrapper, 'common.save')).toBeUndefined()
    expect(api.save).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
