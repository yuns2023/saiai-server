import { flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import KeyUsageView from '../KeyUsageView.vue'

const { showInfo, showSuccess, showError, fetchPublicSettings } = vi.hoisted(() => ({
  showInfo: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
  fetchPublicSettings: vi.fn(),
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    publicSettingsLoaded: true,
    cachedPublicSettings: { site_name: 'SAIAI' },
    showInfo,
    showSuccess,
    showError,
    fetchPublicSettings,
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      locale: ref('en'),
      t: (key: string) => key,
    }),
  }
})

describe('KeyUsageView', () => {
  beforeEach(() => {
    vi.stubGlobal('requestAnimationFrame', () => 1)
    vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false }))
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: globalThis.matchMedia,
    })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        mode: 'quota_limited',
        billing_mode: 'subscription',
        isValid: true,
        status: 'active',
        planName: 'Team subscription',
        remaining: 17,
        quota: { limit: 20, used: 3, remaining: 17 },
        subscription_status: 'active',
        subscription: {
          status: 'active',
          shared: true,
          remaining: 6,
          five_hour_usage_usd: 1,
          five_hour_limit_usd: 5,
          daily_usage_usd: 4,
          daily_limit_usd: 10,
          weekly_usage_usd: 0,
          weekly_limit_usd: null,
          monthly_usage_usd: 0,
          monthly_limit_usd: null,
          expires_at: '2026-10-01T00:00:00Z',
          days_remaining: 12,
        },
      }),
    }))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.clearAllMocks()
  })

  it('shows Key limits and shared subscription limits in one result', async () => {
    const wrapper = mount(KeyUsageView, {
      global: {
        stubs: {
          RouterLink: { template: '<a><slot /></a>' },
          LocaleSwitcher: true,
          Icon: true,
        },
      },
    })

    const input = wrapper.get('input[type="password"]')
    await input.setValue('sk-test-secret')
    await input.trigger('keydown.enter')
    await flushPromises()

    const text = wrapper.text()
    expect(text).toContain('keyUsage.totalQuota')
    expect(text).toContain('keyUsage.subscriptionLimit5h')
    expect(text).toContain('keyUsage.subscriptionLimitDaily')
    expect(text).toContain('keyUsage.sharedSubscriptionRemaining')
    expect(text).toContain('Team subscription')

    expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining('/v1/usage?'),
      expect.objectContaining({
        headers: { Authorization: 'Bearer sk-test-secret' },
        cache: 'no-store',
        referrerPolicy: 'no-referrer',
      }),
    )
    expect(vi.mocked(fetch).mock.calls[0][0]).not.toContain('sk-test-secret')

    wrapper.unmount()
  })
})
