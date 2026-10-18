import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import InputRiskAlerts from '../InputRiskAlerts.vue'
const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (s: string) => s }) }))
vi.mock('@/utils/format', () => ({ formatDateTime: (s: string) => s }))
const props = { userId: 42, startTime: '2026-09-18T00:00:00Z', endTime: '2026-10-18T00:00:00Z', refreshKey: 0 }
const mountPanel = () => mount(InputRiskAlerts, { props, global: { stubs: { ModerationResultCell: true } } })
describe('InputRiskAlerts', () => {
  beforeEach(() => get.mockReset())
  it('shows unbilled alerts, pages, and resets on user change', async () => {
    get.mockResolvedValue({ data: { total: 21, items: [{ request_id: 'unbilled', user_id: 42, turn_number: 2, moderation_result: { status: 'flagged', updated_at: props.endTime } }] } })
    const wrapper = mountPanel()
    await flushPromises()
    expect(wrapper.text()).toContain('unbilled')
    expect(get).toHaveBeenLastCalledWith('/admin/usage/input-risk-alerts', expect.objectContaining({ params: { user_id: 42, start_time: props.startTime, end_time: props.endTime, page: 1, page_size: 20 } }))
    await wrapper.findAll('button')[1].trigger('click')
    await flushPromises()
    expect(get.mock.lastCall?.[1].params.page).toBe(2)
    await wrapper.setProps({ userId: 7 })
    await flushPromises()
    expect(get.mock.lastCall?.[1].params).toMatchObject({ page: 1, user_id: 7 })
    wrapper.unmount()
  })
  it('reports lookup failure instead of zero alerts and retries on refresh', async () => {
    get.mockRejectedValueOnce(new Error('offline'))
    const wrapper = mountPanel()
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('(0)')
    get.mockResolvedValue({ data: { total: 0, items: [] } })
    await wrapper.setProps({ refreshKey: 1 })
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('noAlerts')
    wrapper.unmount()
  })
})
