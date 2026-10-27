import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const { listDevices, revokeDevice } = vi.hoisted(() => ({ listDevices: vi.fn(), revokeDevice: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { users: { getClientDevices: listDevices, revokeClientDevice: revokeDevice } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))
import UserClientDevicesModal from '../UserClientDevicesModal.vue'

const dialog = defineComponent({ template: '<div><slot /></div>' })
const device = {
  id: 4, device_id: 'cd_AAAAAAAAAAAAAAAAAAAAAA', user_id: 1, first_api_key_id: 2, public_key_sha256: 'ab', client_version: '1.1.54',
  os: 'windows', arch: 'x86_64', created_at: '2026-10-20T00:00:00Z', last_seen_at: null, last_ip: '', last_claude_at: null,
  last_codex_at: '2026-10-26T00:00:00Z', revoked_at: null, revoked_by: null
}

async function open(devices: object[]) {
  listDevices.mockResolvedValue(devices)
  const wrapper = mount(UserClientDevicesModal, {
    props: { show: false, user: { id: 1, email: 'alice@example.com' } as any },
    global: { stubs: { BaseDialog: dialog } }
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('UserClientDevicesModal', () => {
  it('lists devices and revokes an active one', async () => {
    const wrapper = await open([device, { ...device, id: 5, device_id: 'cd_BBBBBBBBBBBBBBBBBBBBBB', revoked_at: '2026-10-25T00:00:00Z' }])
    expect(listDevices).toHaveBeenCalledWith(1)
    expect(wrapper.findAll('[data-testid="client-device-row"]')).toHaveLength(2)
    expect(wrapper.text()).toContain('cd_AAAAAAAAAAAAAAAAAAAAAA')
    expect(wrapper.findAll('[data-testid="client-device-revoke"]')).toHaveLength(1)

    vi.spyOn(window, 'confirm').mockReturnValue(true)
    revokeDevice.mockResolvedValue({ message: 'ok' })
    await wrapper.get('[data-testid="client-device-revoke"]').trigger('click')
    await flushPromises()
    expect(revokeDevice).toHaveBeenCalledWith(1, 4)
    expect(wrapper.emitted('changed')).toHaveLength(1)
    expect(listDevices).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('shows the empty state', async () => {
    const wrapper = await open([])
    expect(wrapper.find('[data-testid="client-devices-empty"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('drops the previous user\'s devices and late responses when switching users', async () => {
    let resolveA: (v: object[]) => void = () => {}
    listDevices.mockReset()
    listDevices.mockImplementationOnce(() => new Promise((resolve) => { resolveA = resolve }))
    listDevices.mockResolvedValueOnce([{ ...device, id: 9, user_id: 2, device_id: 'cd_CCCCCCCCCCCCCCCCCCCCCC' }])
    const wrapper = mount(UserClientDevicesModal, {
      props: { show: true, user: { id: 1, email: 'a@example.com' } as any },
      global: { stubs: { BaseDialog: dialog } }
    })
    await wrapper.setProps({ user: { id: 2, email: 'b@example.com' } as any })
    await flushPromises()
    resolveA([device])
    await flushPromises()
    expect(wrapper.text()).toContain('cd_CCCCCCCCCCCCCCCCCCCCCC')
    expect(wrapper.text()).not.toContain('cd_AAAAAAAAAAAAAAAAAAAAAA')
    wrapper.unmount()
  })
})
