import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

import AccountHostedBadges from '../AccountHostedBadges.vue'

describe('AccountHostedBadges', () => {
  it('renders nothing for an ordinary account or an older server', () => {
    expect(mount(AccountHostedBadges, { props: { account: {} } }).html()).not.toContain('badge')
    expect(mount(AccountHostedBadges, { props: { account: { self_onboarded: false, identity_unknown: false, owner_paused_at: null, owner_withdraw_requested_at: null } } }).html()).not.toContain('badge')
  })

  it('shows a badge per set field; owner states come from the timestamps', () => {
    const w = mount(AccountHostedBadges, {
      props: { account: { self_onboarded: true, identity_unknown: true, owner_paused_at: '2026-11-18T03:00:00Z', owner_withdraw_requested_at: '2026-11-18T04:00:00Z' } }
    })
    expect(w.get('[data-testid=account-self-onboarded]').text()).toBe('admin.accounts.selfOnboarded')
    expect(w.get('[data-testid=account-identity-unknown]').text()).toBe('admin.accounts.identityUnknown')
    expect(w.get('[data-testid=account-owner-paused]').text()).toBe('admin.accounts.ownerPaused')
    expect(w.get('[data-testid=account-withdraw-requested]').text()).toBe('admin.accounts.withdrawRequested')
  })
})
