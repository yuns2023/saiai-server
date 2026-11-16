import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount, RouterLinkStub } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { i18n } from '@/i18n'
import type { OwnedAccount } from '@/api'

const hosted = vi.hoisted(() => ({ listOwnedAccounts: vi.fn(), listOwnedAccountUsage: vi.fn() }))
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
  return { ...actual, useRoute: () => ({ path: '/dashboard', fullPath: '/dashboard' }), useRouter: () => ({ replace: vi.fn() }) }
})

import AppShell from '../AppShell.vue'
import { useAuthStore } from '@/stores/auth'
import { useHostedStore } from '@/stores/hosted'

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

async function mountShell() {
  const pinia = createPinia()
  setActivePinia(pinia)
  i18n.global.locale.value = 'zh'
  const w = mount(AppShell, { global: { plugins: [pinia, i18n], stubs: { RouterLink: RouterLinkStub } } })
  await flushPromises()
  return w
}

const navTargets = (w: Awaited<ReturnType<typeof mountShell>>) => w.findAllComponents(RouterLinkStub).map((l) => l.props('to'))

enableAutoUnmount(afterEach)

describe('AppShell hosted-account entry', () => {
  beforeEach(() => {
    hosted.listOwnedAccounts.mockReset()
  })

  it('is hidden when the user owns no hosted account', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([])
    const w = await mountShell()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(1)
    expect(navTargets(w)).not.toContain('/hosted')
    expect(navTargets(w)).toContain('/usage')
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

  it('is fetched once per session, not on every shell mount', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([account()])
    const pinia = createPinia()
    setActivePinia(pinia)
    const opts = { global: { plugins: [pinia, i18n], stubs: { RouterLink: RouterLinkStub } } }
    mount(AppShell, opts).unmount()
    await flushPromises()
    const again = mount(AppShell, opts)
    await flushPromises()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(1)
    expect(navTargets(again)).toContain('/hosted')
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
