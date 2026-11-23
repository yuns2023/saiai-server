import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UsageProgressBar from '../UsageProgressBar.vue'

vi.mock('@/i18n', async () => {
  const { default: messages } = await import('@/i18n/locales/en')
  const t = (key: string, params: Record<string, unknown> = {}) => {
    const message = key.split('.').reduce<unknown>((value, part) => (value as Record<string, unknown>)?.[part], messages)
    return String(message ?? key).replace(/\{(\w+)\}/g, (_, name: string) => String(params[name] ?? ''))
  }
  return { i18n: { global: { t } }, getLocale: () => 'en' }
})

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

describe('UsageProgressBar', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-03-17T00:00:00Z'))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('showNowWhenIdle=true 且利用率为 0 时显示“现在”', () => {
    const wrapper = mount(UsageProgressBar, {
      props: {
        label: '5h',
        utilization: 0,
        resetsAt: '2026-03-17T02:30:00Z',
        showNowWhenIdle: true,
        color: 'indigo'
      }
    })

    expect(wrapper.text()).toContain('common.now')
    expect(wrapper.text()).not.toContain('2h 30m')
  })

  it('showNowWhenIdle=true 但利用率大于 0 时显示倒计时', () => {
    const wrapper = mount(UsageProgressBar, {
      props: {
        label: '7d',
        utilization: 12,
        resetsAt: '2026-03-17T02:30:00Z',
        showNowWhenIdle: true,
        color: 'emerald'
      }
    })

    expect(wrapper.text()).toContain('2h 30m')
    expect(wrapper.text()).not.toContain('common.now')
  })

  it('showNowWhenIdle=false 时保持原有倒计时行为', () => {
    const wrapper = mount(UsageProgressBar, {
      props: {
        label: '1d',
        utilization: 0,
        resetsAt: '2026-03-17T02:30:00Z',
        showNowWhenIdle: false,
        color: 'indigo'
      }
    })

    expect(wrapper.text()).toContain('2h 30m')
    expect(wrapper.text()).not.toContain('common.now')
  })

  it('在根元素上暴露额度说明', () => {
    const wrapper = mount(UsageProgressBar, {
      props: {
        label: '7d F',
        utilization: 43,
        tooltip: 'Fable 7天独立额度',
        color: 'amber'
      }
    })

    expect(wrapper.attributes('title')).toBe('Fable 7天独立额度')
  })

  it('uses the shared countdown format for multi-day resets', () => {
    const wrapper = mount(UsageProgressBar, {
      props: { label: '7d', utilization: 12, resetsAt: '2026-03-22T18:30:00Z', color: 'emerald' }
    })
    expect(wrapper.text()).toContain('5d 18h')
  })

  it('shows standard cost and estimate, including a known zero', () => {
    const stats = { requests: 0, tokens: 0, cost: 0, standard_cost: 0, user_cost: 0 }
    const wrapper = mount(UsageProgressBar, {
      props: { label: '7d', utilization: 12, color: 'emerald', windowStats: stats, standardCost: 0, estimatedCapacity: 123.456 }
    })
    expect(wrapper.get('[data-testid="window-standard-cost"]').text()).toBe('$0.00')
    expect(wrapper.find('[data-testid="window-estimated-capacity"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('req')
  })

  it('keeps one line per window: A and the estimate inline, the rest only on hover', async () => {
    const stats = { requests: 2100, tokens: 428_600_000, cost: 136.03, standard_cost: 136.21, user_cost: 7.08 }
    const wrapper = mount(UsageProgressBar, {
      attachTo: document.body,
      props: {
        label: '5h', utilization: 16, color: 'indigo', resetsAt: '2026-03-17T02:30:00Z',
        windowStats: stats, standardCost: 136.21, estimatedCapacity: 851.31
      }
    })
    const summary = wrapper.get('[data-testid="window-summary"]').text()
    expect(summary).toContain('A $136')
    expect(summary).not.toContain('136.03')
    expect(summary).toContain('admin.accounts.usageWindow.estimatedCapacity')

    const details = wrapper.get('[data-testid="window-stats"]')
    expect(details.isVisible()).toBe(false)
    await wrapper.trigger('pointerenter', { pointerType: 'mouse' })
    expect(details.isVisible()).toBe(true)
    expect(details.text()).toContain('$136.03')
    expect(details.text()).toContain('$136.21')
    expect(details.text()).toContain('$851.31')
    await wrapper.trigger('pointerleave', { pointerType: 'mouse' })
    expect(details.isVisible()).toBe(false)

    // Touch: tap toggles; hover events from touch are ignored.
    await wrapper.trigger('pointerenter', { pointerType: 'touch' })
    expect(details.isVisible()).toBe(false)
    await wrapper.trigger('pointerup', { pointerType: 'touch' })
    expect(details.isVisible()).toBe(true)

    // Any scroll (the table scrolls in its own container) closes it.
    document.body.dispatchEvent(new Event('scroll'))
    await wrapper.vm.$nextTick()
    expect(details.isVisible()).toBe(false)
    wrapper.unmount()
  })

  it('has no summary or hover details for an idle window without estimates', () => {
    const wrapper = mount(UsageProgressBar, {
      props: { label: '5h', utilization: 0, color: 'indigo', windowStats: { requests: 0, tokens: 0, cost: 0, standard_cost: 0, user_cost: 0 } }
    })
    expect(wrapper.find('[data-testid="window-summary"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="window-stats"]').exists()).toBe(false)
  })

  it('labels an estimate whose sample is at least 15 minutes old', async () => {
    const wrapper = mount(UsageProgressBar, {
      props: { label: '5h', utilization: 26, color: 'indigo', resetsAt: '2026-03-17T02:30:00Z', estimatedCapacity: 41.5, estimateSampledAt: '2026-03-16T23:46:00Z' }
    })
    const chip = () => wrapper.get('[data-testid="window-estimated-capacity"]').text()
    expect(chip()).not.toContain('common.time')
    vi.advanceTimersByTime(60 * 1000)
    await wrapper.vm.$nextTick()
    expect(chip()).toContain('common.time.minutesAgo')
  })

  it('hides standard cost for an unknown or rolling window but keeps req/A/U', () => {
    const stats = { requests: 3, tokens: 30, cost: 1, standard_cost: 2, user_cost: 0 }
    const wrapper = mount(UsageProgressBar, {
      props: { label: '5h', utilization: 12, color: 'indigo', windowStats: stats, statsRolling: true, standardCost: null }
    })
    expect(wrapper.find('[data-testid="window-standard-cost"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="window-requests"]').text()).toBe('3 req*')
    expect(wrapper.text()).toContain('A $1.00')
  })

  it('shows U as revenue (balance + converted subscription) in one chip', () => {
    // Balance $100.25 + subscription $250 at rate 0.64 (= 160).
    const stats = {
      requests: 3, tokens: 30, cost: 1, standard_cost: 2, user_cost: 350.25,
      balance_cost: 100.25, subscription_cost: 250, subscription_revenue: 160, subscription_revenue_unpriced_cost: 0,
    }
    const wrapper = mount(UsageProgressBar, { props: { label: '7d', utilization: 36, color: 'emerald', windowStats: stats } })
    expect(wrapper.get('[data-testid="window-revenue"]').text()).toBe('260.25')
    expect(wrapper.text()).not.toContain('≈¥')
  })

  it('hides window costs once the reset passes', async () => {
    const wrapper = mount(UsageProgressBar, {
      props: { label: '5h', utilization: 12, color: 'indigo', resetsAt: '2026-03-17T00:01:30Z', standardCost: 1.5, estimatedCapacity: 10 }
    })
    expect(wrapper.find('[data-testid="window-standard-cost"]').exists()).toBe(true)
    vi.advanceTimersByTime(2 * 60 * 1000)
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[data-testid="window-standard-cost"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="window-estimated-capacity"]').exists()).toBe(false)
  })

  it('hides cost chips when stats are unknown', () => {
    const wrapper = mount(UsageProgressBar, {
      props: { label: '7d', utilization: 12, color: 'emerald', windowStats: null, estimatedCapacity: null }
    })
    expect(wrapper.find('[data-testid="window-stats"]').exists()).toBe(false)
  })
})
