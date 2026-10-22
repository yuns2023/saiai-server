import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import APIKeySpendingRanking from '../APIKeySpendingRanking.vue'
import type { APIKeySpendingRankingItem, APIKeySpendingRankingResponse } from '@/types'

const { getRanking, searchUsers, push } = vi.hoisted(() => ({
  getRanking: vi.fn(), searchUsers: vi.fn(), push: vi.fn()
}))
vi.mock('@/api/admin/dashboard', () => ({ getAPIKeySpendingRanking: getRanking }))
vi.mock('@/api/admin/usage', () => ({ searchUsers }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => ({
  'admin.dashboard.keyRankingGlobal': 'Global',
  'admin.dashboard.keyRankingRetry': 'Retry',
  'admin.dashboard.keyRankingDetails': 'Usage records',
  'admin.dashboard.keyRankingFailed': 'Ranking failed',
  'admin.dashboard.keyRankingSearchFailed': 'Search failed',
}[key] || key) }) }))

const range = { startTime: '2026-03-01T10:00:00Z', endTime: '2026-03-02T10:00:00Z' }
const makeRow = (overrides: Partial<APIKeySpendingRankingItem> = {}): APIKeySpendingRankingItem => ({
  api_key_id: 11, key_name: 'Work', user_id: 7, email: 'user@example.com',
  actual_cost: 25, requests: 8, tokens: 1100,
  input_tokens: 10, output_tokens: 1000, cache_creation_tokens: 20, cache_read_tokens: 70,
  ...overrides
})
const makeResponse = (overrides: Partial<APIKeySpendingRankingResponse> = {}): APIKeySpendingRankingResponse => ({
  ranking: [makeRow()], total_actual_cost: 100, total_requests: 30, total_tokens: 10000,
  start_date: '2026-03-01', end_date: '2026-03-02', ...overrides
})
const wrappers: VueWrapper[] = []
const createWrapper = () => {
  const wrapper = mount(APIKeySpendingRanking, { props: range, global: { stubs: { LoadingSpinner: true } } })
  wrappers.push(wrapper)
  return wrapper
}
const searchForUser = async (wrapper: VueWrapper, query = 'other') => {
  await wrapper.find('input[type="search"]').setValue(query)
  await wrapper.find('form').trigger('submit')
  await flushPromises()
  await wrapper.find('ul button').trigger('click')
  await flushPromises()
}

describe('APIKeySpendingRanking', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getRanking.mockReset().mockResolvedValue(makeResponse())
    searchUsers.mockReset().mockResolvedValue([{ id: 8, email: 'other@example.com' }])
  })
  afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()) })

  it('loads global Top 12 and uses all-Key totals for shares and input-only reads for cache efficiency', async () => {
    getRanking.mockResolvedValue(makeResponse({ ranking: [
      makeRow(),
      makeRow({ api_key_id: 12, key_name: '', input_tokens: 0, cache_creation_tokens: 100, cache_read_tokens: 0 }),
      makeRow({ api_key_id: 13, input_tokens: 0, cache_creation_tokens: 0, cache_read_tokens: 0 }),
    ] }))
    const wrapper = createWrapper()
    await flushPromises()

    expect(getRanking).toHaveBeenCalledWith({
      start_time: range.startTime, end_time: range.endTime,
      start_date: undefined, end_date: undefined, user_id: undefined, limit: 12
    })
    expect(wrapper.text()).toContain('$100.0000')
    const rows = wrapper.findAll('tbody tr')
    expect(rows[0].text()).toContain('25.0%')
    expect(rows[0].text()).toContain('70.00%')
    expect(rows[0].text()).toContain('1.1K')
    expect(rows[0].find('[aria-hidden="true"] div').attributes('style')).toContain('width: 25%')
    expect(rows[1].text()).toContain('Key #12')
    expect(rows[1].text()).toContain('0.00%')
    expect(rows[2].text()).toContain('—')
    expect(wrapper.text()).not.toContain('NaN')
  })

  it('filters by a clicked user and can return to the global ranking', async () => {
    const wrapper = createWrapper()
    await flushPromises()
    await wrapper.findAll('tbody td')[1].find('button').trigger('click')
    await flushPromises()
    expect(getRanking).toHaveBeenLastCalledWith(expect.objectContaining({ user_id: 7 }))
    const globalButton = wrapper.findAll('button').find(button => button.text() === 'Global')!
    await globalButton.trigger('click')
    await flushPromises()
    expect(getRanking).toHaveBeenLastCalledWith(expect.objectContaining({ user_id: undefined }))
  })

  it('searches any user and preserves the user filter when dates change', async () => {
    const wrapper = createWrapper()
    await flushPromises()
    await searchForUser(wrapper, '  other  ')
    expect(searchUsers).toHaveBeenCalledWith('other')
    expect(getRanking).toHaveBeenLastCalledWith(expect.objectContaining({ user_id: 8 }))
    await wrapper.setProps({ startTime: undefined, endTime: undefined, startDate: '2026-03-04', endDate: '2026-03-05' })
    await flushPromises()
    expect(getRanking).toHaveBeenLastCalledWith(expect.objectContaining({
      user_id: 8, start_date: '2026-03-04', end_date: '2026-03-05', start_time: undefined, end_time: undefined
    }))
  })

  it('opens Key-filtered usage with the same precise time range', async () => {
    const wrapper = createWrapper()
    await flushPromises()
    await wrapper.find('tbody button').trigger('click')
    expect(push).toHaveBeenCalledWith({
      path: '/admin/usage', query: {
        start_date: undefined, end_date: undefined, start_time: range.startTime, end_time: range.endTime,
        user_id: '7', api_key_id: '11'
      }
    })
  })

  it('discards late responses from the previous user scope', async () => {
    const wrapper = createWrapper()
    await flushPromises()
    let resolveOld!: (value: APIKeySpendingRankingResponse) => void
    getRanking.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    await wrapper.findAll('tbody td')[1].find('button').trigger('click')
    getRanking.mockResolvedValue(makeResponse({ ranking: [makeRow({ key_name: 'Other scope', user_id: 8 })] }))
    await searchForUser(wrapper)
    resolveOld(makeResponse({ ranking: [makeRow({ key_name: 'Old scope' })] }))
    await flushPromises()
    expect(wrapper.text()).toContain('Other scope')
    expect(wrapper.text()).not.toContain('Old scope')
  })

  it('reports load failures and supports retry', async () => {
    getRanking.mockRejectedValueOnce(new Error('unavailable'))
    const wrapper = createWrapper()
    await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toContain('Ranking failed')
    await wrapper.find('[role="alert"] button').trigger('click')
    await flushPromises()
    expect(wrapper.find('tbody').exists()).toBe(true)
  })

  it('does not render invalid shares for a zero-cost scope', async () => {
    getRanking.mockResolvedValue(makeResponse({ total_actual_cost: 0, ranking: [makeRow({ actual_cost: 0 })] }))
    const wrapper = createWrapper()
    await flushPromises()
    expect(wrapper.find('tbody').text()).toContain('0.0%')
    expect(wrapper.text()).not.toContain('NaN')
    expect(wrapper.text()).not.toContain('Infinity')
  })

  it('keeps ranking scope unchanged when user search fails', async () => {
    searchUsers.mockRejectedValueOnce(new Error('unavailable'))
    const wrapper = createWrapper()
    await flushPromises()
    await wrapper.find('input').setValue('other')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toBe('Search failed')
    expect(getRanking).toHaveBeenCalledTimes(1)
  })
})
