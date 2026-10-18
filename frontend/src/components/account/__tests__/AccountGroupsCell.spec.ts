import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountGroupsCell from '../AccountGroupsCell.vue'
import GroupBadge from '@/components/common/GroupBadge.vue'
import type { Group } from '@/types'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const groups = Array.from({ length: 8 }, (_, id) => ({ id, name: `Full group name ${id} with a long suffix`, platform: 'anthropic' })) as Group[]
describe('AccountGroupsCell', () => {
  it('shows every group and allows full names to wrap in account management', () => {
    const wrapper = mount(AccountGroupsCell, { props: { groups, showAll: true } })
    expect(wrapper.findAllComponents(GroupBadge)).toHaveLength(8)
    for (const group of groups) expect(wrapper.text()).toContain(group.name)
    expect(wrapper.findAllComponents(GroupBadge).every(badge => badge.props('wrapName'))).toBe(true)
    expect(wrapper.find('.overflow-hidden').exists()).toBe(false)
    expect(wrapper.find('button').exists()).toBe(false)
  })
  it('preserves compact display for other callers', () => {
    const wrapper = mount(AccountGroupsCell, { props: { groups } })
    expect(wrapper.findAllComponents(GroupBadge)).toHaveLength(3)
    expect(wrapper.find('button').text()).toBe('+5')
  })
})
