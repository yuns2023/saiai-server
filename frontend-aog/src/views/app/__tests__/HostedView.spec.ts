import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { i18n } from '@/i18n'
import type { OwnedAccount, OwnedUsageItem } from '@/api'

const hosted = vi.hoisted(() => ({ listOwnedAccounts: vi.fn(), listOwnedAccountUsage: vi.fn() }))
vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return { ...actual, hostedApi: hosted }
})

import HostedView from '../HostedView.vue'

const summary = { self_requests: 3, self_standard_cost: 1.23, self_service_fee: 0.25, others_requests: 2, others_standard_cost: 0.7 }

const current: OwnedAccount = {
  account_id: 12,
  current: true,
  display_name: '托管号 #12',
  platform: 'anthropic',
  type: 'setup-token',
  status: 'available',
  updated_at: '2026-11-16T03:00:00Z',
  stale: true,
  windows: [
    { key: 'five_hour', utilization: 58, resets_at: '2026-11-16T06:00:00Z', expired: false, used_cost_usd: null, estimated_capacity_usd: null, estimate_sampled_at: null },
    { key: 'seven_day_opus', utilization: 120, resets_at: '2026-11-20T00:00:00Z', expired: false, used_cost_usd: null, estimated_capacity_usd: null, estimate_sampled_at: null },
    { key: 'weird_window', utilization: 5, resets_at: null, expired: false, used_cost_usd: null, estimated_capacity_usd: null, estimate_sampled_at: null }
  ],
  last_24h: summary,
  last_7d: summary
}
const past: OwnedAccount = {
  account_id: 7,
  current: false,
  display_name: '托管号 #7',
  platform: null,
  type: null,
  status: null,
  updated_at: null,
  stale: false,
  windows: [],
  last_24h: { self_requests: 0, self_standard_cost: 0, others_requests: 0, others_standard_cost: 0 },
  last_7d: { self_requests: 1, self_standard_cost: 0.01, others_requests: 0, others_standard_cost: 0 }
}

const selfRow: OwnedUsageItem = {
  created_at: '2026-11-16T01:59:12.345Z',
  model: 'claude-sonnet-5',
  input_tokens: 1200,
  output_tokens: 300,
  cache_creation_tokens: 0,
  cache_read_tokens: 5000,
  standard_cost: 0.0386,
  is_self: true,
  api_key_name: 'my laptop',
  charged: 0.0386
}
// The backend must never send these for other users; the page must not show them even if it did.
const otherRow: OwnedUsageItem = {
  created_at: '2026-11-16T01:00:00Z',
  model: 'claude-opus-5',
  input_tokens: 1,
  output_tokens: 1,
  cache_creation_tokens: 0,
  cache_read_tokens: 0,
  standard_cost: 0.01,
  is_self: false,
  api_key_name: 'someone else key',
  charged: 0.5
}

let savedTZ: string | undefined

async function mountView(accounts: OwnedAccount[]) {
  hosted.listOwnedAccounts.mockResolvedValue(accounts)
  setActivePinia(createPinia())
  i18n.global.locale.value = 'zh'
  const w = mount(HostedView, { global: { plugins: [i18n] } })
  await flushPromises()
  return w
}

const lastUsageCall = () => hosted.listOwnedAccountUsage.mock.calls.at(-1) as [number, { start: string; end: string; limit?: number; cursor?: string | null }]

enableAutoUnmount(afterEach)

describe('HostedView', () => {
  beforeEach(() => {
    savedTZ = process.env.TZ
    process.env.TZ = 'Asia/Shanghai'
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-11-16T04:00:00Z')) // 12:00 in UTC+8
    hosted.listOwnedAccounts.mockReset()
    hosted.listOwnedAccountUsage.mockReset().mockResolvedValue({ items: [], next_cursor: null })
  })
  afterEach(() => {
    vi.useRealTimers()
    if (savedTZ === undefined) delete process.env.TZ
    else process.env.TZ = savedTZ
  })

  it('shows the explanatory note', async () => {
    const w = await mountView([current])
    expect(w.find('[data-testid=hosted-note]').text()).toBe('这里显示归属于你的上游账号的用量；其他用户的请求只显示用量，不显示其身份。标准价为官方价格参考。')
  })

  it('renders current accounts first and past ones in their own section', async () => {
    const w = await mountView([past, current])
    const sections = w.findAll('[data-testid^=hosted-section-]')
    expect(sections.map((s) => s.attributes('data-testid'))).toEqual(['hosted-section-current', 'hosted-section-history'])
    const cur = w.find('[data-testid=hosted-section-current]')
    const hist = w.find('[data-testid=hosted-section-history]')
    expect(cur.text()).toContain('托管号 #12')
    expect(cur.text()).not.toContain('托管号 #7')
    expect(hist.text()).toContain('历史托管号')
    expect(hist.text()).toContain('托管号 #7')
    // Past accounts carry no status and no windows.
    expect(hist.find('[data-testid=hosted-status]').exists()).toBe(false)
    expect(hist.find('[data-testid=hosted-windows]').exists()).toBe(false)
  })

  it('labels status, kind and windows, and flags stale data', async () => {
    const w = await mountView([current])
    const card = w.find('[data-testid=hosted-account-12]')
    expect(card.find('[data-testid=hosted-status]').text()).toBe('可用')
    expect(card.text()).toContain('Claude · Setup Token')
    expect(card.find('[data-testid=hosted-window-five_hour]').text()).toContain('5 小时')
    expect(card.find('[data-testid=hosted-window-five_hour]').text()).toContain('58%')
    expect(card.find('[data-testid=hosted-window-seven_day_opus]').text()).toContain('7 天 Opus')
    expect(card.find('[data-testid=hosted-window-seven_day_opus]').text()).toContain('120%')
    expect(card.find('[data-testid=hosted-window-seven_day_opus] [role=meter] > span').attributes('style')).toContain('width: 100%')
    expect(card.find('[data-testid=hosted-window-weird_window]').text()).toContain('weird_window')
    expect(card.find('[data-testid=hosted-stale]').text()).toBe('数据可能已过期')
    const stats = card.find('[data-testid=hosted-stats]').text()
    expect(stats).toContain('近 24 小时')
    expect(stats).toContain('近 7 天')
    expect(stats).toContain('3 次 · $1.23')
    expect(stats).toContain('2 次 · $0.7000')
  })

  it('shows my service fee next to my own usage, only when the server reports it', async () => {
    const w = await mountView([current, past])
    const fees = w.find('[data-testid=hosted-account-12]').findAll('[data-testid=hosted-self-fee]').map((f) => f.text())
    expect(fees).toEqual(['服务费 $0.2500', '服务费 $0.2500'])
    // Standard-price numbers stay as they were.
    expect(w.find('[data-testid=hosted-account-12] [data-testid=hosted-self]').text()).toContain('3 次 · $1.23')
    // Older servers send no fee: nothing is shown rather than a made-up $0.
    expect(w.find('[data-testid=hosted-account-7] [data-testid=hosted-self-fee]').exists()).toBe(false)
  })

  it('shows a zero fee (free hosting) as $0', async () => {
    const free = { ...summary, self_service_fee: 0 }
    const w = await mountView([{ ...current, last_24h: free, last_7d: free }])
    expect(w.findAll('[data-testid=hosted-self-fee]').map((f) => f.text())).toEqual(['服务费 $0', '服务费 $0'])
  })

  it('shows a friendly empty state when the user owns nothing', async () => {
    const w = await mountView([])
    expect(w.find('[data-testid=hosted-empty]').exists()).toBe(true)
    expect(w.find('[data-testid=hosted-usage]').exists()).toBe(false)
    expect(hosted.listOwnedAccountUsage).not.toHaveBeenCalled()
  })

  it('loads the first current account for the last 7 local days as a half-open UTC window', async () => {
    await mountView([past, current])
    expect(hosted.listOwnedAccountUsage).toHaveBeenCalledTimes(1)
    const [id, q] = lastUsageCall()
    expect(id).toBe(12)
    expect(q).toMatchObject({ start: '2026-11-09T16:00:00Z', end: '2026-11-16T16:00:00Z', limit: 50, cursor: null })
  })

  it('sends a custom range as UTC bounds and blocks spans over 31 days', async () => {
    const w = await mountView([current])
    await w.find('[data-testid=hosted-range-custom]').trigger('click')
    await w.find('[data-testid=hosted-from]').setValue('2026-11-01')
    await w.find('[data-testid=hosted-to]').setValue('2026-11-03')
    await flushPromises()
    expect(lastUsageCall()[1]).toMatchObject({ start: '2026-10-31T16:00:00Z', end: '2026-11-03T16:00:00Z' })

    const calls = hosted.listOwnedAccountUsage.mock.calls.length
    await w.find('[data-testid=hosted-from]').setValue('2026-10-01')
    await flushPromises()
    expect(w.find('[data-testid=hosted-range-problem]').text()).toBe('时间范围最多 31 天。')
    expect(hosted.listOwnedAccountUsage.mock.calls.length).toBe(calls)

    await w.find('[data-testid=hosted-from]').setValue('2026-11-05')
    await flushPromises()
    expect(w.find('[data-testid=hosted-range-problem]').text()).toBe('开始日期不能晚于结束日期。')
    expect(hosted.listOwnedAccountUsage.mock.calls.length).toBe(calls)
  })

  it('shows other users only as 「其他用户」, without key name or charge', async () => {
    hosted.listOwnedAccountUsage.mockResolvedValue({ items: [selfRow, otherRow], next_cursor: null })
    const w = await mountView([current])
    const rows = w.findAll('[data-testid=hosted-usage-row]')
    expect(rows).toHaveLength(2)
    expect(rows[0].find('[data-testid=hosted-caller]').text()).toBe('自己 · my laptop')
    expect(rows[0].find('[data-testid=hosted-charged]').text()).toBe('$0.0386')
    expect(rows[1].find('[data-testid=hosted-caller]').text()).toBe('其他用户')
    expect(rows[1].find('[data-testid=hosted-charged]').text()).toBe('—')
    expect(rows[1].find('[data-testid=hosted-standard]').text()).toBe('$0.0100')
    expect(w.text()).not.toContain('someone else key')
    expect(w.text()).not.toContain('$0.5000')
  })

  it('load more passes the cursor and appends the next page', async () => {
    hosted.listOwnedAccountUsage.mockResolvedValueOnce({ items: [selfRow], next_cursor: 'cur-1' })
    const w = await mountView([current])
    expect(w.findAll('[data-testid=hosted-usage-row]')).toHaveLength(1)

    hosted.listOwnedAccountUsage.mockResolvedValueOnce({ items: [otherRow], next_cursor: null })
    await w.find('[data-testid=hosted-load-more]').trigger('click')
    await flushPromises()
    const [id, q] = lastUsageCall()
    expect(id).toBe(12)
    expect(q).toMatchObject({ start: '2026-11-09T16:00:00Z', end: '2026-11-16T16:00:00Z', cursor: 'cur-1' })
    expect(w.findAll('[data-testid=hosted-usage-row]')).toHaveLength(2)
    expect(w.find('[data-testid=hosted-load-more]').exists()).toBe(false)
  })

  it('retries a failed page with the same cursor and keeps the rows shown', async () => {
    hosted.listOwnedAccountUsage.mockResolvedValueOnce({ items: [selfRow], next_cursor: 'cur-1' })
    const w = await mountView([current])
    hosted.listOwnedAccountUsage.mockRejectedValueOnce({ status: 0, code: 'NETWORK', message: 'network' })
    await w.find('[data-testid=hosted-load-more]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid=hosted-more-error]').text()).toBe('加载失败。')
    expect(w.findAll('[data-testid=hosted-usage-row]')).toHaveLength(1)

    hosted.listOwnedAccountUsage.mockResolvedValueOnce({ items: [otherRow], next_cursor: null })
    await w.find('[data-testid=hosted-load-more]').trigger('click')
    await flushPromises()
    expect(lastUsageCall()[1].cursor).toBe('cur-1')
    expect(w.findAll('[data-testid=hosted-usage-row]')).toHaveLength(2)
    expect(w.find('[data-testid=hosted-more-error]').exists()).toBe(false)
    expect(w.find('[data-testid=hosted-caller]').exists()).toBe(true)
    expect(w.text()).not.toContain('someone else key')
  })

  it('stops paging on 404 and restarts from page one on 400', async () => {
    hosted.listOwnedAccountUsage.mockResolvedValueOnce({ items: [selfRow], next_cursor: 'cur-1' })
    const w = await mountView([current])
    hosted.listOwnedAccountUsage.mockRejectedValueOnce({ status: 404, message: 'not found' })
    await w.find('[data-testid=hosted-load-more]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid=hosted-more-error]').text()).toBe('无权查看或不存在。')
    expect(w.find('[data-testid=hosted-load-more]').exists()).toBe(false)

    hosted.listOwnedAccountUsage.mockResolvedValueOnce({ items: [selfRow], next_cursor: 'cur-1' })
    await w.find('[data-testid=hosted-range-today]').trigger('click')
    await flushPromises()
    hosted.listOwnedAccountUsage.mockRejectedValueOnce({ status: 400, message: 'bad cursor' })
    await w.find('[data-testid=hosted-load-more]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid=hosted-more-error]').text()).toContain('查询条件无效')
    hosted.listOwnedAccountUsage.mockResolvedValueOnce({ items: [selfRow, selfRow], next_cursor: null })
    await w.find('[data-testid=hosted-reload]').trigger('click')
    await flushPromises()
    expect(lastUsageCall()[1].cursor).toBeNull()
    expect(w.findAll('[data-testid=hosted-usage-row]')).toHaveLength(2)
    expect(w.find('[data-testid=hosted-more-error]').exists()).toBe(false)
  })

  it('does not claim a value for an unknown utilization', async () => {
    const w = await mountView([{ ...current, windows: [{ ...current.windows[0], utilization: null }] }])
    const win = w.find('[data-testid=hosted-window-five_hour]')
    expect(win.text()).toContain('—')
    expect(win.find('[role=meter]').exists()).toBe(false)
  })

  it('switches the table when another account is picked', async () => {
    const w = await mountView([current, past])
    await w.find('[data-testid=hosted-account-7] input[type=radio]').setValue(true)
    await flushPromises()
    expect(lastUsageCall()[0]).toBe(7)
    expect(w.find('#hosted-usage-title').text()).toContain('托管号 #7')
  })

  it('explains 404 and 400 answers', async () => {
    hosted.listOwnedAccountUsage.mockRejectedValueOnce({ status: 404, message: 'not found' })
    const w = await mountView([current])
    expect(w.find('[data-testid=hosted-usage-error]').text()).toBe('无权查看或不存在。')

    hosted.listOwnedAccountUsage.mockRejectedValueOnce({ status: 400, message: 'bad' })
    await w.find('[data-testid=hosted-range-30d]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid=hosted-usage-error]').text()).toContain('查询条件无效')
  })

  it('ignores a load-more failure that lands after the account changed', async () => {
    hosted.listOwnedAccountUsage.mockResolvedValueOnce({ items: [selfRow], next_cursor: 'cur-1' })
    const w = await mountView([current, past])
    let failOld: (e: unknown) => void = () => undefined
    hosted.listOwnedAccountUsage.mockReturnValueOnce(new Promise((_, rej) => (failOld = rej)))
    await w.find('[data-testid=hosted-load-more]').trigger('click')

    let finishNew: (v: { items: OwnedUsageItem[]; next_cursor: string | null }) => void = () => undefined
    hosted.listOwnedAccountUsage.mockReturnValueOnce(new Promise((r) => (finishNew = r)))
    await w.find('[data-testid=hosted-account-7] input[type=radio]').setValue(true)
    failOld({ status: 404, message: 'gone' })
    await flushPromises()
    // The new account's first page is still loading; the old failure must not end that or show its error.
    expect(w.find('[data-testid=hosted-more-error]').exists()).toBe(false)
    expect(w.find('tbody .skeleton').exists()).toBe(true)

    finishNew({ items: [selfRow], next_cursor: 'cur-7' })
    await flushPromises()
    expect(w.find('[data-testid=hosted-more-error]').exists()).toBe(false)
    expect(w.find('[data-testid=hosted-load-more]').exists()).toBe(true)
  })

  it('drops a page that arrives after the account changed', async () => {
    let resolveFirst: (v: { items: OwnedUsageItem[]; next_cursor: string | null }) => void = () => undefined
    hosted.listOwnedAccountUsage.mockReturnValueOnce(new Promise((r) => (resolveFirst = r)))
    const w = await mountView([current, past])
    hosted.listOwnedAccountUsage.mockResolvedValueOnce({ items: [selfRow], next_cursor: null })
    await w.find('[data-testid=hosted-account-7] input[type=radio]').setValue(true)
    await flushPromises()
    resolveFirst({ items: [otherRow, otherRow], next_cursor: 'stale' })
    await flushPromises()
    expect(w.findAll('[data-testid=hosted-usage-row]')).toHaveLength(1)
    expect(w.find('[data-testid=hosted-load-more]').exists()).toBe(false)
  })
})
