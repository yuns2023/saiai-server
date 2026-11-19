import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const settingsApi = vi.hoisted(() => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
  getAdminApiKey: vi.fn(),
  getOverloadCooldownSettings: vi.fn(),
  updateOverloadCooldownSettings: vi.fn()
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

const market = {
  market_enabled: true,
  market_platform_cut_rate: 0.15,
  market_earning_freeze_days: 7,
  market_min_convert_usd: 1,
  market_default_reserved_concurrency: 1,
  market_default_reserved_devices: 2,
  market_default_reserved_admissions: 1,
  market_default_cap_5h_pct: 70,
  market_default_cap_7d_pct: 60,
  market_default_sample_max_age_minutes: 30
}

function stored(over: Record<string, unknown> = {}) {
  return {
    registration_email_suffix_whitelist: [],
    default_subscriptions: [],
    custom_menu_items: [],
    api_endpoints: [],
    ...market,
    ...over
  }
}

async function mountView() {
  const w = mount(SettingsView, { global: { stubs } })
  await flushPromises()
  return w
}

async function save(w: Awaited<ReturnType<typeof mountView>>) {
  await w.find('form').trigger('submit')
  await flushPromises()
}

describe('admin settings: sharing market', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    settingsApi.getAdminApiKey.mockResolvedValue({ exists: false })
    settingsApi.getOverloadCooldownSettings.mockResolvedValue({ enabled: false, cooldown_minutes: 10 })
    proxiesApi.getAll.mockResolvedValue([])
  })

  const marketKeys = (payload: Record<string, unknown>) => Object.fromEntries(Object.entries(payload).filter(([k]) => k.startsWith('market_')))

  it('sends only edited market keys; explicit 0 cut rate and 0 freeze days are saved as 0', async () => {
    settingsApi.getSettings.mockResolvedValue(stored())
    settingsApi.updateSettings.mockImplementation(async (payload) => ({ ...stored(), ...payload }))
    const w = await mountView()
    expect(w.find('[data-testid=market-settings]').exists()).toBe(true)
    expect((w.get('[data-testid=market-cut-rate]').element as HTMLInputElement).value).toBe('0.15')
    // Saving other settings never writes the market values back (another tab may have changed them).
    await save(w)
    expect(marketKeys(settingsApi.updateSettings.mock.calls[0][0])).toEqual({})

    await w.get('[data-testid=market-cut-rate]').setValue('0')
    await w.get('[data-testid=market-freeze-days]').setValue('0')
    await save(w)
    const sent = settingsApi.updateSettings.mock.calls[1][0]
    expect(marketKeys(sent)).toEqual({ market_platform_cut_rate: 0, market_earning_freeze_days: 0 })
    // The saved 0 is what the form keeps (not a fallback to the default).
    expect((w.get('[data-testid=market-cut-rate]').element as HTMLInputElement).value).toBe('0')
  })

  it('a stored 0 cut rate loads as 0 and is not touched by another edit', async () => {
    settingsApi.getSettings.mockResolvedValue(stored({ market_platform_cut_rate: 0 }))
    settingsApi.updateSettings.mockImplementation(async (payload) => ({ ...stored({ market_platform_cut_rate: 0 }), ...payload }))
    const w = await mountView()
    expect((w.get('[data-testid=market-cut-rate]').element as HTMLInputElement).value).toBe('0')
    await w.get('[data-testid=market-freeze-days]').setValue('5')
    await save(w)
    expect(marketKeys(settingsApi.updateSettings.mock.calls[0][0])).toEqual({ market_earning_freeze_days: 5 })
    expect((w.get('[data-testid=market-cut-rate]').element as HTMLInputElement).value).toBe('0')
  })

  it('market on: invalid values block the save (cut rate 5 decimals, empty cut rate, min convert 3 decimals, cap 0)', async () => {
    settingsApi.getSettings.mockResolvedValue(stored())
    const w = await mountView()
    for (const [id, value] of [
      ['market-cut-rate', '0.12345'],
      ['market-cut-rate', ''],
      ['market-cut-rate', '1.5']
    ]) {
      await w.get(`[data-testid=${id}]`).setValue(value)
      await save(w)
    }
    await w.get('[data-testid=market-cut-rate]').setValue('0.2')
    await w.get('[data-testid=market-min-convert]').setValue('1.005')
    await save(w)
    await w.get('[data-testid=market-min-convert]').setValue('1')
    await w.get('[data-testid=market-default-market_default_cap_5h_pct]').setValue('0')
    await save(w)
    expect(settingsApi.updateSettings).not.toHaveBeenCalled()
    expect(appStore.showError).toHaveBeenCalledWith('market.settings.invalidField')
  })

  it('market off: an invalid edit never blocks other settings; only that field goes back', async () => {
    settingsApi.getSettings.mockResolvedValue(stored({ market_enabled: false }))
    settingsApi.updateSettings.mockImplementation(async (payload) => ({ ...stored(), ...payload }))
    const w = await mountView()
    await w.get('[data-testid=market-cut-rate]').setValue('')
    await w.get('[data-testid=market-freeze-days]').setValue('3')
    await save(w)
    expect(settingsApi.updateSettings).toHaveBeenCalledTimes(1)
    // The invalid field went back to the stored value, so it is not sent at all.
    expect(marketKeys(settingsApi.updateSettings.mock.calls[0][0])).toEqual({ market_earning_freeze_days: 3 })
    expect((w.get('[data-testid=market-cut-rate]').element as HTMLInputElement).value).toBe('0.15')
  })

  it('a failed settings read blocks saving: market values are never written from defaults', async () => {
    settingsApi.getSettings.mockRejectedValue({ status: 500, message: 'boom' })
    const w = await mountView()
    expect((w.get('[data-testid=settings-save]').element as HTMLButtonElement).disabled).toBe(true)
    await w.get('[data-testid=market-cut-rate]').setValue('0')
    await save(w)
    expect(settingsApi.updateSettings).not.toHaveBeenCalled()
  })
})
