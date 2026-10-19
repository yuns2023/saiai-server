import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import UsageBillingSummary from '@/components/common/UsageBillingSummary.vue'
import { usageBillingLabel } from '@/utils/usageBilling'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('billing source presentation', () => {
  it.each([[0, 'balanceCharge'], [1, 'subscriptionUsage'], [2, 'unclassifiedUsage'], [undefined, 'unclassifiedUsage']])('classifies persisted type %s', (type, label) => {
    expect(usageBillingLabel(type as number | undefined)).toBe(`usage.${label}`)
  })
  it.each([[3, 0], [0, 4], [3, 4], [0, 0]])('shows balance %s and subscription %s independently', (balance, subscription) => {
    const wrapper = mount(UsageBillingSummary, { props: { stats: { total_balance_cost: balance, total_subscription_cost: subscription } } })
    expect(wrapper.text()).toContain(`usage.balanceCharge$${balance.toFixed(4)}`)
    expect(wrapper.text()).toContain(`usage.subscriptionUsage$${subscription.toFixed(4)}`)
    expect(wrapper.text()).toContain('usage.subscriptionUsageHint')
  })
  it('does not silently display unavailable breakdowns as zero', () => {
    const wrapper = mount(UsageBillingSummary, { props: { stats: {} } })
    expect(wrapper.text()).toContain('—')
    expect(wrapper.text()).not.toContain('$0.0000')
  })
  it('exposes unclassified usage', () => {
    const wrapper = mount(UsageBillingSummary, { props: { stats: { total_unclassified_cost: 2 } } })
    expect(wrapper.text()).toContain('usage.unclassifiedUsage: $2.0000')
  })
})
