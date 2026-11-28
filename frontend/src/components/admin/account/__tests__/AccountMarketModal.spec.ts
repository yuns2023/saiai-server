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

function listing(over: Record<string, unknown> = {}) {
  return { account_id: 11, owner_user_id: 7, plan: 'max', allowed_group_ids: [4], selected_group_id: null, status: 'unlisted', version: 1, ...over }
}

function view(over: Record<string, unknown> = {}) {
  return {
    account_id: 11,
    owner_user_id: 7,
    listing: null,
    allowed_tiers: [],
    ineligible_reason: '',
    market_enabled: true,
    ...over
  }
}

const tierGroups = [
  { id: 3, name: 'T1-Pro', platform: 'anthropic', rate_multiplier: 1.2, status: 'active', market_tier: true, market_plan: 'pro' },
  { id: 4, name: 'T2-Max', platform: 'anthropic', rate_multiplier: 1.6, status: 'active', market_tier: true, market_plan: 'max' },
  { id: 5, name: 'plain', platform: 'anthropic', rate_multiplier: 1, status: 'active', market_tier: false, market_plan: null },
  { id: 6, name: 'oai-tier', platform: 'openai', rate_multiplier: 1, status: 'active', market_tier: true, market_plan: 'oai' },
  { id: 7, name: 'T3-Ultra', platform: 'anthropic', rate_multiplier: 2, status: 'inactive', market_tier: true, market_plan: 'ultra' }
]

const planValues = (w: Awaited<ReturnType<typeof mountModal>>) => w.get('[data-testid=market-plan]').findAll('option').map((o) => o.element.getAttribute('value'))

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
  it('new config: one 套餐 select with this platform\'s live plans, no tier checkboxes or reservations; saves PUT {plan}', async () => {
    market.getListing.mockResolvedValue(view())
    const w = await mountModal()
    expect(planValues(w)).toEqual(['', 'max', 'pro'])
    expect(w.find('[data-testid^=market-allow-]').exists()).toBe(false)
    expect(w.find('[data-testid^=market-reserved_]').exists()).toBe(false)
    expect(w.text()).not.toContain('protection')
    // Nothing chosen: refused before sending.
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(market.putListing).not.toHaveBeenCalled()
    expect(w.get('[data-testid=market-error]').text()).toBe('market.account.planRequired')

    await w.get('[data-testid=market-plan]').setValue('max')
    // A plan not saved yet: the tiers are a preview from the group list.
    expect(w.get('[data-testid=market-tier-4]').text()).toContain('T2-Max ×1.6')
    expect(w.find('[data-testid=market-tier-3]').exists()).toBe(false)
    expect(w.find('[data-testid=market-plan-preview]').exists()).toBe(true)
    market.getListing.mockResolvedValue(view())
    market.putListing.mockResolvedValue(
      view({ listing: listing(), allowed_tiers: [{ group_id: 4, name: 'T2-Max', rate_multiplier: 1.6, available: true, admin_member: false }] })
    )
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(market.putListing).toHaveBeenCalledWith(11, { plan: 'max' })
    expect(w.emitted('updated')).toHaveLength(1)
    expect(w.find('[data-testid=market-plan-preview]').exists()).toBe(false)
  })

  it('existing listing: shows 共享中 status, the stored plan and the server-derived tiers read-only; keeps a plan with no live tier', async () => {
    market.getListing.mockResolvedValue(
      view({
        listing: listing({ plan: 'legacy', status: 'listed', selected_group_id: 9, version: 5 }),
        allowed_tiers: [{ group_id: 9, name: 'Old-Tier', rate_multiplier: 1.4, available: false, admin_member: false }]
      })
    )
    const w = await mountModal()
    expect(w.get('[data-testid=market-status-badge]').text()).toBe('market.status.listed')
    expect(w.get('[data-testid=market-version]').text()).toContain('"v":5')
    expect((w.get('[data-testid=market-plan]').element as HTMLSelectElement).value).toBe('legacy')
    expect(planValues(w)).toEqual(['', 'max', 'pro', 'legacy'])
    expect(w.get('[data-testid=market-plan]').text()).toContain('market.account.planNoTier')
    expect(w.get('[data-testid=market-tier-9]').text()).toContain('market.account.tierInactive')
    market.putListing.mockRejectedValue({ status: 400, reason: 'MARKET_PLAN_UNKNOWN', message: 'no live, active market tier has this plan' })
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(market.putListing).toHaveBeenCalledWith(11, { plan: 'legacy' })
    expect(w.get('[data-testid=market-error]').text()).toBe('market.errors.MARKET_PLAN_UNKNOWN')
  })

  it('MARKET_PLAN_INVALID from the server is shown in plain words', async () => {
    market.getListing.mockResolvedValue(view({ listing: listing() }))
    const w = await mountModal()
    market.putListing.mockRejectedValue({ status: 400, reason: 'MARKET_PLAN_INVALID', message: 'the plan must match …' })
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(w.get('[data-testid=market-error]').text()).toBe('market.errors.MARKET_PLAN_INVALID')
  })

  it('a plan with no live tier in the preview says so', async () => {
    market.getListing.mockResolvedValue(view({ listing: listing({ plan: 'legacy' }) }))
    const w = await mountModal()
    expect(w.find('[data-testid=market-derived-empty]').exists()).toBe(true)
  })

  it('a revoked listing: saving a plan re-grants (hint shown, PUT {plan})', async () => {
    market.getListing.mockResolvedValue(view({ listing: listing({ status: 'revoked', version: 4 }) }))
    const w = await mountModal()
    expect(w.find('[data-testid=market-revoke]').exists()).toBe(false)
    expect(w.find('[data-testid=market-regrant-hint]').exists()).toBe(true)
    market.putListing.mockResolvedValue(view({ listing: listing({ version: 5 }) }))
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(market.putListing).toHaveBeenCalledWith(11, { plan: 'max' })
  })

  it('a stale form never saves: the listing changed since it was loaded (version / status) → reload, no PUT', async () => {
    const loaded = view({ listing: listing({ status: 'revoked', version: 4 }) })
    market.getListing.mockResolvedValueOnce(view({ listing: listing({ version: 3 }) })).mockResolvedValueOnce(loaded)
    const w = await mountModal()
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(market.getListing).toHaveBeenCalledTimes(2)
    expect(market.putListing).not.toHaveBeenCalled()
    expect(w.get('[data-testid=market-error]').text()).toBe('market.account.staleReload')
    expect(w.get('[data-testid=market-status-badge]').text()).toBe('market.status.revoked')
  })

  it('another admin changed only the plan (same version and status) → reload, no PUT that would write the old plan back', async () => {
    market.getListing.mockResolvedValueOnce(view({ listing: listing({ plan: 'max', version: 3 }) })).mockResolvedValueOnce(view({ listing: listing({ plan: 'pro', version: 3 }) }))
    const w = await mountModal()
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(market.putListing).not.toHaveBeenCalled()
    expect(w.get('[data-testid=market-error]').text()).toBe('market.account.staleReload')
    expect((w.get('[data-testid=market-plan]').element as HTMLSelectElement).value).toBe('pro')
  })

  it('the owner changed since loading (no listing either time) → reload showing the new owner, no PUT', async () => {
    market.getListing.mockResolvedValueOnce(view({ owner_user_id: 7 })).mockResolvedValueOnce(view({ owner_user_id: 9 }))
    const w = await mountModal()
    await w.get('[data-testid=market-plan]').setValue('pro')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(market.putListing).not.toHaveBeenCalled()
    expect(w.get('[data-testid=market-error]').text()).toBe('market.account.staleReload')
    expect(w.get('[data-testid=market-status]').text()).toContain('#9')
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
    market.getListing.mockResolvedValue(view({ listing: listing() }))
    const w = await mountModal()
    await w.get('[data-testid=market-revoke]').trigger('click')
    expect(market.revokeListing).not.toHaveBeenCalled()
    market.revokeListing.mockResolvedValue(view({ listing: listing({ status: 'revoked', version: 2 }) }))
    await w.get('[data-testid=confirm-yes]').trigger('click')
    await flushPromises()
    expect(market.revokeListing).toHaveBeenCalledWith(11)
    expect(w.find('[data-testid=market-revoke]').exists()).toBe(false)
  })

  it('revoke re-reads first: a listing changed since the confirmation opened is shown, nothing is revoked', async () => {
    market.getListing.mockResolvedValueOnce(view({ listing: listing({ version: 3 }) })).mockResolvedValueOnce(view({ listing: listing({ version: 4, plan: 'pro' }) }))
    const w = await mountModal()
    await w.get('[data-testid=market-revoke]').trigger('click')
    await w.get('[data-testid=confirm-yes]').trigger('click')
    await flushPromises()
    expect(market.revokeListing).not.toHaveBeenCalled()
    expect(w.get('[data-testid=market-error]').text()).toBe('market.account.staleReload')
    expect((w.get('[data-testid=market-plan]').element as HTMLSelectElement).value).toBe('pro')
  })

  it('the target account changes while the revoke confirmation is open: it closes and revokes nothing', async () => {
    market.getListing.mockResolvedValue(view({ listing: listing() }))
    const w = await mountModal()
    await w.get('[data-testid=market-revoke]').trigger('click')
    expect(w.find('[data-testid=confirm]').exists()).toBe(true)
    await w.setProps({ account: { id: 12, name: 'other', platform: 'anthropic', type: 'setup-token', concurrency: 4, owner_user_id: 7 } as Account })
    await flushPromises()
    expect(w.find('[data-testid=confirm]').exists()).toBe(false)
    expect(market.revokeListing).not.toHaveBeenCalled()
  })

  describe('answers for an account no longer shown never touch the new one', () => {
    const B = { id: 12, name: 'acct-b', platform: 'anthropic', type: 'setup-token', concurrency: 4, owner_user_id: 8 } as Account
    const viewA = view({ listing: listing({ version: 3, plan: 'max' }) })
    const viewB = view({ account_id: 12, owner_user_id: 8, listing: listing({ account_id: 12, owner_user_id: 8, version: 7, plan: 'pro' }) })

    async function switchWhileRevokeGetPending(answerA: unknown) {
      let resolveA!: (v: unknown) => void
      market.getListing.mockResolvedValueOnce(viewA).mockReturnValueOnce(new Promise((r) => (resolveA = r))).mockResolvedValueOnce(viewB)
      const w = await mountModal()
      await w.get('[data-testid=market-revoke]').trigger('click')
      await w.get('[data-testid=confirm-yes]').trigger('click')
      // A's re-read is pending; the dialog moves to B, which loads.
      await w.setProps({ account: B })
      await flushPromises()
      expect((w.get('[data-testid=market-plan]').element as HTMLSelectElement).value).toBe('pro')
      resolveA(answerA)
      await flushPromises()
      return w
    }

    it('revoke: A\'s re-read returns changed → nothing applied to B, no error, no DELETE', async () => {
      const w = await switchWhileRevokeGetPending(view({ listing: listing({ version: 4, plan: 'ultra' }) }))
      expect((w.get('[data-testid=market-plan]').element as HTMLSelectElement).value).toBe('pro')
      expect(w.find('[data-testid=market-error]').exists()).toBe(false)
      expect(market.revokeListing).not.toHaveBeenCalled()
    })

    it('revoke: A\'s re-read returns unchanged → still no DELETE for A, nothing written into B', async () => {
      const w = await switchWhileRevokeGetPending(viewA)
      expect(market.revokeListing).not.toHaveBeenCalled()
      expect(w.get('[data-testid=market-status-badge]').text()).toBe('market.status.unlisted')
      expect(w.get('[data-testid=market-version]').text()).toContain('"v":7')
    })

    it('revoke: A\'s DELETE answers after the switch → B\'s form untouched, no success toast', async () => {
      let resolveDelete!: (v: unknown) => void
      market.getListing.mockResolvedValueOnce(viewA).mockResolvedValueOnce(viewA).mockResolvedValueOnce(viewB)
      market.revokeListing.mockReturnValueOnce(new Promise((r) => (resolveDelete = r)))
      const w = await mountModal()
      await w.get('[data-testid=market-revoke]').trigger('click')
      await w.get('[data-testid=confirm-yes]').trigger('click')
      await flushPromises()
      expect(market.revokeListing).toHaveBeenCalledWith(11)
      await w.setProps({ account: B })
      await flushPromises()
      resolveDelete(view({ listing: listing({ status: 'revoked', version: 4 }) }))
      await flushPromises()
      expect(w.get('[data-testid=market-status-badge]').text()).toBe('market.status.unlisted')
      expect((w.get('[data-testid=market-plan]').element as HTMLSelectElement).value).toBe('pro')
      expect(store.showSuccess).not.toHaveBeenCalled()
      expect(w.emitted('updated')).toBeUndefined()
    })

    it('save: A\'s PUT answers after the switch → B\'s form untouched', async () => {
      let resolvePut!: (v: unknown) => void
      market.getListing.mockResolvedValueOnce(viewA).mockResolvedValueOnce(viewA).mockResolvedValueOnce(viewB)
      market.putListing.mockReturnValueOnce(new Promise((r) => (resolvePut = r)))
      const w = await mountModal()
      await w.get('form').trigger('submit')
      await flushPromises()
      expect(market.putListing).toHaveBeenCalledWith(11, { plan: 'max' })
      await w.setProps({ account: B })
      await flushPromises()
      resolvePut(view({ listing: listing({ version: 4, plan: 'max' }) }))
      await flushPromises()
      expect((w.get('[data-testid=market-plan]').element as HTMLSelectElement).value).toBe('pro')
      expect(store.showSuccess).not.toHaveBeenCalled()
    })
  })

  it('a failed group read: an error with a retry; the stored plan stays selectable', async () => {
    groups.getAll.mockRejectedValueOnce({ status: 500 })
    market.getListing.mockResolvedValue(view({ listing: listing() }))
    const w = await mountModal()
    expect(w.find('[data-testid=market-tiers-failed]').exists()).toBe(true)
    expect(planValues(w)).toEqual(['', 'max'])
    await w.get('[data-testid=market-tiers-failed] button').trigger('click')
    await flushPromises()
    expect(planValues(w)).toEqual(['', 'max', 'pro'])
  })

  it('a failed read shows the error and a retry, never a form', async () => {
    market.getListing.mockRejectedValueOnce({ status: 500, message: 'boom' })
    const w = await mountModal()
    expect(w.find('[data-testid=market-form]').exists()).toBe(false)
    expect(w.get('[data-testid=market-load-error]').text()).toContain('boom')
  })
})
