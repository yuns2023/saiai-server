import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AccountStatsModal from '../AccountStatsModal.vue'
import type { Account } from '@/types'
const { getStats } = vi.hoisted(() => ({ getStats: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getStats } } }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-chartjs', () => ({ Line: { template: '<div />' } }))

const stats = (requests: number) => ({
  summary: { total_requests: requests, total_cost: 0, total_user_cost: 0, total_standard_cost: 0, avg_daily_cost: 0, avg_daily_user_cost: 0, avg_daily_requests: 0, total_tokens: 0, avg_daily_tokens: 0, avg_duration_ms: 0, actual_days_used: 0, days: 30 },
  history: [], models: [], endpoints: [], upstream_endpoints: []
})
describe('AccountStatsModal', () => {
  it('defaults to 30 days and ignores stale responses after changing the range', async () => {
    getStats.mockResolvedValueOnce(stats(10))
    const wrapper = mount(AccountStatsModal, {
      props: { show: false, account: { id: 1, name: 'Test', status: 'active' } as Account },
      global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, DateTimeRangePicker: true, ModelDistributionChart: true, EndpointDistributionChart: true, LoadingSpinner: true } }
    })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(getStats.mock.calls[0][1]).toEqual(expect.objectContaining({ timezone: expect.any(String), start_time: expect.any(String), end_time: expect.any(String) }))
    expect(wrapper.findComponent({ name: 'DateTimeRangePicker' }).props('preset')).toBe('30days')
    let finishOld!: (value: ReturnType<typeof stats>) => void
    getStats.mockImplementationOnce(() => new Promise(resolve => { finishOld = resolve }))
    const picker = wrapper.findComponent({ name: 'DateTimeRangePicker' })
    picker.vm.$emit('change', { startTime: '2026-10-17T10:00:00Z', endTime: '2026-10-17T11:00:00Z', preset: null })
    await flushPromises()
    getStats.mockResolvedValueOnce(stats(222))
    picker.vm.$emit('change', { startTime: '2026-10-17T12:00:00Z', endTime: '2026-10-17T13:00:00Z', preset: null })
    await flushPromises()
    expect(getStats.mock.calls.at(-1)![1].start_time).toBe('2026-10-17T12:00:00Z')
    finishOld(stats(111))
    await flushPromises()
    expect(wrapper.text()).toContain('222')
    expect(wrapper.text()).not.toContain('111')
    wrapper.unmount()
  })
})
