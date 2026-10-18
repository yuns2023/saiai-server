import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import CacheUsageTrend from '../CacheUsageTrend.vue'
import type { TrendDataPoint } from '@/types'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-chartjs', () => ({ Line: { name: 'Line', props: ['data', 'options'], template: '<div />' } }))
const point = (input: number, read: number): TrendDataPoint => ({ date: '2026-10-18 00:15', requests: 2, input_tokens: input, cache_read_tokens: read, cache_creation_tokens: 0, output_tokens: 900, total_tokens: 1000, cost: 1, actual_cost: 1 })
describe('CacheUsageTrend', () => {
  it('weights the period rate by input tokens rather than averaging requests or buckets', () => {
    const wrapper = mount(CacheUsageTrend, { props: { trendData: [point(100, 0), point(0, 900)] } })
    expect(wrapper.get('[data-testid=period-cache-read-rate]').text()).toContain('90.00%')
    const charts = wrapper.findAllComponents({ name: 'Line' })
    expect(charts).toHaveLength(2)
    expect(charts[0].props('data').datasets[0].data).toEqual([0, 100])
    expect(charts[1].props('data').datasets.map((dataset: { data: number[] }) => dataset.data)).toEqual([[100, 0], [2, 2]])
    wrapper.unmount()
  })
  it('does not display a misleading rate while loading or with no data', () => {
    for (const props of [{ trendData: [] }, { trendData: [point(10, 90)], loading: true }]) {
      const wrapper = mount(CacheUsageTrend, { props })
      expect(wrapper.find('[data-testid=period-cache-read-rate]').exists()).toBe(false)
      wrapper.unmount()
    }
  })
})
