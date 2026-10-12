import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const { listDevices } = vi.hoisted(() => ({ listDevices: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { users: { getClaudeDevices: listDevices } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))
import UserClaudeDevicesModal from '../UserClaudeDevicesModal.vue'

const dialog = defineComponent({ template: '<div><slot /></div>' })

async function open(devices: object[]) {
  listDevices.mockResolvedValue(devices)
  const wrapper = mount(UserClaudeDevicesModal, {
    props: { show: false, user: { id: 1, email: 'alice@example.com' } as any },
    global: { stubs: { BaseDialog: dialog } }
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

const device = { id: 1, user_id: 1, group_id: 3, device_id: 'test-device', first_seen_at: '2026-10-12T00:00:00Z', last_seen_at: '2026-10-12T00:00:00Z' }

describe('UserClaudeDevicesModal client environment', () => {
  it('shows the captured UA, OS, architecture, runtime and SDK', async () => {
    const wrapper = await open([{ ...device, client_hints: { user_agent: 'claude-cli/2.1.296 (external, cli)', os: 'Windows', arch: 'x64', runtime: 'node', runtime_version: 'v26.3.0', sdk_version: '0.128.0' } }])
    expect(wrapper.text()).toContain('UA: claude-cli/2.1.296 (external, cli)')
    expect(wrapper.text()).toContain('OS / Arch: Windows / x64')
    expect(wrapper.text()).toContain('Runtime: node v26.3.0')
    expect(wrapper.text()).toContain('SDK: 0.128.0')
    wrapper.unmount()
  })

  it('renders missing historical fields without inventing environment values', async () => {
    const wrapper = await open([device])
    expect(wrapper.text()).toContain('UA: —')
    expect(wrapper.text()).toContain('OS / Arch: — / —')
    expect(wrapper.text()).toContain('Runtime: —')
    expect(wrapper.text()).toContain('SDK: —')
    wrapper.unmount()
  })
})
