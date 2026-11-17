import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount, RouterLinkStub } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { i18n } from '@/i18n'
import type { OwnedAccount } from '@/api'
import type { PublicSettings, User, UserInviteOverview } from '@/types'

const api = vi.hoisted(() => ({
  invites: { overview: vi.fn(), create: vi.fn(), revoke: vi.fn() },
  usage: {
    dashboard: vi.fn(),
    stats: vi.fn(),
    query: vi.fn()
  },
  keys: { list: vi.fn() },
  me: vi.fn()
}))
vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return {
    ...actual,
    invitesApi: api.invites,
    usageApi: { ...actual.usageApi, ...api.usage },
    keysApi: { ...actual.keysApi, list: api.keys.list },
    authApi: { ...actual.authApi, me: api.me }
  }
})

import OverviewView from '../OverviewView.vue'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { useHostedStore } from '@/stores/hosted'

const USER = { id: 5, email: 'a@b.c', username: 'a', balance: 10 } as User

const invites = (over: Partial<UserInviteOverview> = {}): UserInviteOverview => ({
  enabled: true,
  eligible: true,
  max_unused: 5,
  max_total: 0,
  unused: 0,
  invited: 0,
  daily_limit_reached: false,
  max_per_week: 5,
  created_this_week: 2,
  weekly_limit_reached: false,
  can_create: true,
  codes: [],
  pagination: { total: 0, page: 1, page_size: 1, pages: 1 },
  ...over
})

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

async function mountOverview(opts: { inviteSetting?: boolean; owned?: OwnedAccount[]; locale?: 'zh' | 'en' } = {}) {
  const pinia = createPinia()
  setActivePinia(pinia)
  useAppStore().settings = { user_invite_enabled: opts.inviteSetting ?? true } as PublicSettings
  const auth = useAuthStore()
  auth.token = 'token'
  auth.user = USER
  useHostedStore().accounts = opts.owned ?? []
  i18n.global.locale.value = opts.locale ?? 'zh'
  const w = mount(OverviewView, { global: { plugins: [pinia, i18n], stubs: { RouterLink: RouterLinkStub, ChargeTable: true } } })
  await flushPromises()
  return w
}

const links = (w: Awaited<ReturnType<typeof mountOverview>>, testid: string) =>
  w.find(`[data-testid=${testid}]`).findAllComponents(RouterLinkStub).map((l) => l.props('to'))

enableAutoUnmount(afterEach)

describe('OverviewView account service lines', () => {
  beforeEach(() => {
    api.invites.overview.mockReset().mockResolvedValue(invites())
    api.usage.dashboard.mockReset().mockResolvedValue({ today_actual_cost: 0, today_requests: 0 })
    api.usage.stats.mockReset().mockResolvedValue({ total_actual_cost: 0 })
    api.usage.query.mockReset().mockResolvedValue({ items: [], total: 0, page: 1, page_size: 8, pages: 0 })
    api.keys.list.mockReset().mockResolvedValue({ items: [], total: 1, page: 1, page_size: 1, pages: 1 })
    api.me.mockReset().mockResolvedValue(USER)
  })

  it('shows nothing extra without hosted accounts or invitations', async () => {
    const w = await mountOverview({ inviteSetting: false })
    expect(w.find('[data-testid=overview-services]').exists()).toBe(false)
    expect(api.invites.overview).not.toHaveBeenCalled()
  })

  it('links current hosted accounts to /hosted', async () => {
    const w = await mountOverview({ inviteSetting: false, owned: [account(), account({ account_id: 13 })] })
    const row = w.find('[data-testid=overview-hosted]')
    expect(row.text()).toContain('当前 2 个')
    expect(row.text()).toContain('查看额度与用量')
    expect(links(w, 'overview-hosted')).toEqual(['/hosted'])
  })

  it('still links past-only hosted accounts, worded as history', async () => {
    const w = await mountOverview({ inviteSetting: false, owned: [account({ current: false, status: null })] })
    expect(w.find('[data-testid=overview-hosted]').text()).toContain('仅有历史记录')
    expect(w.find('[data-testid=overview-hosted]').text()).toContain('查看历史用量')
    expect(links(w, 'overview-hosted')).toEqual(['/hosted'])
  })

  it('follows the hosted store when the shell finds an account later', async () => {
    const w = await mountOverview({ inviteSetting: false })
    expect(w.find('[data-testid=overview-hosted]').exists()).toBe(false)
    useHostedStore().accounts = [account()]
    await flushPromises()
    expect(w.find('[data-testid=overview-hosted]').exists()).toBe(true)
  })

  it('asks for one invitation page and shows what is left in the rolling 7 days', async () => {
    const w = await mountOverview()
    expect(api.invites.overview).toHaveBeenCalledWith(1, 1)
    expect(w.find('[data-testid=overview-invites]').text()).toContain('近 7 天还可生成 3 个邀请码')
    expect(links(w, 'overview-invites')).toEqual(['/invites'])
  })

  it('without a weekly cap says codes can be generated', async () => {
    api.invites.overview.mockResolvedValue(invites({ max_per_week: 0, created_this_week: 0 }))
    const w = await mountOverview()
    expect(w.find('[data-testid=overview-invites]').text()).toContain('可以生成邀请码')
    expect(links(w, 'overview-invites')).toEqual(['/invites'])
  })

  it('says the week is used up instead of "0 left"', async () => {
    api.invites.overview.mockResolvedValue(invites({ created_this_week: 5, weekly_limit_reached: true, can_create: false }))
    const w = await mountOverview()
    const text = w.find('[data-testid=overview-invites]').text()
    expect(text).toContain('近 7 天的邀请码已用完')
    expect(text).not.toContain('0 个')
    expect(links(w, 'overview-invites')).toEqual(['/invites'])
  })

  it('does not promise a code when another limit blocks it', async () => {
    api.invites.overview.mockResolvedValue(invites({ unused: 5, can_create: false }))
    const w = await mountOverview()
    expect(w.find('[data-testid=overview-invites]').text()).toContain('现在不能生成新的邀请码')
  })

  it('sends users who have not topped up to /billing', async () => {
    api.invites.overview.mockResolvedValue(invites({ eligible: false, can_create: false }))
    const w = await mountOverview()
    expect(w.find('[data-testid=overview-invites]').text()).toContain('充值或兑换一次后即可邀请好友')
    expect(links(w, 'overview-invites')).toEqual(['/billing'])
  })

  it('hides the invitation line when the server reports invites off or the request fails', async () => {
    api.invites.overview.mockResolvedValue(invites({ enabled: false }))
    expect((await mountOverview()).find('[data-testid=overview-invites]').exists()).toBe(false)
    api.invites.overview.mockRejectedValue(new Error('boom'))
    const failed = await mountOverview()
    expect(failed.find('[data-testid=overview-invites]').exists()).toBe(false)
    expect(failed.find('[role=alert]').exists()).toBe(false)
  })

  it('holds the invitation row while the request is pending, then fills it', async () => {
    let resolve: (v: UserInviteOverview) => void = () => undefined
    api.invites.overview.mockReturnValue(new Promise<UserInviteOverview>((r) => (resolve = r)))
    const w = await mountOverview()
    const row = w.find('[data-testid=overview-invites]')
    expect(row.find('.skeleton').exists()).toBe(true)
    expect(row.find('dd').attributes('aria-busy')).toBe('true')
    expect(row.find('.skeleton .sr-only').text()).toBe('正在加载邀请情况…')
    resolve(invites())
    await flushPromises()
    expect(w.find('[data-testid=overview-invites] .skeleton').exists()).toBe(false)
    expect(w.find('[data-testid=overview-invites]').text()).toContain('近 7 天还可生成 3 个邀请码')
  })

  it('follows the public invite switch after mount', async () => {
    const w = await mountOverview({ inviteSetting: false })
    expect(api.invites.overview).not.toHaveBeenCalled()
    const app = useAppStore()
    app.settings = { user_invite_enabled: true } as PublicSettings
    await flushPromises()
    expect(api.invites.overview).toHaveBeenCalledTimes(1)
    expect(w.find('[data-testid=overview-invites]').exists()).toBe(true)
    app.settings = { user_invite_enabled: false } as PublicSettings
    await flushPromises()
    expect(w.find('[data-testid=overview-invites]').exists()).toBe(false)
  })

  it('drops an answer that arrives after invites were switched off', async () => {
    let resolve: (v: UserInviteOverview) => void = () => undefined
    api.invites.overview.mockReturnValue(new Promise<UserInviteOverview>((r) => (resolve = r)))
    const w = await mountOverview()
    useAppStore().settings = { user_invite_enabled: false } as PublicSettings
    await flushPromises()
    resolve(invites())
    await flushPromises()
    expect(w.find('[data-testid=overview-invites]').exists()).toBe(false)
  })

  it('shows both lines together, in English too', async () => {
    const w = await mountOverview({ owned: [account()], locale: 'en' })
    const services = w.find('[data-testid=overview-services]')
    expect(services.text()).toContain('1 current')
    expect(services.text()).toContain('3 more in the last 7 days')
    i18n.global.locale.value = 'zh'
  })
})
