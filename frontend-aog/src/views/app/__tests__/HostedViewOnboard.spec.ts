import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { i18n } from '@/i18n'
import type { HostedApplication, OwnedAccount, SelfOnboardConfig } from '@/api'

const hosted = vi.hoisted(() => ({
  listOwnedAccounts: vi.fn(),
  listOwnedAccountUsage: vi.fn(),
  selfOnboardConfig: vi.fn(),
  listApplications: vi.fn(),
  cancelApplication: vi.fn(),
  setOwnerState: vi.fn(),
  createAuthUrl: vi.fn(),
  submitApplication: vi.fn()
}))
vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return { ...actual, hostedApi: hosted }
})

import HostedView from '../HostedView.vue'

function account(over: Partial<OwnedAccount> = {}): OwnedAccount {
  return {
    account_id: 12,
    current: true,
    display_name: '我的 Max 号',
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

function application(over: Partial<HostedApplication> = {}): HostedApplication {
  return {
    id: 1,
    status: 'pending',
    display_name: '申请 1',
    upstream_email: 'me@example.com',
    proxy_display: 'socks5h://proxy.example.com:1080',
    reject_reason: '',
    account_id: null,
    created_at: '2026-11-18T01:00:00Z',
    reviewed_at: null,
    ...over
  }
}

const ON: SelfOnboardConfig = { enabled: true, exchange_proxy_configured: true, max_pending: 3 }
const OFF: SelfOnboardConfig = { enabled: false, exchange_proxy_configured: false, max_pending: 3 }

async function mountView(opts: { accounts?: OwnedAccount[]; apps?: HostedApplication[]; config?: SelfOnboardConfig } = {}) {
  hosted.listOwnedAccounts.mockResolvedValue(opts.accounts ?? [])
  hosted.listApplications.mockResolvedValue(opts.apps ?? [])
  hosted.selfOnboardConfig.mockResolvedValue(opts.config ?? ON)
  setActivePinia(createPinia())
  i18n.global.locale.value = 'zh'
  const w = mount(HostedView, { global: { plugins: [i18n] }, attachTo: document.body })
  await flushPromises()
  return w
}

enableAutoUnmount(afterEach)

describe('HostedView self-onboarding', () => {
  beforeEach(() => {
    Object.values(hosted).forEach((f) => f.mockReset())
    hosted.listOwnedAccountUsage.mockResolvedValue({ items: [], next_cursor: null })
    // jsdom has no <dialog> methods.
    HTMLDialogElement.prototype.showModal ??= function (this: HTMLDialogElement) {
      this.open = true
    }
    HTMLDialogElement.prototype.close ??= function (this: HTMLDialogElement) {
      this.open = false
    }
  })

  it('switch on, no accounts: explains the empty list and shows the form', async () => {
    const w = await mountView()
    expect(w.get('[data-testid=hosted-empty]').text()).toContain('可以在下方提交自己的 Claude 号')
    expect(w.find('[data-testid=onboard]').exists()).toBe(true)
    expect(w.get('[data-testid=onboard-fee]').text()).toContain('站点设置')
    expect(w.find('[data-testid=apps]').exists()).toBe(false)
  })

  it('switch off: no form, the old empty text', async () => {
    const w = await mountView({ config: OFF })
    expect(w.find('[data-testid=onboard]').exists()).toBe(false)
    expect(w.get('[data-testid=hosted-empty]').text()).toContain('管理员把上游账号归属到你之后')
  })

  it('a server without self-onboarding counts as off', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([])
    hosted.listApplications.mockRejectedValue({ status: 404 })
    hosted.selfOnboardConfig.mockRejectedValue({ status: 404 })
    setActivePinia(createPinia())
    const w = mount(HostedView, { global: { plugins: [i18n] } })
    await flushPromises()
    expect(w.find('[data-testid=onboard]').exists()).toBe(false)
    expect(w.find('[data-testid=apps-error]').exists()).toBe(false)
  })

  it('lists applications with status, proxy and reject reason; cancels a pending one after confirming', async () => {
    const apps = [
      application({ id: 1, status: 'pending', created_at: '2026-11-18T03:00:00Z' }),
      application({ id: 2, status: 'approved', account_id: 12, proxy_display: '', reviewed_at: '2026-11-17T05:00:00Z', created_at: '2026-11-17T01:00:00Z' }),
      application({ id: 3, status: 'rejected', reject_reason: '代理连不上', created_at: '2026-11-16T01:00:00Z' }),
      application({ id: 4, status: 'cancelled', created_at: '2026-11-15T01:00:00Z' }),
      application({ id: 5, status: 'expired', created_at: '2026-11-14T01:00:00Z' })
    ]
    const w = await mountView({ apps })
    const rows = w.findAll('[data-testid^=app-]').filter((r) => /^app-\d+$/.test(r.attributes('data-testid') ?? ''))
    expect(rows.map((r) => r.attributes('data-testid'))).toEqual(['app-1', 'app-2', 'app-3', 'app-4', 'app-5'])
    expect(rows.map((r) => r.get('[data-testid=app-status]').text())).toEqual(['待审核', '已通过', '已驳回', '已取消', '已过期'])
    expect(w.get('[data-testid=app-1]').get('[data-testid=app-proxy]').text()).toContain('socks5h://proxy.example.com:1080')
    expect(w.get('[data-testid=app-2]').get('[data-testid=app-proxy]').text()).toContain('使用站点代理')
    expect(w.get('[data-testid=app-3]').get('[data-testid=app-reason]').text()).toBe('驳回原因：代理连不上')
    // Only the pending one can be cancelled.
    expect(w.findAll('[data-testid^=app-cancel-]').map((b) => b.attributes('data-testid'))).toEqual(['app-cancel-1'])

    hosted.cancelApplication.mockResolvedValue(application({ id: 1, status: 'cancelled' }))
    hosted.listApplications.mockResolvedValue([application({ id: 1, status: 'cancelled' })])
    await w.get('[data-testid=app-cancel-1]').trigger('click')
    await flushPromises()
    expect(hosted.cancelApplication).not.toHaveBeenCalled()
    const dialog = document.querySelector('dialog[open]') as HTMLDialogElement
    expect(dialog.textContent).toContain('取消这个申请？')
    const confirm = [...dialog.querySelectorAll('button')].find((b) => b.textContent?.includes('确认取消申请')) as HTMLButtonElement
    confirm.click()
    await flushPromises()
    expect(hosted.cancelApplication).toHaveBeenCalledWith(1)
    expect(w.get('[data-testid=app-1]').get('[data-testid=app-status]').text()).toBe('已取消')
  })

  it('shows the cancelled state right away, even if the refresh is slow', async () => {
    const w = await mountView({ apps: [application({ id: 1, status: 'pending' })] })
    hosted.cancelApplication.mockResolvedValue(application({ id: 1, status: 'cancelled' }))
    hosted.listApplications.mockReturnValue(new Promise(() => undefined))
    await w.get('[data-testid=app-cancel-1]').trigger('click')
    await flushPromises()
    const dialog = document.querySelector('dialog[open]') as HTMLDialogElement
    ;([...dialog.querySelectorAll('button')].find((b) => b.textContent?.includes('确认取消申请')) as HTMLButtonElement).click()
    await flushPromises()
    expect(w.get('[data-testid=app-1]').get('[data-testid=app-status]').text()).toBe('已取消')
    expect(w.find('[data-testid=app-cancel-1]').exists()).toBe(false)
  })

  it('offers a retry when the list fails, also with the switch off', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([])
    hosted.listApplications.mockRejectedValue({ status: 500 })
    hosted.selfOnboardConfig.mockResolvedValue(OFF)
    setActivePinia(createPinia())
    const w = mount(HostedView, { global: { plugins: [i18n] } })
    await flushPromises()
    expect(w.find('[data-testid=apps-error]').exists()).toBe(true)
  })

  it('applications stay visible with the switch off', async () => {
    const w = await mountView({ config: OFF, apps: [application({ status: 'pending' })] })
    expect(w.find('[data-testid=apps]').exists()).toBe(true)
    expect(w.find('[data-testid=app-cancel-1]').exists()).toBe(true)
    expect(w.find('[data-testid=onboard]').exists()).toBe(false)
  })

  it('pauses, resumes and requests take-down (with a confirmation)', async () => {
    const w = await mountView({ accounts: [account({ owner_paused: false, withdraw_requested: false })] })
    const row = () => w.get('[data-testid=hosted-account-12]')
    expect(row().text()).toContain('暂停后不再接新请求，进行中的请求会正常完成')

    hosted.setOwnerState.mockResolvedValueOnce({ id: 12, owner_paused: true, withdraw_requested: false })
    // The server's view after the pause, fetched again by the page.
    hosted.listOwnedAccounts.mockResolvedValue([account({ status: 'paused', owner_paused: true, withdraw_requested: false })])
    const listCalls = hosted.listOwnedAccounts.mock.calls.length
    await row().get('[data-testid=hosted-pause]').trigger('click')
    await flushPromises()
    expect(hosted.setOwnerState).toHaveBeenLastCalledWith(12, 'pause')
    expect(hosted.listOwnedAccounts.mock.calls.length).toBe(listCalls + 1)
    expect(row().get('[data-testid=hosted-owner-paused]').text()).toBe('你已暂停')
    // One clear label: no "已暂停" / "可用" next to it.
    expect(row().find('[data-testid=hosted-status]').exists()).toBe(false)
    expect(row().find('[data-testid=hosted-pause]').exists()).toBe(false)

    hosted.setOwnerState.mockResolvedValueOnce({ id: 12, owner_paused: false, withdraw_requested: false })
    hosted.listOwnedAccounts.mockResolvedValue([account({ owner_paused: false, withdraw_requested: false })])
    await row().get('[data-testid=hosted-resume]').trigger('click')
    await flushPromises()
    expect(hosted.setOwnerState).toHaveBeenLastCalledWith(12, 'resume')
    expect(row().find('[data-testid=hosted-owner-paused]').exists()).toBe(false)
    expect(row().get('[data-testid=hosted-status]').text()).toBe('可用')

    await row().get('[data-testid=hosted-withdraw]').trigger('click')
    await flushPromises()
    expect(hosted.setOwnerState).toHaveBeenCalledTimes(2)
    const dialog = document.querySelector('dialog[open]') as HTMLDialogElement
    expect(dialog.textContent).toContain('不能撤销')
    hosted.setOwnerState.mockResolvedValueOnce({ id: 12, owner_paused: false, withdraw_requested: true })
    hosted.listOwnedAccounts.mockResolvedValue([account({ status: 'paused', owner_paused: false, withdraw_requested: true })])
    const confirm = [...dialog.querySelectorAll('button')].find((b) => b.textContent?.includes('确认申请下架')) as HTMLButtonElement
    confirm.click()
    await flushPromises()
    expect(hosted.setOwnerState).toHaveBeenLastCalledWith(12, 'withdraw')
    expect(row().get('[data-testid=hosted-withdraw-requested]').text()).toBe('已申请下架')
    // Not revocable: no buttons any more.
    expect(row().find('[data-testid=hosted-pause]').exists()).toBe(false)
    expect(row().find('[data-testid=hosted-withdraw]').exists()).toBe(false)
    expect(row().text()).toContain('等待管理员移除')
  })

  it('keeps a real problem status next to the owner label', async () => {
    const w = await mountView({ accounts: [account({ status: 'needs_attention', owner_paused: true, withdraw_requested: false })] })
    expect(w.get('[data-testid=hosted-status]').text()).toBe('需处理')
    expect(w.get('[data-testid=hosted-owner-paused]').text()).toBe('你已暂停')
  })

  it('at max_pending the form says so; a cancel re-enables generating a link', async () => {
    const pending = [1, 2, 3].map((id) => application({ id, status: 'pending', created_at: `2026-11-18T0${id}:00:00Z` }))
    const w = await mountView({ apps: pending })
    expect(w.find('[data-testid=onboard-pending-full]').exists()).toBe(true)
    expect((w.get('[data-testid=onboard-generate]').element as HTMLButtonElement).disabled).toBe(true)

    hosted.cancelApplication.mockResolvedValue(application({ id: 1, status: 'cancelled', created_at: '2026-11-18T01:00:00Z' }))
    hosted.listApplications.mockReturnValue(new Promise(() => undefined))
    await w.get('[data-testid=app-cancel-1]').trigger('click')
    await flushPromises()
    const dialog = document.querySelector('dialog[open]') as HTMLDialogElement
    ;([...dialog.querySelectorAll('button')].find((b) => b.textContent?.includes('确认取消申请')) as HTMLButtonElement).click()
    await flushPromises()
    expect(w.find('[data-testid=onboard-pending-full]').exists()).toBe(false)
    expect((w.get('[data-testid=onboard-generate]').element as HTMLButtonElement).disabled).toBe(false)
  })

  it('a failed applications read is not "0 pending": no limit message, retry offered', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([])
    hosted.listApplications.mockRejectedValue({ status: 500 })
    hosted.selfOnboardConfig.mockResolvedValue(ON)
    setActivePinia(createPinia())
    const w = mount(HostedView, { global: { plugins: [i18n] } })
    await flushPromises()
    expect(w.find('[data-testid=apps-error]').exists()).toBe(true)
    expect(w.find('[data-testid=onboard-pending-full]').exists()).toBe(false)
  })

  it('shows no owner controls for servers without the owner flags or for past accounts', async () => {
    const w = await mountView({ accounts: [account(), account({ account_id: 7, current: false, status: null, owner_paused: false, withdraw_requested: false })] })
    expect(w.find('[data-testid=hosted-owner-12]').exists()).toBe(false)
    expect(w.find('[data-testid=hosted-owner-7]').exists()).toBe(false)
  })
})
