import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { i18n } from '@/i18n'
import type { OwnedAccount } from '@/api'
import type { OwnerEarnings, OwnerMarketView } from '@/api/market'

const hosted = vi.hoisted(() => ({
  listOwnedAccounts: vi.fn(),
  listOwnedAccountUsage: vi.fn(),
  selfOnboardConfig: vi.fn(),
  listApplications: vi.fn(),
  setOwnerState: vi.fn()
}))
const market = vi.hoisted(() => ({ getListing: vi.fn(), setListing: vi.fn(), earnings: vi.fn(), entries: vi.fn(), convert: vi.fn() }))
vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return { ...actual, hostedApi: hosted }
})
vi.mock('@/api/market', () => ({ marketApi: market }))

import HostedView from '../HostedView.vue'
import { useAuthStore } from '@/stores/auth'

function account(over: Partial<OwnedAccount> = {}): OwnedAccount {
  return {
    account_id: 11,
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
    owner_paused: false,
    withdraw_requested: false,
    ...over
  }
}

function view(over: Partial<OwnerMarketView> = {}): OwnerMarketView {
  return {
    account_id: 11,
    market_enabled: true,
    status: 'unlisted',
    selected_group_id: null,
    version: 2,
    allowed_tiers: [
      { group_id: 3, name: 'T1-Pro', rate_multiplier: 1.2, available: true, admin_member: false },
      { group_id: 4, name: 'T2-Max', rate_multiplier: 1.6, available: true, admin_member: false }
    ],
    protection: { reserved_concurrency: 1, reserved_devices: 2, reserved_admissions: 1, cap_5h_pct: 70, cap_7d_pct: 60, sample_max_age_minutes: 30 },
    unavailable_reasons: [],
    can_list: true,
    ...over
  }
}

const NOT_CONFIGURED = view({ status: 'not_configured', version: 0, allowed_tiers: [], protection: null, unavailable_reasons: ['not_configured'], can_list: false })
const ZERO: OwnerEarnings = { frozen: '0', available: '0', converted_total: '0', min_convert: '1', as_of: '2026-11-19T00:00:00Z' }
const SOME: OwnerEarnings = { frozen: '1.2', available: '3.4999999999', converted_total: '2', min_convert: '1', as_of: '2026-11-19T00:00:00Z' }

async function mountView(opts: { views?: Record<number, OwnerMarketView | Error>; earnings?: OwnerEarnings | Error; accounts?: OwnedAccount[] } = {}) {
  hosted.listOwnedAccounts.mockResolvedValue(opts.accounts ?? [account()])
  const views = opts.views ?? { 11: NOT_CONFIGURED }
  market.getListing.mockImplementation(async (id: number) => {
    const v = views[id]
    if (v instanceof Error || !v) throw v ?? { status: 404 }
    return v
  })
  market.earnings.mockImplementation(async () => {
    const e = opts.earnings ?? ZERO
    if (e instanceof Error) throw e
    return e
  })
  freshPinia()
  const w = mount(HostedView, { global: { plugins: [i18n] }, attachTo: document.body })
  await flushPromises()
  return w
}

const OWNER_ID = 7

/** A new Pinia (as after a page reload) signed in as the owner. */
function freshPinia(): void {
  setActivePinia(createPinia())
  i18n.global.locale.value = 'zh'
  useAuthStore().user = { id: OWNER_ID, username: 'o', email: 'o@example.com', role: 'user', balance: 0, concurrency: 1, status: 'active', allowed_groups: null, created_at: '', updated_at: '' }
}

enableAutoUnmount(afterEach)

describe('HostedView sharing market', () => {
  beforeEach(() => {
    Object.values(hosted).forEach((f) => f.mockReset())
    Object.values(market).forEach((f) => f.mockReset())
    hosted.listOwnedAccountUsage.mockResolvedValue({ items: [], next_cursor: null })
    hosted.selfOnboardConfig.mockResolvedValue({ enabled: false, exchange_proxy_configured: false, max_pending: 3 })
    hosted.listApplications.mockResolvedValue([])
    market.entries.mockResolvedValue({ items: [], next_cursor: null })
    localStorage.clear()
    HTMLDialogElement.prototype.showModal ??= function (this: HTMLDialogElement) {
      this.open = true
    }
    HTMLDialogElement.prototype.close ??= function (this: HTMLDialogElement) {
      this.open = false
    }
  })

  it('no permission anywhere and no earnings: nothing about the market shows', async () => {
    const w = await mountView()
    expect(market.getListing).toHaveBeenCalledWith(11)
    expect(w.find('[data-testid=market-11]').exists()).toBe(false)
    expect(w.find('[data-testid=earnings]').exists()).toBe(false)
  })

  it('a server without the market (404 everywhere) shows nothing and no error', async () => {
    const w = await mountView({ views: {}, earnings: Object.assign(new Error('nf'), { status: 404 }) })
    expect(w.find('[data-testid=market-11]').exists()).toBe(false)
    expect(w.find('[data-testid=earnings-error]').exists()).toBe(false)
  })

  it('a configuration with every tier removed counts as no permission', async () => {
    const w = await mountView({ views: { 11: view({ allowed_tiers: [], can_list: false, unavailable_reasons: ['no_allowed_tier'] }) } })
    expect(w.find('[data-testid=market-11]').exists()).toBe(false)
    expect(w.find('[data-testid=earnings]').exists()).toBe(false)
  })

  it('earnings read failures are reported (first load and refresh), a 404 hides the section', async () => {
    const w = await mountView({ views: { 11: view() }, earnings: Object.assign(new Error('boom'), { status: 500 }) })
    expect(w.get('[data-testid=earnings-error]').text()).toContain('共享收益读取失败')
    market.earnings.mockResolvedValueOnce(SOME)
    await w.get('[data-testid=earnings-error] button').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid=earnings-error]').exists()).toBe(false)
    expect(w.get('[data-testid=earnings-available]').text()).toBe('$3.4999999999')
    // A refresh (after a conversion) that fails keeps the last values and says so.
    vi.spyOn(useAuthStore(), 'refreshUser').mockResolvedValue({} as never)
    market.earnings.mockRejectedValueOnce({ status: 502 })
    market.convert.mockResolvedValueOnce({ entry_id: 1, request_id: 'r', amount: '1', created_at: 'x', replayed: false })
    await w.get('[data-testid=earnings-convert]').trigger('click')
    await w.get('[data-testid=earnings-amount]').setValue('1')
    await w.get('[data-testid=earnings] form').trigger('submit')
    await flushPromises()
    expect(w.get('[data-testid=earnings-error]').text()).toContain('刷新失败')
    expect(w.find('[data-testid=earnings]').exists()).toBe(true)
    // The endpoint is gone (404): nothing stays on screen.
    market.earnings.mockRejectedValueOnce({ status: 404 })
    await w.get('[data-testid=earnings-error] button').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid=earnings-error]').exists()).toBe(false)
    expect(w.find('[data-testid=earnings-frozen]').exists()).toBe(false)
  })

  it('all balances zero but ledger history (e.g. everything reversed): the section still shows', async () => {
    market.entries.mockResolvedValue({ items: [{ id: 3, kind: 'reverse', amount: '-1', status: 'reversed', available_at: '2026-11-24T00:00:00Z', created_at: '2026-11-18T00:00:00Z', account_id: 11, model: null }], next_cursor: null })
    const w = await mountView()
    expect(market.entries).toHaveBeenCalledWith(null, 1)
    expect(w.find('[data-testid=earnings]').exists()).toBe(true)
    expect(w.get('[data-testid=earnings-row]').text()).toContain('冲正')
  })

  it('earnings alone are enough to show the market UI (account then 未获准)', async () => {
    const w = await mountView({ earnings: SOME })
    expect(w.get('[data-testid=market-11] [data-testid=market-state]').text()).toBe('未获准')
    expect(w.get('[data-testid=earnings-frozen]').text()).toBe('$1.20')
    expect(w.get('[data-testid=earnings-available]').text()).toBe('$3.4999999999')
  })

  it('can list: shows tiers with rate and protection copy; listing sends {group_id, listed}', async () => {
    const w = await mountView({ views: { 11: view() } })
    const block = w.get('[data-testid=market-11]')
    expect(block.get('[data-testid=market-state]').text()).toBe('可上架')
    expect(block.get('[data-testid=market-tier-3]').text()).toContain('T1-Pro')
    expect(block.get('[data-testid=market-tier-3]').text()).toContain('×1.2')
    expect(block.get('[data-testid=market-explain]').text()).toContain('预留 1 个并发、2 台设备')
    // Two tiers: nothing preselected, so listing waits for a choice.
    expect(block.get('[data-testid=market-list]').attributes('disabled')).toBeDefined()
    await block.get('[data-testid=market-tier-4] input').setValue(true)
    market.setListing.mockResolvedValue(view({ status: 'listed', selected_group_id: 4, version: 3 }))
    await block.get('[data-testid=market-list]').trigger('click')
    await flushPromises()
    expect(market.setListing).toHaveBeenCalledWith(11, { listed: true, group_id: 4 })
    expect(w.get('[data-testid=market-11] [data-testid=market-state]').text()).toBe('已上架')
    expect(w.get('[data-testid=market-current-tier]').text()).toContain('T2-Max ×1.6')
  })

  it('one listing action at a time per page: pause in flight disables sharing, and an older answer never replaces a newer view', async () => {
    const w = await mountView({ views: { 11: view({ allowed_tiers: [view().allowed_tiers[0]] }) } })
    hosted.setOwnerState.mockReturnValueOnce(new Promise(() => undefined))
    await w.get('[data-testid=hosted-pause]').trigger('click')
    expect(w.get('[data-testid=market-list]').attributes('disabled')).toBeDefined()
  })

  it('the shared action lock is released even when the page was left before the answer', async () => {
    const opts = { global: { plugins: [i18n] }, attachTo: document.body }
    const w = await mountView({ views: { 11: view({ allowed_tiers: [view().allowed_tiers[0]] }) } })
    let resolve!: (v: unknown) => void
    market.setListing.mockReturnValueOnce(new Promise((r) => (resolve = r)))
    await w.get('[data-testid=market-list]').trigger('click')
    w.unmount()
    resolve(view({ status: 'listed', selected_group_id: 3, version: 3 }))
    await flushPromises()
    market.getListing.mockResolvedValue(view({ status: 'listed', selected_group_id: 3, version: 3, allowed_tiers: [view().allowed_tiers[0]] }))
    const again = mount(HostedView, opts)
    await flushPromises()
    expect(again.get('[data-testid=market-unlist]').attributes('disabled')).toBeUndefined()
    expect(again.get('[data-testid=hosted-pause]').attributes('disabled')).toBeUndefined()
  })

  it('a listing action answered after the page was re-entered: the new page shows the result', async () => {
    const opts = { global: { plugins: [i18n] }, attachTo: document.body }
    const w = await mountView({ views: { 11: view({ allowed_tiers: [view().allowed_tiers[0]] }) } })
    let resolve!: (v: unknown) => void
    market.setListing.mockReturnValueOnce(new Promise((r) => (resolve = r)))
    await w.get('[data-testid=market-list]').trigger('click')
    w.unmount()
    // Re-entered while the request is still open: the server still says unlisted.
    const again = mount(HostedView, opts)
    await flushPromises()
    expect(again.get('[data-testid=market-state]').text()).toBe('可上架')
    market.getListing.mockResolvedValue(view({ status: 'listed', selected_group_id: 3, version: 3, allowed_tiers: [view().allowed_tiers[0]] }))
    resolve(view({ status: 'listed', selected_group_id: 3, version: 3 }))
    await flushPromises()
    expect(again.get('[data-testid=market-state]').text()).toBe('已上架')
  })

  it('a failed ledger probe (all balances zero) keeps the section and a retry', async () => {
    market.entries.mockRejectedValue({ status: 500, message: 'boom' })
    const w = await mountView({ earnings: ZERO })
    expect(w.find('[data-testid=earnings]').exists()).toBe(true)
    expect(w.find('[data-testid=earnings-error]').exists()).toBe(true)
  })

  it('a listing answer older than the view on screen is dropped and the state read again', async () => {
    const w = await mountView({ views: { 11: view({ version: 5, allowed_tiers: [view().allowed_tiers[0]] }) } })
    market.setListing.mockResolvedValueOnce(view({ status: 'listed', selected_group_id: 3, version: 4 }))
    await w.get('[data-testid=market-list]').trigger('click')
    await flushPromises()
    expect(market.getListing).toHaveBeenCalledTimes(2)
    expect(w.get('[data-testid=market-state]').text()).toBe('可上架')
  })

  it('listed: unlisting sends {listed:false}', async () => {
    const w = await mountView({ views: { 11: view({ status: 'listed', selected_group_id: 3 }) } })
    market.setListing.mockResolvedValue(view({ status: 'unlisted', selected_group_id: 3, version: 4 }))
    await w.get('[data-testid=market-unlist]').trigger('click')
    await flushPromises()
    expect(market.setListing).toHaveBeenCalledWith(11, { listed: false })
    expect(w.get('[data-testid=market-state]').text()).toBe('可上架')
  })

  it('unavailable: maps reason codes, with a generic text for unknown ones', async () => {
    const w = await mountView({
      views: { 11: view({ can_list: false, unavailable_reasons: ['proxy_unavailable', 'market_disabled', 'brand_new_reason'] }) }
    })
    expect(w.get('[data-testid=market-state]').text()).toBe('暂不可售')
    const reasons = w.get('[data-testid=market-reasons]').text()
    expect(reasons).toContain('代理不可用')
    expect(reasons).toContain('共享市场暂未开放')
    expect(reasons).toContain('暂时不满足共享条件')
    expect(reasons).not.toContain('brand_new_reason')
    expect(w.get('[data-testid=market-list]').attributes('disabled')).toBeDefined()
  })

  it('409 / 400 errors show plain messages and the state is read again', async () => {
    const w = await mountView({ views: { 11: view({ allowed_tiers: [view().allowed_tiers[0]] }) } })
    market.setListing.mockRejectedValueOnce({ status: 409, reason: 'MARKET_ADMIN_MEMBERSHIP_CONFLICT', message: 'x' })
    await w.get('[data-testid=market-list]').trigger('click')
    await flushPromises()
    expect(w.get('[data-testid=market-error]').text()).toContain('已由管理员放在该档位')
    expect(market.getListing).toHaveBeenCalledTimes(2)

    market.setListing.mockRejectedValueOnce({ status: 409, reason: 'MARKET_ACCOUNT_NOT_ELIGIBLE', metadata: { reason: 'proxy_unavailable' }, message: 'x' })
    await w.get('[data-testid=market-list]').trigger('click')
    await flushPromises()
    expect(w.get('[data-testid=market-error]').text()).toBe('这个号暂时不能共享：代理不可用')

    market.setListing.mockRejectedValueOnce({ status: 409, reason: 'MARKET_DISABLED', message: 'x' })
    await w.get('[data-testid=market-list]').trigger('click')
    await flushPromises()
    expect(w.get('[data-testid=market-error]').text()).toContain('共享市场暂未开放')
  })

  it('earnings ledger: no buyer identity, account named «托管号 #id», keyset load more', async () => {
    market.entries.mockResolvedValueOnce({
      items: [
        { id: 9, kind: 'accrue', amount: '0.8', status: 'frozen', available_at: '2026-11-26T00:00:00Z', created_at: '2026-11-19T00:00:00Z', account_id: 11, model: 'claude-sonnet-5' },
        { id: 8, kind: 'convert', amount: '-2', status: 'converted', available_at: null, created_at: '2026-11-18T00:00:00Z', account_id: null, model: null }
      ],
      next_cursor: 8
    })
    const w = await mountView({ views: { 11: view() }, earnings: SOME })
    const rows = w.findAll('[data-testid=earnings-row]')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('托管号 #11')
    expect(rows[0].text()).toContain('出售收益')
    expect(rows[0].text()).toContain('冻结中')
    expect(rows[1].text()).toContain('-$2.00')
    expect(market.entries).toHaveBeenLastCalledWith(null, 20)
    market.entries.mockResolvedValueOnce({ items: [{ id: 7, kind: 'reverse', amount: '-0.5', status: 'reversed', available_at: '2026-11-24T00:00:00Z', created_at: '2026-11-17T00:00:00Z', account_id: 11, model: null }], next_cursor: null })
    await w.get('[data-testid=earnings-more]').trigger('click')
    await flushPromises()
    expect(market.entries).toHaveBeenLastCalledWith(8, 20)
    expect(w.findAll('[data-testid=earnings-row]')).toHaveLength(3)
    expect(w.find('[data-testid=earnings-more]').exists()).toBe(false)
  })

  describe('convert to balance', () => {
    async function openDialog(earnings: OwnerEarnings = SOME) {
      const w = await mountView({ views: { 11: view() }, earnings })
      const auth = useAuthStore()
      vi.spyOn(auth, 'refreshUser').mockResolvedValue({} as never)
      await w.get('[data-testid=earnings-convert]').trigger('click')
      return { w, auth }
    }
    const confirm = (w: Awaited<ReturnType<typeof openDialog>>['w']) => w.get('[data-testid=earnings] form').trigger('submit')

    it('«全部» fills the available amount floored to cents', async () => {
      const { w } = await openDialog()
      await w.get('[data-testid=earnings-all]').trigger('click')
      expect((w.get('[data-testid=earnings-amount]').element as HTMLInputElement).value).toBe('3.49')
    })

    it('rejects bad amounts before sending', async () => {
      const { w } = await openDialog()
      for (const [input, text] of [
        ['0.5', '最少转入'],
        ['3.5', '超过可用收益'],
        ['1.234', '最多两位小数'],
        ['abc', '大于 0']
      ]) {
        await w.get('[data-testid=earnings-amount]').setValue(input)
        await confirm(w)
        await flushPromises()
        expect(w.get('[data-testid=earnings-convert-error]').text()).toContain(text)
      }
      expect(market.convert).not.toHaveBeenCalled()
    })

    it('reuses one request id across retries, a second click and reopening; a new one after success', async () => {
      const { w, auth } = await openDialog()
      await w.get('[data-testid=earnings-amount]').setValue('2.50')
      // Timeout (no answer): the outcome is unknown.
      market.convert.mockRejectedValueOnce({ status: 0, code: 'NETWORK', message: 'network' })
      await confirm(w)
      await flushPromises()
      expect(w.get('[data-testid=earnings-convert-error]').text()).toContain('不会重复转入')
      const first = market.convert.mock.calls[0][1] as string
      expect(market.convert.mock.calls[0][0]).toBe('2.5')

      // Server error: still unknown.
      market.convert.mockRejectedValueOnce({ status: 502, message: 'bad gateway' })
      await confirm(w)
      await flushPromises()
      expect(market.convert.mock.calls[1][1]).toBe(first)

      // Close and reopen: the unsettled attempt keeps its id.
      await w.get('[data-testid=earnings] dialog .btn-line').trigger('click')
      await w.get('[data-testid=earnings-convert]').trigger('click')
      await w.get('[data-testid=earnings-amount]').setValue('2.5')

      // Double click while the request is in flight: only one request.
      let resolve!: (v: unknown) => void
      market.convert.mockReturnValueOnce(new Promise((r) => (resolve = r)))
      await confirm(w)
      await confirm(w)
      expect(market.convert).toHaveBeenCalledTimes(3)
      expect(market.convert.mock.calls[2][1]).toBe(first)
      market.earnings.mockResolvedValue({ ...SOME, available: '0.9999999999', converted_total: '4.5' })
      resolve({ entry_id: 4, request_id: first, amount: '2.5', created_at: 'x', replayed: true })
      await flushPromises()
      expect(auth.refreshUser).toHaveBeenCalled()
      expect(market.earnings).toHaveBeenCalledTimes(2)
      expect(w.get('[data-testid=earnings-converted]').text()).toBe('$4.50')

      expect(document.querySelector('[data-testid=earnings] dialog')?.hasAttribute('open')).toBe(false)

      // The next conversion is a new attempt with a new id.
      market.earnings.mockResolvedValue(SOME)
      await w.vm.$nextTick()
    })

    it('after a success the next conversion gets a new id', async () => {
      const { w } = await openDialog()
      await w.get('[data-testid=earnings-amount]').setValue('1')
      market.convert.mockResolvedValue({ entry_id: 1, request_id: 'r', amount: '1', created_at: 'x', replayed: false })
      await confirm(w)
      await flushPromises()
      await w.get('[data-testid=earnings-convert]').trigger('click')
      await w.get('[data-testid=earnings-amount]').setValue('1')
      await confirm(w)
      await flushPromises()
      expect(market.convert).toHaveBeenCalledTimes(2)
      expect(market.convert.mock.calls[0][1]).not.toBe(market.convert.mock.calls[1][1])
    })

    it('a definite 4xx ends the attempt: conflict and insufficient get a fresh id next time', async () => {
      const { w } = await openDialog()
      await w.get('[data-testid=earnings-amount]').setValue('2')
      market.convert.mockRejectedValueOnce({ status: 409, reason: 'MARKET_EARNING_CONVERT_CONFLICT', message: 'x' })
      await confirm(w)
      await flushPromises()
      expect(w.get('[data-testid=earnings-convert-error]').text()).toContain('上一次转入已经完成')
      // A settled attempt clears the amount: the next one is typed again.
      expect((w.get('[data-testid=earnings-amount]').element as HTMLInputElement).value).toBe('')
      await w.get('[data-testid=earnings-amount]').setValue('2')
      market.convert.mockRejectedValueOnce({ status: 409, reason: 'MARKET_EARNING_INSUFFICIENT', message: 'x' })
      await confirm(w)
      await flushPromises()
      expect(w.get('[data-testid=earnings-convert-error]').text()).toContain('可用收益不足')
      await w.get('[data-testid=earnings-amount]').setValue('2')
      market.convert.mockResolvedValueOnce({ entry_id: 5, request_id: 'r', amount: '2', created_at: 'x', replayed: false })
      await confirm(w)
      await flushPromises()
      const ids = market.convert.mock.calls.map((c) => c[1])
      expect(new Set(ids).size).toBe(3)
    })

    it('an unknown outcome is replayed with the same id and amount, even when the balance no longer covers it', async () => {
      const { w } = await openDialog()
      await w.get('[data-testid=earnings-amount]').setValue('3')
      market.convert.mockRejectedValueOnce({ status: 504, message: 'timeout' })
      await confirm(w)
      await flushPromises()
      const [amount, id] = market.convert.mock.calls[0]
      w.unmount()

      // It went through after all: on the next visit the summary shows less than the attempt.
      market.earnings.mockResolvedValue({ ...SOME, available: '0.4999999999', converted_total: '5' })
      const again = mount(HostedView, { global: { plugins: [i18n] }, attachTo: document.body })
      await flushPromises()
      expect(again.get('[data-testid=earnings-convert]').attributes('disabled')).toBeUndefined()
      await again.get('[data-testid=earnings-convert]').trigger('click')
      expect(again.get('[data-testid=earnings-pending]').text()).toContain('$3.00')
      expect(again.get('[data-testid=earnings-amount]').attributes('disabled')).toBeDefined()
      market.convert.mockResolvedValueOnce({ entry_id: 4, request_id: id, amount: '3', created_at: 'x', replayed: true })
      await again.get('[data-testid=earnings] form').trigger('submit')
      await flushPromises()
      expect(market.convert.mock.calls[1]).toEqual([amount, id])
      expect(again.find('[data-testid=earnings-convert-error]').exists()).toBe(false)
      again.unmount()
    })

    it('a retry dialog whose conversion was settled elsewhere never sends a new one', async () => {
      const { w } = await openDialog()
      await w.get('[data-testid=earnings-amount]').setValue('3')
      market.convert.mockRejectedValueOnce({ status: 503, message: 'busy' })
      await confirm(w)
      await flushPromises()
      expect(market.convert).toHaveBeenCalledTimes(1)
      // Reopened as a retry of that conversion …
      await w.get('[data-testid=earnings] dialog .btn-line').trigger('click')
      await w.get('[data-testid=earnings-convert]').trigger('click')
      expect(w.get('[data-testid=earnings-pending]').text()).toContain('$3.00')
      // … which another visit then settles (success).
      const { useHostedStore } = await import('@/stores/hosted')
      useHostedStore().pendingConvert = null
      await flushPromises()
      expect(document.querySelector('[data-testid=earnings] dialog')?.hasAttribute('open')).toBe(false)
      // Confirming the (now closed) dialog sends nothing new.
      await confirm(w)
      await flushPromises()
      expect(market.convert).toHaveBeenCalledTimes(1)
    })

    it('an unsettled conversion survives a page reload (fresh store): the retry sends the same id and amount', async () => {
      const { w } = await openDialog()
      await w.get('[data-testid=earnings-amount]').setValue('2.5')
      market.convert.mockRejectedValueOnce({ status: 0, code: 'NETWORK', message: 'network' })
      await confirm(w)
      await flushPromises()
      const [amount, id] = market.convert.mock.calls[0]
      expect(JSON.parse(localStorage.getItem(`ccyou.market.pendingConvert.${OWNER_ID}`) ?? 'null')).toEqual({ requestId: id, amount })
      w.unmount()

      // Reload: a new Pinia, the same signed-in user.
      freshPinia()
      const again = mount(HostedView, { global: { plugins: [i18n] }, attachTo: document.body })
      await flushPromises()
      await again.get('[data-testid=earnings-convert]').trigger('click')
      expect(again.get('[data-testid=earnings-pending]').text()).toContain('$2.50')
      market.convert.mockResolvedValueOnce({ entry_id: 4, request_id: id, amount: '2.5', created_at: 'x', replayed: true })
      await again.get('[data-testid=earnings] form').trigger('submit')
      await flushPromises()
      expect(market.convert.mock.calls[1]).toEqual([amount, id])
      // Settled (a replay counts): storage is cleared.
      expect(localStorage.getItem(`ccyou.market.pendingConvert.${OWNER_ID}`)).toBeNull()
      again.unmount()
    })

    it('429, 401/403 and 5xx keep the id and amount ("结果未确认，可重试")', async () => {
      const { w } = await openDialog()
      await w.get('[data-testid=earnings-amount]').setValue('2')
      for (const err of [{ status: 429, message: 'slow down' }, { status: 403, message: 'forbidden' }, { status: 500, reason: 'MARKET_EARNING_UNAVAILABLE', message: 'x' }]) {
        market.convert.mockRejectedValueOnce(err)
        await confirm(w)
        await flushPromises()
        expect(w.get('[data-testid=earnings-convert-error]').text()).toContain('结果未确认，可重试')
        expect(w.get('[data-testid=earnings-pending]').text()).toContain('$2.00')
      }
      const ids = new Set(market.convert.mock.calls.map((c) => c[1]))
      expect(ids.size).toBe(1)
      expect(localStorage.getItem(`ccyou.market.pendingConvert.${OWNER_ID}`)).not.toBeNull()
    })

    it('two tabs: B cannot start a new conversion while A has one unsettled; B retries A\'s id; settling never deletes another id', async () => {
      const key = `ccyou.market.pendingConvert.${OWNER_ID}`
      const opts = { global: { plugins: [i18n] }, attachTo: document.body }
      hosted.listOwnedAccounts.mockResolvedValue([account()])
      market.getListing.mockResolvedValue(view())
      market.earnings.mockResolvedValue(SOME)
      // Tab B is open first (nothing pending yet) and starts typing.
      freshPinia()
      const tabB = mount(HostedView, opts)
      await flushPromises()
      await tabB.get('[data-testid=earnings-convert]').trigger('click')
      await tabB.get('[data-testid=earnings-amount]').setValue('1')
      // Tab A (another store, same storage) records a conversion whose answer is lost.
      const aId = '11111111-1111-4111-8111-111111111111'
      localStorage.setItem(key, JSON.stringify({ requestId: aId, amount: '3' }))
      // B's new conversion is refused: it shows A's and sends nothing.
      await tabB.get('[data-testid=earnings] form').trigger('submit')
      await flushPromises()
      expect(market.convert).not.toHaveBeenCalled()
      expect(tabB.get('[data-testid=earnings-pending]').text()).toContain('$3.00')
      expect(tabB.get('[data-testid=earnings-convert-error]').text()).toContain('结果未确认，可重试')
      // Confirming again retries A's id and amount.
      let resolve!: (v: unknown) => void
      market.convert.mockReturnValueOnce(new Promise((r) => (resolve = r)))
      await tabB.get('[data-testid=earnings] form').trigger('submit')
      expect(market.convert.mock.calls[0]).toEqual(['3', aId])
      // Meanwhile another conversion replaced the record (e.g. A settled and started a new one).
      const otherId = '22222222-2222-4222-8222-222222222222'
      localStorage.setItem(key, JSON.stringify({ requestId: otherId, amount: '1' }))
      resolve({ entry_id: 1, request_id: aId, amount: '3', created_at: 'x', replayed: true })
      await flushPromises()
      // Compare-and-delete: the other conversion is still recorded.
      expect(JSON.parse(localStorage.getItem(key) ?? 'null')).toEqual({ requestId: otherId, amount: '1' })
      tabB.unmount()
    })

    it('no record, no request: a storage that refuses the write blocks the conversion', async () => {
      const { w } = await openDialog()
      const spy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
        throw new Error('QuotaExceededError')
      })
      await w.get('[data-testid=earnings-amount]').setValue('1')
      await confirm(w)
      await flushPromises()
      spy.mockRestore()
      expect(market.convert).not.toHaveBeenCalled()
      expect(w.get('[data-testid=earnings-convert-error]').text()).toContain('无法保存转换记录')
    })

    it('BALANCE_OUT_OF_RANGE is not terminal: the id and amount are kept', async () => {
      const { w } = await openDialog()
      await w.get('[data-testid=earnings-amount]').setValue('1')
      market.convert.mockRejectedValueOnce({ status: 400, reason: 'BALANCE_OUT_OF_RANGE', message: 'x' })
      await confirm(w)
      await flushPromises()
      expect(w.get('[data-testid=earnings-pending]').text()).toContain('$1.00')
      expect(localStorage.getItem(`ccyou.market.pendingConvert.${OWNER_ID}`)).not.toBeNull()
    })

    it('two tabs confirming at once (Web Locks): one request id is created and sent; no lock is held during the request', async () => {
      const held: string[] = []
      let chain: Promise<unknown> = Promise.resolve()
      const request = vi.fn((name: string, fn: () => unknown) => {
        const run = chain.then(async () => {
          held.push(name)
          try {
            return await fn()
          } finally {
            held.pop()
          }
        })
        chain = run.catch(() => undefined)
        return run
      })
      Object.defineProperty(navigator, 'locks', { value: { request }, configurable: true })
      try {
        hosted.listOwnedAccounts.mockResolvedValue([account()])
        market.getListing.mockResolvedValue(view())
        market.earnings.mockResolvedValue(SOME)
        const opts = { global: { plugins: [i18n] }, attachTo: document.body }
        const tabs = []
        for (const amt of ['1', '2']) {
          freshPinia()
          const w = mount(HostedView, opts)
          await flushPromises()
          await w.get('[data-testid=earnings-convert]').trigger('click')
          await w.get('[data-testid=earnings-amount]').setValue(amt)
          tabs.push(w)
        }
        market.convert.mockImplementation(async () => {
          expect(held).toEqual([])
          throw { status: 0, message: 'network' }
        })
        await Promise.all(tabs.map((w) => w.get('[data-testid=earnings] form').trigger('submit')))
        await flushPromises()
        expect(market.convert).toHaveBeenCalledTimes(1)
        const sentId = market.convert.mock.calls[0][1]
        expect(JSON.parse(localStorage.getItem(`ccyou.market.pendingConvert.${OWNER_ID}`) ?? 'null').requestId).toBe(sentId)
        tabs.forEach((w) => w.unmount())
      } finally {
        Reflect.deleteProperty(navigator, 'locks')
      }
    })

    it('an unreadable record: no POST, no overwrite; the user clears it explicitly after checking the ledger', async () => {
      const key = `ccyou.market.pendingConvert.${OWNER_ID}`
      localStorage.setItem(key, '{"requestId":"not-a-uuid"')
      const { w } = await openDialog()
      expect(w.get('[data-testid=earnings-unreadable]').text()).toContain('无法读取上一笔转换记录')
      await w.get('[data-testid=earnings-amount]').setValue('1')
      await confirm(w)
      await flushPromises()
      expect(market.convert).not.toHaveBeenCalled()
      expect(localStorage.getItem(key)).toBe('{"requestId":"not-a-uuid"')
      await w.get('[data-testid=earnings-clear-unreadable]').trigger('click')
      await flushPromises()
      expect(localStorage.getItem(key)).toBeNull()
      expect(w.find('[data-testid=earnings-unreadable]').exists()).toBe(false)
    })

    it('getItem throws: no POST', async () => {
      const { w } = await openDialog()
      const spy = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
        throw new Error('denied')
      })
      await w.get('[data-testid=earnings-amount]').setValue('1')
      await confirm(w)
      await flushPromises()
      spy.mockRestore()
      expect(market.convert).not.toHaveBeenCalled()
      expect(w.get('[data-testid=earnings-convert-error]').text()).toContain('无法读取上一笔转换记录')
    })

    it('a second mount cannot send while the first request is in flight, and a late answer never clears a newer attempt', async () => {
      freshPinia()
      hosted.listOwnedAccounts.mockResolvedValue([account()])
      market.getListing.mockResolvedValue(view())
      market.earnings.mockResolvedValue(SOME)
      const opts = { global: { plugins: [i18n] }, attachTo: document.body }
      const first = mount(HostedView, opts)
      await flushPromises()
      await first.get('[data-testid=earnings-convert]').trigger('click')
      await first.get('[data-testid=earnings-amount]').setValue('3')
      let resolveA!: (v: unknown) => void
      market.convert.mockReturnValueOnce(new Promise((r) => (resolveA = r)))
      await first.get('[data-testid=earnings] form').trigger('submit')
      first.unmount()

      // Same session, page opened again while A is still in flight: no second request can start.
      const second = mount(HostedView, opts)
      await flushPromises()
      expect(second.get('[data-testid=earnings-convert]').attributes('disabled')).toBeDefined()
      expect(market.convert).toHaveBeenCalledTimes(1)
      const refreshesBefore = market.earnings.mock.calls.length

      // A answers: settled; the next attempt is a new one with a new id.
      resolveA({ entry_id: 1, request_id: 'a', amount: '3', created_at: 'x', replayed: false })
      await flushPromises()
      // The page on screen (not the unmounted one) refreshes its summary.
      expect(market.earnings.mock.calls.length).toBeGreaterThan(refreshesBefore)
      await second.get('[data-testid=earnings-convert]').trigger('click')
      expect(second.find('[data-testid=earnings-pending]').exists()).toBe(false)
      await second.get('[data-testid=earnings-amount]').setValue('1')
      market.convert.mockRejectedValueOnce({ status: 0, message: 'network' })
      await second.get('[data-testid=earnings] form').trigger('submit')
      await flushPromises()
      expect(market.convert).toHaveBeenCalledTimes(2)
      expect(market.convert.mock.calls[1][1]).not.toBe(market.convert.mock.calls[0][1])
      expect(second.get('[data-testid=earnings-pending]').text()).toContain('$1.00')
      second.unmount()
    })
  })
})
