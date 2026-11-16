import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountOwnerCell from '../AccountOwnerCell.vue'

describe('AccountOwnerCell', () => {
  it('shows the owner email with the user ID', () => {
    const wrapper = mount(AccountOwnerCell, { props: { ownerUserId: 5, ownerEmail: 'alice@example.com' } })
    expect(wrapper.get('[data-testid="account-owner"]').text()).toContain('alice@example.com')
    expect(wrapper.text()).toContain('#5')
    expect(wrapper.find('[data-testid="account-owner-empty"]').exists()).toBe(false)
  })

  it('falls back to the user ID when the email is missing', () => {
    const wrapper = mount(AccountOwnerCell, { props: { ownerUserId: 7, ownerEmail: null } })
    expect(wrapper.get('[data-testid="account-owner"]').text()).toBe('#7')
  })

  it('shows a dash when the account has no owner', () => {
    for (const ownerUserId of [null, undefined]) {
      const wrapper = mount(AccountOwnerCell, { props: { ownerUserId, ownerEmail: null } })
      expect(wrapper.find('[data-testid="account-owner"]').exists()).toBe(false)
      expect(wrapper.get('[data-testid="account-owner-empty"]').text()).toBe('-')
    }
  })
})
