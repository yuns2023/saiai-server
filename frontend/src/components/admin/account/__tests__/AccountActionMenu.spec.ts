import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountActionMenu from '../AccountActionMenu.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

function makeAccount(overrides: Partial<Account>): Account {
  return {
    id: 13,
    name: 'quota-account',
    platform: 'anthropic',
    type: 'setup-token',
    status: 'active',
    schedulable: true,
    rate_limited_at: null,
    rate_limit_reset_at: null,
    overload_until: null,
    temp_unschedulable_until: null,
    ...overrides
  } as Account
}

function mountMenu(account: Account) {
  return mount(AccountActionMenu, {
    props: {
      show: true,
      account,
      position: { top: 10, left: 10 }
    },
    global: {
      stubs: {
        Teleport: true,
        Icon: true
      }
    }
  })
}

describe('AccountActionMenu quota snapshot action', () => {
  it('shows the action for Anthropic setup-token accounts and emits the account', async () => {
    const account = makeAccount({})
    const wrapper = mountMenu(account)
    const button = wrapper.findAll('button').find((item) => item.text().includes('admin.accounts.clearQuotaSnapshot'))

    expect(button).toBeTruthy()
    await button!.trigger('click')
    expect(wrapper.emitted('clear-quota-snapshot')).toEqual([[account]])
  })

  it('shows the action for OpenAI OAuth accounts', () => {
    const wrapper = mountMenu(makeAccount({ platform: 'openai', type: 'oauth' }))
    expect(wrapper.text()).toContain('admin.accounts.clearQuotaSnapshot')
  })

  it('hides the action for API key accounts', () => {
    const wrapper = mountMenu(makeAccount({ type: 'apikey' }))
    expect(wrapper.text()).not.toContain('admin.accounts.clearQuotaSnapshot')
  })
})
