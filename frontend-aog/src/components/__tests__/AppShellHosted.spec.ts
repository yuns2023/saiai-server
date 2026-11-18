import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount, RouterLinkStub } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { i18n } from '@/i18n'
import type { OwnedAccount } from '@/api'
import type { User } from '@/types'

const hosted = vi.hoisted(() => ({ listOwnedAccounts: vi.fn(), listOwnedAccountUsage: vi.fn(), selfOnboardConfig: vi.fn() }))
vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return {
    ...actual,
    hostedApi: hosted,
    announcementsApi: { list: vi.fn().mockResolvedValue([]), markRead: vi.fn() },
    authApi: { ...actual.authApi, logout: vi.fn().mockResolvedValue(undefined) }
  }
})
vi.mock('vue-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-router')>()
  const { reactive } = await import('vue')
  const route = reactive({ path: '/dashboard', fullPath: '/dashboard' })
  return { ...actual, useRoute: () => route, useRouter: () => ({ replace: vi.fn() }) }
})

import { useRoute } from 'vue-router'
import AppShell from '../AppShell.vue'
import { useAuthStore } from '@/stores/auth'
import { HOSTED_RETRY_MS, HOSTED_TTL_MS, useHostedStore } from '@/stores/hosted'

function account(over: Partial<OwnedAccount> = {}): OwnedAccount {
  return {
    account_id: 12,
    current: true,
    display_name: '托管号 #12',
    platform: 'anthropic',
    type: 'setup-token',
    status: 'available',
    updated_at: null,
    stale: false,
    windows: [],
    last_24h: null,
    last_7d: null,
    ...over
  }
}

/** A fresh store set with a signed-in user (the shell only checks while signed in). */
function signedIn(): Pinia {
  const pinia = createPinia()
  setActivePinia(pinia)
  const auth = useAuthStore()
  auth.token = 'token'
  auth.user = { id: 5, email: 'a@b.c' } as User
  return pinia
}

async function mountShell(pinia: Pinia = signedIn()) {
  i18n.global.locale.value = 'zh'
  const w = mount(AppShell, { global: { plugins: [pinia, i18n], stubs: { RouterLink: RouterLinkStub } } })
  await flushPromises()
  return w
}

const navTargets = (w: Awaited<ReturnType<typeof mountShell>>) => w.findAllComponents(RouterLinkStub).map((l) => l.props('to'))
const later = (ms: number) => vi.setSystemTime(Date.now() + ms)

async function focus(): Promise<void> {
  window.dispatchEvent(new Event('focus'))
  await flushPromises()
}

enableAutoUnmount(afterEach)

describe('AppShell hosted-account entry', () => {
  beforeEach(() => {
    hosted.listOwnedAccounts.mockReset()
    hosted.selfOnboardConfig.mockReset().mockRejectedValue({ status: 404 })
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-11-16T04:00:00Z'))
    useRoute().fullPath = '/dashboard'
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('is hidden when the user owns no hosted account', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([])
    const w = await mountShell()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(1)
    expect(navTargets(w)).not.toContain('/hosted')
    expect(navTargets(w)).toContain('/usage')
  })

  it('is shown to a user without accounts while self-onboarding is open', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([])
    hosted.selfOnboardConfig.mockResolvedValue({ enabled: true, exchange_proxy_configured: true, max_pending: 3 })
    const w = await mountShell()
    expect(navTargets(w)).toContain('/hosted')
  })

  it('stays hidden for a user without accounts while self-onboarding is off', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([])
    hosted.selfOnboardConfig.mockResolvedValue({ enabled: false, exchange_proxy_configured: true, max_pending: 3 })
    const w = await mountShell()
    expect(navTargets(w)).not.toContain('/hosted')
  })

  it('is shown when there is at least one account, current or past', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([account({ current: false, status: null, type: null })])
    const w = await mountShell()
    expect(navTargets(w)).toContain('/hosted')
    expect(w.text()).toContain('我的托管号')
  })

  it('stays hidden (and the shell still renders) when the lookup fails', async () => {
    hosted.listOwnedAccounts.mockRejectedValue({ status: 500, message: 'boom' })
    const w = await mountShell()
    expect(navTargets(w)).not.toContain('/hosted')
    expect(navTargets(w)).toContain('/dashboard')
  })

  it('does not refetch on every shell mount within the TTL', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([account()])
    const pinia = signedIn()
    const opts = { global: { plugins: [pinia, i18n], stubs: { RouterLink: RouterLinkStub } } }
    mount(AppShell, opts).unmount()
    await flushPromises()
    const again = mount(AppShell, opts)
    await flushPromises()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(1)
    expect(navTargets(again)).toContain('/hosted')
  })

  it('makes no extra request for focus, visibility or navigation spam within the TTL', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([])
    await mountShell()
    for (let i = 0; i < 5; i++) await focus()
    document.dispatchEvent(new Event('visibilitychange'))
    useRoute().fullPath = '/keys'
    await flushPromises()
    useRoute().fullPath = '/usage'
    await flushPromises()
    later(HOSTED_TTL_MS - 1000)
    await focus()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(1)
  })

  it('shows an account assigned after sign-in once the TTL has passed (focus)', async () => {
    hosted.listOwnedAccounts.mockResolvedValueOnce([])
    const w = await mountShell()
    expect(navTargets(w)).not.toContain('/hosted')

    hosted.listOwnedAccounts.mockResolvedValueOnce([account()])
    later(HOSTED_TTL_MS)
    await focus()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(2)
    expect(navTargets(w)).toContain('/hosted')
  })

  it('re-checks on navigation and when the tab becomes visible again', async () => {
    hosted.listOwnedAccounts.mockResolvedValueOnce([])
    const w = await mountShell()

    hosted.listOwnedAccounts.mockResolvedValueOnce([account()])
    later(HOSTED_TTL_MS)
    useRoute().fullPath = '/keys'
    await flushPromises()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(2)
    expect(navTargets(w)).toContain('/hosted')

    const visibility = vi.spyOn(document, 'visibilityState', 'get')
    try {
      hosted.listOwnedAccounts.mockResolvedValueOnce([])
      later(HOSTED_TTL_MS)
      visibility.mockReturnValue('hidden')
      document.dispatchEvent(new Event('visibilitychange'))
      await flushPromises()
      expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(2)
      visibility.mockReturnValue('visible')
      document.dispatchEvent(new Event('visibilitychange'))
      await flushPromises()
      expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(3)
      expect(navTargets(w)).not.toContain('/hosted')
    } finally {
      visibility.mockRestore()
    }
  })

  it('retries a failed lookup after the backoff and then shows the entry', async () => {
    hosted.listOwnedAccounts.mockRejectedValueOnce({ status: 0, code: 'NETWORK', message: 'network' })
    const w = await mountShell()
    expect(navTargets(w)).not.toContain('/hosted')

    hosted.listOwnedAccounts.mockResolvedValueOnce([account()])
    later(HOSTED_RETRY_MS - 1000)
    await focus()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(1)

    later(1000)
    await focus()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(2)
    expect(navTargets(w)).toContain('/hosted')
  })

  it('keeps a shown entry when a later re-check fails', async () => {
    hosted.listOwnedAccounts.mockResolvedValueOnce([account()])
    const w = await mountShell()
    hosted.listOwnedAccounts.mockRejectedValueOnce({ status: 503, message: 'busy' })
    later(HOSTED_TTL_MS)
    await focus()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(2)
    expect(navTargets(w)).toContain('/hosted')

    // Earlier success + latest failure: the failure backoff applies, not the TTL.
    hosted.listOwnedAccounts.mockResolvedValueOnce([account()])
    later(HOSTED_RETRY_MS - 1)
    await focus()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(2)
    later(1)
    await focus()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(3)
  })

  it('does not check after sign-out or once the shell is gone', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([account()])
    const w = await mountShell()
    await useAuthStore().logout()
    later(HOSTED_TTL_MS)
    await focus()
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(1)

    const auth = useAuthStore()
    auth.token = 'token'
    auth.user = { id: 6, email: 'c@d.e' } as User
    w.unmount()
    later(HOSTED_TTL_MS)
    await focus()
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(1)
  })

  it('does not check while signed out', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([account()])
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = await mountShell(pinia)
    await focus()
    expect(hosted.listOwnedAccounts).not.toHaveBeenCalled()
    expect(navTargets(w)).not.toContain('/hosted')
  })

  it('forgets the list on sign-out so the next account starts hidden', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([account()])
    const w = await mountShell()
    expect(navTargets(w)).toContain('/hosted')
    await useAuthStore().logout()
    await flushPromises()
    expect(useHostedStore().accounts).toEqual([])
    expect(useHostedStore().loaded).toBe(false)
    expect(navTargets(w)).not.toContain('/hosted')
  })

  it('drops a list that arrives after the session changed', async () => {
    let resolve: (v: OwnedAccount[]) => void = () => undefined
    hosted.listOwnedAccounts.mockReturnValue(new Promise<OwnedAccount[]>((r) => (resolve = r)))
    const w = await mountShell()
    useHostedStore().reset()
    resolve([account()])
    await flushPromises()
    expect(navTargets(w)).not.toContain('/hosted')
  })
})
