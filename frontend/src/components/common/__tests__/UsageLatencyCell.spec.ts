import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UsageLatencyCell from '../UsageLatencyCell.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))

const render = (row: Record<string, unknown>) => mount(UsageLatencyCell, { props: { row } })

describe('UsageLatencyCell', () => {
  it('stacks first token, total duration and output TPS for streamed requests', () => {
    const wrapper = render({ first_token_ms: 2820, duration_ms: 36090, output_tokens: 1886 })
    expect(wrapper.get('[data-testid="usage-latency-first-token"]').text()).toBe('2.82s')
    expect(wrapper.get('[data-testid="usage-latency-duration"]').text()).toBe('36.09s')
    expect(wrapper.get('[data-testid="usage-latency-tps"]').text()).toBe('56.7 tok/s')
    expect(wrapper.get('[data-testid="usage-latency-tps"]').attributes('aria-label')).toContain('usage.outputTpsHint:{"tokens":1886}')
  })

  it('shows only total duration when no first token was recorded', () => {
    const wrapper = render({ first_token_ms: null, duration_ms: 850, output_tokens: 500 })
    expect(wrapper.find('[data-testid="usage-latency-first-token"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="usage-latency-duration"]').text()).toBe('850ms')
    expect(wrapper.find('[data-testid="usage-latency-tps"]').exists()).toBe(false)
  })
})
