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
    const text = wrapper.text().replace(/\s+/g, '')
    expect(text).toContain(`usage.balanceCharge$${balance.toFixed(4)}`)
    expect(text).toContain(`usage.subscriptionUsage$${subscription.toFixed(4)}`)
  })
  it('keeps the long explanations in the info tooltip instead of the card body', async () => {
    const wrapper = mount(UsageBillingSummary, {
      props: { stats: { total_balance_cost: 1, total_subscription_cost: 2 }, baseCost: 5 },
      attachTo: document.body,
    })
    expect(wrapper.text()).not.toContain('usage.subscriptionUsageHint')
    expect(wrapper.text().replace(/\s+/g, '')).toContain('usage.standardCost$5.0000')
    const hint = wrapper.get('[data-testid="usage-billing-hint"]')
    expect(hint.attributes('aria-label')).toContain('usage.subscriptionUsageHint')
    expect(hint.attributes('aria-label')).toContain('usage.baseCostHint')
    await hint.trigger('focusin')
    const tooltips = document.body.querySelectorAll<HTMLElement>('[role="tooltip"]')
    const tooltip = tooltips[tooltips.length - 1]
    expect(tooltip.textContent).toContain('usage.subscriptionUsageHint')
    expect(tooltip.textContent).toContain('usage.baseCostHint')
    expect(tooltip.style.display).not.toBe('none')
    wrapper.unmount()
  })
  it('omits the base cost row unless provided and shows unavailable base cost as a dash', () => {
    expect(mount(UsageBillingSummary, { props: { stats: {} } }).text()).not.toContain('usage.standardCost')
    expect(mount(UsageBillingSummary, { props: { stats: null, baseCost: null } }).text().replace(/\s+/g, '')).toContain('usage.standardCost—')
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
