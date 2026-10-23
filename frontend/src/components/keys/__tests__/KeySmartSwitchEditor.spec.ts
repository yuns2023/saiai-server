import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import KeySmartSwitchEditor from '../KeySmartSwitchEditor.vue'
import type { Group, KeySmartSwitchPolicy } from '@/types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const groups = [
  { id: 1, name: 'Subscription', platform: 'anthropic', subscription_type: 'subscription', rate_multiplier: 1 },
  { id: 2, name: 'Balance', platform: 'anthropic', subscription_type: 'standard', rate_multiplier: 2 },
  { id: 3, name: 'Other service', platform: 'openai', rate_multiplier: 1 },
  { id: 4, name: 'Hosted', platform: 'anthropic', hosted_usage_enabled: true }
] as Group[]
const policy: KeySmartSwitchPolicy = { enabled: true, group_ids: [1, 2], payg_confirmed: true, version: 3 }
function editor() {
  return mount(KeySmartSwitchEditor, { props: { modelValue: policy, groups, groupId: 1 } })
}

describe('KeySmartSwitchEditor', () => {
  it('shows only compatible groups and retains the original priority on mount', () => {
    const wrapper = editor()
    expect(wrapper.text()).toContain('Subscription')
    expect(wrapper.text()).not.toContain('Other service')
    expect(wrapper.text()).not.toContain('Hosted')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('reorders without mutating the saved policy or revision', async () => {
    const wrapper = editor()
    await wrapper.find('button[aria-label="keys.smartSwitch.down"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual({ ...policy, group_ids: [2, 1] })
    expect(policy.group_ids).toEqual([1, 2])
  })

  it('distinguishes an empty budget from a zero budget', async () => {
    const wrapper = editor()
    await wrapper.find('input[type="number"]').setValue('0')
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toMatchObject({ payg_budget_usd: 0 })
    await wrapper.find('input[type="number"]').setValue('')
    expect(wrapper.emitted('update:modelValue')?.[1]?.[0]).toMatchObject({ payg_budget_usd: null })
  })

  it('retains missing selected groups so users can explicitly remove them', async () => {
    const wrapper = editor()
    await wrapper.setProps({ groups: [groups[0]] })
    expect(wrapper.findAll('li')).toHaveLength(2)
    expect(wrapper.text()).toContain('keys.smartSwitch.unavailable')
  })

  it('lets users turn the policy off when the original group is unavailable', async () => {
    const wrapper = mount(KeySmartSwitchEditor, { props: { modelValue: policy, groups: [groups[1]], groupId: 1 } })
    const toggle = wrapper.find('label input[type="checkbox"]')
    expect((toggle.element as HTMLInputElement).disabled).toBe(false)
    await toggle.setValue(false)
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual({ ...policy, enabled: false })
    await wrapper.setProps({ modelValue: { ...policy, enabled: false } })
    expect((wrapper.find('label input[type="checkbox"]').element as HTMLInputElement).disabled).toBe(true)
  })
})
