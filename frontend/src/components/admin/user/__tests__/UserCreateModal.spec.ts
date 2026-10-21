import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UserCreateModal from '../UserCreateModal.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin', () => ({ adminAPI: { users: { create: vi.fn() } } }))
vi.mock('@/composables/useForm', () => ({ useForm: () => ({ loading: false, submit: vi.fn() }) }))

describe('create user device capacity placement', () => {
  it('starts with identity and puts device capacity after concurrency', () => {
    const wrapper = mount(UserCreateModal, {
      props: { show: true },
      global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' }, Icon: true } }
    })
    const labels = wrapper.findAll('label').map(label => label.text())
    expect(labels[0]).toBe('admin.users.email')
    expect(labels.indexOf('admin.users.claudeDeviceBaseLimit')).toBe(labels.indexOf('admin.users.columns.concurrency') + 1)
    const capacity = wrapper.get('input[min="1"][max="100"]')
    expect((capacity.element as HTMLInputElement).value).toBe('3')
    wrapper.unmount()
  })
})
