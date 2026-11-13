import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UsageProgressBar from '../UsageProgressBar.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params: Record<string, unknown> = {}) => `${key}${Object.keys(params).length ? JSON.stringify(params) : ''}`
    })
  }
})

describe('UsageProgressBar window restart', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-13T04:00:00Z'))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  const mountBar = (resetsAt: string, windowRestart: { start: string; source: 'auto' | 'manual' } | null) =>
    mount(UsageProgressBar, {
      props: { label: '7d', utilization: 20, resetsAt, color: 'emerald', standardCost: 12.5, windowRestart }
    })

  it('shows when the statistics restarted and why', () => {
    const wrapper = mountBar('2026-11-15T13:00:00Z', { start: '2026-11-12T00:00:00Z', source: 'auto' })
    const chip = wrapper.get('[data-testid="window-restart"]')
    expect(chip.text()).toContain('admin.accounts.usageWindow.restartedChip')
    expect(chip.attributes('title')).toContain('admin.accounts.usageWindow.restartedHint')
    expect(chip.attributes('title')).toContain('admin.accounts.usageWindow.restartSourceAuto')
  })

  it('hides without a restart or once the window has passed', () => {
    expect(mountBar('2026-11-15T13:00:00Z', null).find('[data-testid="window-restart"]').exists()).toBe(false)
    expect(mountBar('2026-11-13T03:00:00Z', { start: '2026-11-12T00:00:00Z', source: 'manual' }).find('[data-testid="window-restart"]').exists()).toBe(false)
  })
})
