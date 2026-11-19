import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AccountMarketModal from '../AccountMarketModal.vue'
import type { Account } from '@/types'

const market = vi.hoisted(() => ({ getListing: vi.fn(), putListing: vi.fn(), revokeListing: vi.fn() }))
const groups = vi.hoisted(() => ({ getAll: vi.fn() }))
const store = vi.hoisted(() => ({ showSuccess: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { market, groups } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => store }))
vi.mock('@/api/client', () => ({ apiClient: {} }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, p?: Record<string, unknown>) => (p ? `${key}:${JSON.stringify(p)}` : key), te: () => true }) }))

const protection = { reserved_concurrency: 1, reserved_devices: 2, reserved_admissions: 1, cap_5h_pct: 70, cap_7d_pct: 60, sample_max_age_minutes: 30 }

function view(over: Record<string, unknown> = {}) {
  return {
    account_id: 11,
    owner_user_id: 7,
    listing: null,
    allowed_tiers: [],
    ineligible_reason: '',
    default_protection: protection,
    market_enabled: true,
    ...over
  }
}

const tierGroups = [
  { id: 3, name: 'T1-Pro', platform: 'anthropic', rate_multiplier: 1.2, status: 'active', market_tier: true },
  { id: 4, name: 'T2-Max', platform: 'anthropic', rate_multiplier: 1.6, status: 'active', market_tier: true },
  { id: 5, name: 'plain', platform: 'anthropic', rate_multiplier: 1, status: 'active', market_tier: false },
  { id: 6, name: 'oai-tier', platform: 'openai', rate_multiplier: 1, status: 'active', market_tier: true }
]

const mountModal = async (account: Partial<Account> = {}) => {
  const w = mount(AccountMarketModal, {
    props: { show: true, account: { id: 11, name: 'acct', platform: 'anthropic', type: 'setup-token', concurrency: 4, claude_oauth_carpool_device_limit: 5, owner_user_id: 7, owner_email: 'o@example.com', ...account } as Account },
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        ConfirmDialog: { props: ['show'], emits: ['confirm', 'cancel'], template: '<div v-if="show" data-testid="confirm"><button data-testid="confirm-yes" @click="$emit(\'confirm\')" /></div>' }
      }
    }
  })
  await flushPromises()
  return w
}

beforeEach(() => {
  vi.clearAllMocks()
  groups.getAll.mockResolvedValue(tierGroups)
})

describe('AccountMarketModal', () => {
  it('new config: offers only this platform\'s tiers, starts from the defaults, saves explicit zeros', async () => {
    market.getListing.mockResolvedValue(view())
    const w = await mountModal()
    expect(w.find('[data-testid=market-allow-3]').exists()).toBe(true)
    expect(w.find('[data-testid=market-allow-4]').exists()).toBe(true)
    expect(w.find('[data-testid=market-allow-5]').exists()).toBe(false)
    expect(w.find('[data-testid=market-allow-6]').exists()).toBe(false)
    expect((w.get('[data-testid=market-reserved_devices]').element as HTMLInputElement).value).toBe('2')
    // Per-account bound: concurrency 4 → at most 3 reserved.
    expect(w.get('[data-testid=market-reserved_concurrency]').attributes('max')).toBe('3')

    await w.get('[data-testid=market-allow-4] input').setValue(true)
    await w.get('[data-testid=market-allow-3] input').setValue(true)
    await w.get('[data-testid=market-reserved_concurrency]').setValue('0')
    market.putListing.mockResolvedValue(view({ listing: { allowed_group_ids: [3, 4], status: 'unlisted', version: 1, protection: { ...protection, reserved_concurrency: 0 } } }))
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(market.putListing).toHaveBeenCalledWith(11, { allowed_group_ids: [3, 4], ...protection, reserved_concurrency: 0 })
    expect(w.emitted('updated')).toHaveLength(1)
  })

  it('refuses an out-of-range value before sending (static and per-account bounds)', async () => {
    market.getListing.mockResolvedValue(view())
    const w = await mountModal()
    await w.get('[data-testid=market-cap_5h_pct]').setValue('0')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(market.putListing).not.toHaveBeenCalled()
    await w.get('[data-testid=market-cap_5h_pct]').setValue('70')
    // Concurrency 4: reserving 4 would leave buyers nothing.
    await w.get('[data-testid=market-reserved_concurrency]').setValue('4')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(market.putListing).not.toHaveBeenCalled()
    expect(w.get('[data-testid=market-error]').text()).toContain('market.account.invalidField')
  })

  it('existing listing: shows status/version, keeps a tier that is no longer a tier, shows server errors', async () => {
    market.getListing.mockResolvedValue(
      view({
        listing: { allowed_group_ids: [3, 99], selected_group_id: 3, status: 'listed', version: 5, protection },
        allowed_tiers: [{ group_id: 3, name: 'T1-Pro', rate_multiplier: 1.2, available: true, admin_member: false }]
      })
    )
    const w = await mountModal()
    expect(w.get('[data-testid=market-status-badge]').text()).toBe('market.status.listed')
    expect(w.get('[data-testid=market-version]').text()).toContain('"v":5')
    expect(w.get('[data-testid=market-allow-99]').text()).toContain('market.account.tierGone')
    market.putListing.mockRejectedValue({ status: 400, reason: 'MARKET_INVALID_PARAMS', message: 'reserved_devices must be at most the device limit - 1' })
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(w.get('[data-testid=market-error]').text()).toBe('market.errors.MARKET_INVALID_PARAMS（reserved_devices must be at most the device limit - 1）')
  })

  it('a stale form never saves: the listing changed since it was loaded (version / status) → reload, no PUT', async () => {
    const loaded = view({ listing: { allowed_group_ids: [3], status: 'revoked', version: 4, protection } })
    market.getListing.mockResolvedValueOnce(view({ listing: { allowed_group_ids: [3], status: 'unlisted', version: 3, protection } })).mockResolvedValueOnce(loaded)
    const w = await mountModal()
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(market.getListing).toHaveBeenCalledTimes(2)
    expect(market.putListing).not.toHaveBeenCalled()
    expect(w.get('[data-testid=market-error]').text()).toBe('market.account.staleReload')
    expect(w.get('[data-testid=market-status-badge]').text()).toBe('market.status.revoked')
  })

  it('owner label comes from the listing endpoint, the row email only for the same owner', async () => {
    market.getListing.mockResolvedValue(view({ owner_user_id: null }))
    const w = await mountModal()
    expect(w.get('[data-testid=market-status]').text()).toContain('market.account.noOwner')
    market.getListing.mockResolvedValue(view({ owner_user_id: 8 }))
    const w2 = await mountModal()
    expect(w2.get('[data-testid=market-status]').text()).not.toContain('o@example.com')
    expect(w2.get('[data-testid=market-status]').text()).toContain('#8')
  })

  it('revoke asks for confirmation first', async () => {
    market.getListing.mockResolvedValue(view({ listing: { allowed_group_ids: [3], status: 'unlisted', version: 1, protection } }))
    const w = await mountModal()
    await w.get('[data-testid=market-revoke]').trigger('click')
    expect(market.revokeListing).not.toHaveBeenCalled()
    market.revokeListing.mockResolvedValue(view({ listing: { allowed_group_ids: [], status: 'revoked', version: 2, protection } }))
    await w.get('[data-testid=confirm-yes]').trigger('click')
    await flushPromises()
    expect(market.revokeListing).toHaveBeenCalledWith(11)
    expect(w.find('[data-testid=market-revoke]').exists()).toBe(false)
  })

  it('a failed read shows the error and a retry, never a form', async () => {
    market.getListing.mockRejectedValueOnce({ status: 500, message: 'boom' })
    const w = await mountModal()
    expect(w.find('[data-testid=market-form]').exists()).toBe(false)
    expect(w.get('[data-testid=market-load-error]').text()).toContain('boom')
  })
})
