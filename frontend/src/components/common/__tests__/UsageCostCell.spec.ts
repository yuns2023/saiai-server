import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UsageCostCell from '../UsageCostCell.vue'
import UsageCostLegend from '../UsageCostLegend.vue'

const messages: Record<string, string> = {
  'usage.balanceCharge': '按量余额扣费',
  'usage.subscriptionUsage': '订阅额度消耗',
  'usage.unclassifiedUsage': '未分类消耗',
  'usage.serviceTier': '计费档位',
  'usage.serviceTierPriority': 'Fast',
  'usage.serviceTierFlex': 'Flex',
  'usage.serviceTierStandard': 'Standard',
  'usage.costDetails': '成本明细',
  'usage.accountBilled': '账号计费',
  'usage.accountBilledHint': 'ACCOUNT_HINT',
  'hostedUsage.reference': '折算用量',
  'hostedUsage.hint': 'REFERENCE_HINT',
  'usage.costLegendTitle': '费用图标说明',
}
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => messages[key] ?? key }) }))

const base = { actual_cost: 0.071854, total_cost: 0.071854, billing_type: 0, service_tier: null }
const mounted: { unmount: () => void }[] = []
const render = (row: Record<string, unknown>, props: Record<string, unknown> = {}) => {
  const wrapper = mount(UsageCostCell, { props: { row: { ...base, ...row }, ...props }, attachTo: document.body })
  mounted.push(wrapper)
  return wrapper
}

// Visible cell text, excluding the teleported tooltip bodies.
const cellText = (wrapper: ReturnType<typeof render>) => wrapper.get('[data-testid="usage-cost-cell"]').text()

describe('UsageCostCell', () => {
  afterEach(() => { mounted.splice(0).forEach((wrapper) => { try { wrapper.unmount() } catch { /* already unmounted */ } }) })

  it.each([
    [0, 'balance', '按量余额扣费'],
    [1, 'subscription', '订阅额度消耗'],
    [undefined, 'unclassified', '未分类消耗'],
  ])('renders billing type %s as an icon with an accessible label', (billingType, kind, label) => {
    const wrapper = render({ billing_type: billingType })
    const icon = wrapper.get('[data-testid="usage-cost-billing-icon"]')
    expect(icon.attributes('data-billing')).toBe(kind)
    expect(icon.attributes('aria-label')).toBe(label)
    expect(cellText(wrapper)).not.toContain(label)
    expect(wrapper.get('[data-testid="usage-cost-amount"]').text()).toBe('$0.071854')
  })

  it.each([[null], ['default'], ['standard']])('hides the service tier for %s', (tier) => {
    const wrapper = render({ service_tier: tier })
    expect(wrapper.find('[data-testid="usage-cost-tier-icon"]').exists()).toBe(false)
    expect(cellText(wrapper)).not.toContain('Standard')
  })

  it.each([['priority', 'Fast'], ['fast', 'Fast'], ['flex', 'Flex']])('shows an icon for the %s tier', (tier, label) => {
    const wrapper = render({ service_tier: tier })
    const icon = wrapper.get('[data-testid="usage-cost-tier-icon"]')
    expect(icon.attributes('aria-label')).toBe(`计费档位: ${label}`)
    expect(cellText(wrapper)).not.toContain(label)
  })

  it('keeps multiplier chips visible', () => {
    const wrapper = render({ model_rate_multiplier: 1.5, user_payg_discount_multiplier: 0.8 })
    expect(cellText(wrapper)).toContain('1.5x')
    expect(cellText(wrapper)).toContain('0.8x')
  })

  it('renders the hosted reference and admin account cost as compact explained amounts', async () => {
    const user = render({ metered_cost: 0.0123 })
    const reference = user.get('[data-testid="usage-cost-reference"]')
    expect(reference.text()).toBe('≈$0.012300')
    expect(reference.attributes('aria-label')).toContain('REFERENCE_HINT')
    expect(user.find('[data-testid="usage-cost-account"]').exists()).toBe(false)

    const admin = render({ metered_cost: 0.0123 }, { accountCost: 0.089818, showReference: false })
    expect(admin.find('[data-testid="usage-cost-reference"]').exists()).toBe(false)
    const account = admin.get('[data-testid="usage-cost-account"]')
    expect(account.text()).toBe('A $0.089818')
    expect(account.attributes('aria-label')).toContain('ACCOUNT_HINT')
    await account.trigger('click')
    const tooltips = [...document.body.querySelectorAll<HTMLElement>('[role="tooltip"]')]
    const open = tooltips.filter((node) => node.style.display !== 'none')
    expect(open.map((node) => node.textContent).join(' ')).toContain('ACCOUNT_HINT')
  })

  it('opens the cost breakdown on hover, focus and tap', async () => {
    const wrapper = render({})
    const detail = wrapper.get('[data-testid="usage-cost-detail"]')
    expect(detail.attributes('aria-label')).toBe('成本明细')
    await detail.trigger('mouseenter')
    await detail.trigger('mouseleave')
    await detail.trigger('focus')
    await detail.trigger('blur')
    await detail.trigger('click')
    expect(wrapper.emitted('show-detail')).toHaveLength(3)
    expect(wrapper.emitted('hide-detail')).toHaveLength(2)
    expect(detail.attributes('aria-expanded')).toBe('true')
  })

  it('keeps the breakdown open while focused and closes it on a second tap', async () => {
    const wrapper = render({})
    const detail = wrapper.get('[data-testid="usage-cost-detail"]')
    // touch sequence: emulated mouseenter -> focus -> click
    await detail.trigger('mouseenter')
    await detail.trigger('focus')
    await detail.trigger('click')
    await detail.trigger('mouseleave')
    expect(detail.attributes('aria-expanded')).toBe('true')
    await detail.trigger('click')
    expect(detail.attributes('aria-expanded')).toBe('false')
    expect(wrapper.emitted('show-detail')).toHaveLength(1)
    expect(wrapper.emitted('hide-detail')).toHaveLength(1)
  })

  it.each([
    ['an outside tap', () => document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))],
    ['Escape', () => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))],
    ['scroll', () => window.dispatchEvent(new Event('scroll'))],
  ])('closes a tapped-open breakdown on %s', async (_name, act) => {
    const wrapper = render({})
    const detail = wrapper.get('[data-testid="usage-cost-detail"]')
    await detail.trigger('click')
    act()
    await wrapper.vm.$nextTick()
    expect(detail.attributes('aria-expanded')).toBe('false')
    expect(wrapper.emitted('hide-detail')).toHaveLength(1)
  })

  it.each(['resize', 'orientationchange'])('closes a tapped-open breakdown on %s and removes its listeners', async (type) => {
    const removeSpy = vi.spyOn(window, 'removeEventListener')
    const wrapper = render({})
    const detail = wrapper.get('[data-testid="usage-cost-detail"]')
    await detail.trigger('click')
    window.dispatchEvent(new Event(type))
    await wrapper.vm.$nextTick()
    expect(detail.attributes('aria-expanded')).toBe('false')
    expect(wrapper.emitted('hide-detail')).toHaveLength(1)
    const removed = removeSpy.mock.calls.map(([name]) => name)
    expect(removed).toEqual(expect.arrayContaining(['scroll', 'resize', 'orientationchange']))
    removeSpy.mockRestore()
  })

  it('drops its state silently when another row takes the panel, then can re-claim it', async () => {
    const wrapper = render({})
    const detail = wrapper.get('[data-testid="usage-cost-detail"]')
    await detail.trigger('click')
    await wrapper.setProps({ detailActive: false })
    expect(detail.attributes('aria-expanded')).toBe('false')
    expect(wrapper.emitted('hide-detail')).toBeUndefined()
    await wrapper.setProps({ detailActive: true })
    await detail.trigger('mouseenter')
    expect(wrapper.emitted('show-detail')).toHaveLength(2)
  })

  it('hides the parent breakdown when the row unmounts while open', async () => {
    const wrapper = render({})
    await wrapper.get('[data-testid="usage-cost-detail"]').trigger('click')
    wrapper.unmount()
    expect(wrapper.emitted('hide-detail')).toHaveLength(1)
  })
})

describe('UsageCostLegend', () => {
  it('explains every icon in the column header tooltip', () => {
    const wrapper = mount(UsageCostLegend, { props: { label: '费用', showAccountCost: true, showReference: true }, attachTo: document.body })
    expect(wrapper.text()).toContain('费用')
    expect(wrapper.get('[data-testid="usage-cost-legend-trigger"]').attributes('aria-label')).toBe('费用图标说明')
    const tooltip = [...document.body.querySelectorAll<HTMLElement>('[role="tooltip"]')].pop()!
    for (const label of ['按量余额扣费', '订阅额度消耗', '未分类消耗', '计费档位: Fast', '计费档位: Flex', '账号计费', '折算用量']) {
      expect(tooltip.textContent).toContain(label)
    }
    wrapper.unmount()
  })
})
