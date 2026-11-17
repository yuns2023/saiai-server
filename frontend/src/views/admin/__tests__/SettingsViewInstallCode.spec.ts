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

vi.mock('@/api', () => ({
  adminAPI: {
    settings: settingsApi,
    groups: { getAll: vi.fn().mockResolvedValue([]) }
  }
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), fetchPublicSettings: vi.fn() })
}))

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
  Select: true,
  GroupBadge: true,
  GroupOptionItem: true,
  Toggle: true,
  SocialOAuthSettingsCard: true,
  ImageUpload: true,
  ChatGPTBillingSettings: true,
  JevSettings: true,
  BackupSettings: true,
  RouterLink: true
}

function storedSettings(enabled: boolean) {
  return {
    registration_email_suffix_whitelist: [],
    default_subscriptions: [],
    custom_menu_items: [],
    api_endpoints: [],
    api_base_url: 'https://ccyou.dev',
    aitp_install_code_enabled: enabled
  }
}

describe('admin settings: one-time AITP install codes', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    settingsApi.getAdminApiKey.mockResolvedValue({ exists: false })
    settingsApi.getOverloadCooldownSettings.mockResolvedValue({ enabled: false, cooldown_minutes: 10 })
  })

  it('loads the switch and sends it on save', async () => {
    settingsApi.getSettings.mockResolvedValue(storedSettings(false))
    settingsApi.updateSettings.mockImplementation(async (payload) => ({ ...storedSettings(false), ...payload }))
    const w = mount(SettingsView, { global: { stubs } })
    await flushPromises()

    const box = w.find('[data-testid=aitp-install-code-enabled]')
    expect(box.exists()).toBe(true)
    expect((box.element as HTMLInputElement).checked).toBe(false)
    expect(w.text()).toContain('admin.settings.site.aitpInstallCodeHint')

    await box.setValue(true)
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(settingsApi.updateSettings).toHaveBeenCalledTimes(1)
    expect(settingsApi.updateSettings.mock.calls[0][0]).toMatchObject({ aitp_install_code_enabled: true })

    await box.setValue(false)
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(settingsApi.updateSettings.mock.calls[1][0]).toMatchObject({ aitp_install_code_enabled: false })
  })

  it('shows a stored "on"', async () => {
    settingsApi.getSettings.mockResolvedValue(storedSettings(true))
    const w = mount(SettingsView, { global: { stubs } })
    await flushPromises()
    expect((w.find('[data-testid=aitp-install-code-enabled]').element as HTMLInputElement).checked).toBe(true)
  })

  it('has English and Chinese texts that mention the API base URL requirement', () => {
    expect(en.admin.settings.site.aitpInstallCode).toBeTruthy()
    expect(zh.admin.settings.site.aitpInstallCode).toBeTruthy()
    expect(en.admin.settings.site.aitpInstallCodeHint).toContain('API Base URL')
    expect(zh.admin.settings.site.aitpInstallCodeHint).toContain('API 端点地址')
  })
})
