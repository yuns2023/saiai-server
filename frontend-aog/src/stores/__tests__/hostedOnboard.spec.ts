import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const hosted = vi.hoisted(() => ({ listOwnedAccounts: vi.fn(), selfOnboardConfig: vi.fn() }))
vi.mock('@/api', () => ({ hostedApi: hosted }))

import { useHostedStore } from '../hosted'

describe('hosted store: self-onboarding switch', () => {
  beforeEach(() => {
    hosted.listOwnedAccounts.mockReset()
    hosted.selfOnboardConfig.mockReset()
    setActivePinia(createPinia())
  })

  it('shows the entry to non-owners while the switch is on', async () => {
    const store = useHostedStore()
    expect(store.showEntry).toBe(false)
    hosted.selfOnboardConfig.mockResolvedValue({ enabled: true, exchange_proxy_configured: false, max_pending: 3 })
    await store.loadConfig()
    expect(store.showEntry).toBe(true)
    expect(store.onboard?.exchange_proxy_configured).toBe(false)
  })

  it('a failed check (old server) keeps the entry hidden and never rejects in the background', async () => {
    const store = useHostedStore()
    hosted.selfOnboardConfig.mockRejectedValue({ status: 404 })
    store.ensureConfig()
    await Promise.resolve()
    await new Promise((r) => setTimeout(r, 0))
    expect(store.showEntry).toBe(false)
    // Backoff: not asked again right away.
    store.ensureConfig()
    expect(hosted.selfOnboardConfig).toHaveBeenCalledTimes(1)
  })

  it('reset (sign-out) forgets the switch', async () => {
    const store = useHostedStore()
    hosted.selfOnboardConfig.mockResolvedValue({ enabled: true, exchange_proxy_configured: true, max_pending: 3 })
    await store.loadConfig()
    store.reset()
    expect(store.onboard).toBeNull()
    expect(store.showEntry).toBe(false)
  })

  it('applies an owner state answer to the listed account', async () => {
    const store = useHostedStore()
    hosted.listOwnedAccounts.mockResolvedValue([{ account_id: 12, current: true, owner_paused: false, withdraw_requested: false }])
    await store.load()
    store.applyOwnerState({ id: 12, owner_paused: true, withdraw_requested: false })
    expect(store.accounts[0].owner_paused).toBe(true)
  })

  it('a list read issued before an owner action cannot undo it', async () => {
    const store = useHostedStore()
    let answer!: (v: unknown) => void
    hosted.listOwnedAccounts.mockReturnValue(new Promise((r) => { answer = r }))
    const pending = store.load()
    store.applyOwnerState({ id: 12, owner_paused: false, withdraw_requested: true })
    answer([{ account_id: 12, current: true, owner_paused: false, withdraw_requested: false }])
    await pending
    expect(store.accounts[0].withdraw_requested).toBe(true)
  })

  it('sign-out clears an owner action in flight', () => {
    const store = useHostedStore()
    store.ownerBusy = 12
    store.reset()
    expect(store.ownerBusy).toBeNull()
  })

  it('drops an owner answer that arrives after sign-out', () => {
    const store = useHostedStore()
    const session = store.session()
    store.reset()
    expect(store.applyOwnerState({ id: 12, owner_paused: true, withdraw_requested: false }, session)).toBe(false)
  })
})
