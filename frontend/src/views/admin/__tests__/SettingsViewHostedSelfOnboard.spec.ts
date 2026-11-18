import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

const settingsApi = vi.hoisted(() => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
  getAdminApiKey: vi.fn(),
  getOverloadCooldownSettings: vi.fn()
}))
const proxiesApi = vi.hoisted(() => ({ getAll: vi.fn() }))
const appStore = vi.hoisted(() => ({ showError: vi.fn(), showSuccess: vi.fn(), fetchPublicSettings: vi.fn() }))

vi.mock('@/api', () => ({
  adminAPI: {
    settings: settingsApi,
    proxies: proxiesApi,
    groups: { getAll: vi.fn().mockResolvedValue([]) }
  }
}))

vi.mock('@/stores', () => ({ useAppStore: () => appStore }))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => ({ fetch: vi.fn() })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard: vi.fn() })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

import SettingsView from '../SettingsView.vue'

const stubs = {
  AppLayout: { template: '<div><slot /></div>' },
  Icon: true,
  Select: { props: ['modelValue', 'options'], template: '<div class="select-stub" :data-value="modelValue">{{ options.map((o) => o.label).join("|") }}</div>' },
  GroupBadge: true,
  GroupOptionItem: true,
  Toggle: { props: ['modelValue'], emits: ['update:modelValue'], template: '<input type="checkbox" :checked="modelValue" @change="$emit(\'update:modelValue\', $event.target.checked)" />' },
  SocialOAuthSettingsCard: true,
  ImageUpload: true,
  ChatGPTBillingSettings: true,
  JevSettings: true,
  BackupSettings: true,
  RouterLink: true
}

const hosted = {
  hosted_self_onboard_enabled: true,
  hosted_self_onboard_exchange_proxy_id: 8,
  hosted_self_fee_rate: 0.15,
  hosted_self_onboard_default_concurrency: 4,
  hosted_self_onboard_default_priority: 2,
  hosted_self_onboard_default_device_limit: 6,
  hosted_self_onboard_default_admissions_per_cycle: 2,
  hosted_self_onboard_default_interval_hours: 12
}

function stored(over: Record<string, unknown> = {}) {
  return {
    registration_email_suffix_whitelist: [],
    default_subscriptions: [],
    custom_menu_items: [],
    api_endpoints: [],
    ...hosted,
    ...over
  }
}

async function mountView() {
  const w = mount(SettingsView, { global: { stubs } })
  await flushPromises()
  return w
}

describe('admin settings: hosted self-onboarding', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    settingsApi.getAdminApiKey.mockResolvedValue({ exists: false })
    settingsApi.getOverloadCooldownSettings.mockResolvedValue({ enabled: false, cooldown_minutes: 10 })
    proxiesApi.getAll.mockResolvedValue([{ id: 8, name: 'HK-1', protocol: 'socks5h', host: 'hk.example.com', port: 1080, status: 'active' }])
  })

  it('round-trips every hosted self-onboarding field through the full-object save', async () => {
    settingsApi.getSettings.mockResolvedValue(stored())
    settingsApi.updateSettings.mockImplementation(async (payload) => ({ ...stored(), ...payload }))
    const w = await mountView()
    const block = w.get('[data-testid=hosted-self-onboard-settings]')
    expect(block.text()).toContain('HK-1 · socks5h://hk.example.com:1080')
    expect(block.text()).toContain('admin.settings.hostedSelfOnboard.exchangeProxyNone')
    expect((w.get('[data-testid=hosted-self-fee-rate]').element as HTMLInputElement).value).toBe('0.15')

    // Saving without touching the block sends the stored values back unchanged.
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(settingsApi.updateSettings).toHaveBeenCalledTimes(1)
    expect(settingsApi.updateSettings.mock.calls[0][0]).toMatchObject(hosted)

    await w.get('[data-testid=hosted-self-fee-rate]').setValue('0.2')
    await w.get('[data-testid=hosted-default-hosted_self_onboard_default_interval_hours]').setValue('48')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(settingsApi.updateSettings.mock.calls[1][0]).toMatchObject({ ...hosted, hosted_self_fee_rate: 0.2, hosted_self_onboard_default_interval_hours: 48 })
  })

  it('shows a stored proxy that is no longer listed', async () => {
    settingsApi.getSettings.mockResolvedValue(stored({ hosted_self_onboard_exchange_proxy_id: 99 }))
    const w = await mountView()
    expect(w.get('[data-testid=hosted-self-onboard-settings]').text()).toContain('admin.settings.hostedSelfOnboard.exchangeProxyMissing')
  })

  it('refuses to save a negative fee rate or an out-of-range default', async () => {
    settingsApi.getSettings.mockResolvedValue(stored())
    const w = await mountView()
    await w.get('[data-testid=hosted-self-fee-rate]').setValue('-1')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(settingsApi.updateSettings).not.toHaveBeenCalled()
    expect(appStore.showError).toHaveBeenCalledWith('admin.settings.hostedSelfOnboard.invalidFeeRate')

    await w.get('[data-testid=hosted-self-fee-rate]').setValue('0.1')
    await w.get('[data-testid=hosted-default-hosted_self_onboard_default_device_limit]').setValue('33')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(settingsApi.updateSettings).not.toHaveBeenCalled()
  })

  it('has English and Chinese texts', () => {
    expect(zh.admin.settings.hostedSelfOnboard.title).toBe('托管自助上号')
    expect(en.admin.settings.hostedSelfOnboard.title).toBeTruthy()
    expect(Object.keys(en.admin.hostedApplications).sort()).toEqual(Object.keys(zh.admin.hostedApplications).sort())
    expect(Object.keys(en.admin.settings.hostedSelfOnboard).sort()).toEqual(Object.keys(zh.admin.settings.hostedSelfOnboard).sort())
    expect(zh.nav.hostedApplications).toBe('托管申请')
    expect(zh.admin.accounts.identityUnknown).toBe('身份未知')
  })
})
