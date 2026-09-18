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
        key_limits: { configured: true },
        billing: { type: 'subscription', available: true, shared: true, plan_name: 'Team subscription' },
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
        usage: {
          range: { requests: 1, input_tokens: 10, output_tokens: 5, cache_tokens: 2, total_tokens: 17, actual_cost: 0.2, average_duration_ms: 100 },
          rpm: 1,
          tpm: 17,
        },
        recent_usage: {
          records: [{
            created_at: '2026-09-18T10:00:00Z',
            model: 'claude-sonnet',
            input_tokens: 10,
            output_tokens: 5,
            cache_creation_tokens: 2,
            cache_read_tokens: 0,
            total_tokens: 17,
            actual_cost: 0.2,
            duration_ms: 100,
            request_type: 'stream',
          }],
          pagination: { total: 11, page: 1, page_size: 10, pages: 2 },
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
    expect(text).toContain('keyUsage.recentUsage')
    expect(text).toContain('claude-sonnet')

    expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining('/v1/usage?'),
      expect.objectContaining({
        headers: { Authorization: 'Bearer sk-test-secret' },
        cache: 'no-store',
        referrerPolicy: 'no-referrer',
      }),
    )
    expect(vi.mocked(fetch).mock.calls[0][0]).not.toContain('sk-test-secret')
    expect(vi.mocked(fetch).mock.calls[0][0]).toContain('records_page=1')

    const nextButton = wrapper.findAll('button').find(button => button.text() === 'keyUsage.next')
    expect(nextButton).toBeDefined()
    await nextButton!.trigger('click')
    await flushPromises()
    expect(vi.mocked(fetch).mock.calls[1][0]).toContain('records_page=2')

    wrapper.unmount()
  })

  it('does not expose wallet balance when Key spending limits are configured', async () => {
    vi.mocked(fetch).mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        mode: 'quota_limited',
        billing_mode: 'balance',
        billing: { type: 'wallet', available: true, balance_visible: false },
        key_limits: { configured: true },
        isValid: true,
        status: 'active',
        quota: { limit: 300, used: 0, remaining: 300 },
        // A defensive UI check: even an older/misconfigured API response must
        // not render this value when the visibility contract says false.
        balance: 9195.54,
      }),
    } as Response)

    const wrapper = mount(KeyUsageView, {
      global: { stubs: { RouterLink: { template: '<a><slot /></a>' }, LocaleSwitcher: true, Icon: true } },
    })
    await wrapper.get('input[type="password"]').setValue('sk-limited')
    await wrapper.get('input[type="password"]').trigger('keydown.enter')
    await flushPromises()

    expect(wrapper.text()).toContain('keyUsage.keyLimitsConfigured')
    expect(wrapper.text()).toContain('keyUsage.totalQuota')
    expect(wrapper.text()).toContain('keyUsage.payAsYouGo')
    expect(wrapper.text()).not.toContain('$9195.54')
    wrapper.unmount()
  })

  it('shows the wallet balance for a pay-as-you-go Key without spending limits', async () => {
    vi.mocked(fetch).mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        mode: 'unrestricted',
        billing_mode: 'balance',
        billing: { type: 'wallet', available: true, balance_visible: true, balance: 99.25 },
        key_limits: { configured: false },
        isValid: true,
        status: 'active',
        balance: 99.25,
      }),
    } as Response)

    const wrapper = mount(KeyUsageView, {
      global: { stubs: { RouterLink: { template: '<a><slot /></a>' }, LocaleSwitcher: true, Icon: true } },
    })
    await wrapper.get('input[type="password"]').setValue('sk-wallet')
    await wrapper.get('input[type="password"]').trigger('keydown.enter')
    await flushPromises()

    expect(wrapper.text()).toContain('keyUsage.payAsYouGo')
    expect(wrapper.text()).toContain('$99.25')
    wrapper.unmount()
  })
})
