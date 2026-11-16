import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AccountOwnerModal from '../AccountOwnerModal.vue'
import type { Account } from '@/types'

const api = vi.hoisted(() => ({ setAccountOwner: vi.fn() }))
const store = vi.hoisted(() => ({ showSuccess: vi.fn() }))
vi.mock('@/api/admin/accounts', () => ({ accountsAPI: api, ACCOUNT_OWNER_TRANSFER_FORBIDDEN: 'ACCOUNT_OWNER_TRANSFER_FORBIDDEN' }))
vi.mock('@/api/admin', () => ({ adminAPI: { users: { list: vi.fn() } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => store }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const makeModal = (account: Partial<Account>) => mount(AccountOwnerModal, {
  props: { show: true, account: { id: 12, name: 'acct', platform: 'anthropic', type: 'setup-token', ...account } as Account },
  global: {
    stubs: {
      BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
      // Stand-in for the reused user picker: "picks" user 9.
      CarpoolUserSelector: {
        props: { modelValue: Array, single: Boolean },
        emits: ['update:modelValue'],
        template: '<button data-testid="pick" :data-single="String(single)" @click="$emit(\'update:modelValue\', [9])">{{ modelValue.join(",") }}</button>'
      }
    }
  }
})

beforeEach(() => {
  vi.clearAllMocks()
})

describe('AccountOwnerModal', () => {
  it('explains the transfer rule when the server returns 409 ACCOUNT_OWNER_TRANSFER_FORBIDDEN', async () => {
    api.setAccountOwner.mockRejectedValue({ status: 409, reason: 'ACCOUNT_OWNER_TRANSFER_FORBIDDEN', message: 'conflict' })
    const wrapper = makeModal({ owner_user_id: null, owner_email: null })
    await wrapper.get('[data-testid="pick"]').trigger('click')
    await wrapper.get('[data-testid="owner-save"]').trigger('click')
    await flushPromises()
    expect(api.setAccountOwner).toHaveBeenCalledWith(12, 9)
    expect(wrapper.get('[data-testid="owner-error"]').text()).toBe('admin.accounts.owner.transferForbidden')
    expect(wrapper.emitted('updated')).toBeUndefined()
    expect(wrapper.emitted('close')).toBeUndefined()
  })

  it('shows the server message for other errors', async () => {
    api.setAccountOwner.mockRejectedValue({ status: 404, message: 'user not found' })
    const wrapper = makeModal({ owner_user_id: null, owner_email: null })
    await wrapper.get('[data-testid="pick"]').trigger('click')
    await wrapper.get('[data-testid="owner-save"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="owner-error"]').text()).toBe('user not found')
  })

  it('saves the picked user with the single-select picker and reports the result', async () => {
    const result = { account_id: 12, owner_user_id: 9, owner_email: 'nine@example.com' }
    api.setAccountOwner.mockResolvedValue(result)
    const wrapper = makeModal({ owner_user_id: null, owner_email: null })
    expect(wrapper.get('[data-testid="pick"]').attributes('data-single')).toBe('true')
    expect(wrapper.get('[data-testid="owner-save"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="owner-clear"]').exists()).toBe(false)
    await wrapper.get('[data-testid="pick"]').trigger('click')
    await wrapper.get('[data-testid="owner-save"]').trigger('click')
    await flushPromises()
    expect(wrapper.emitted('updated')).toEqual([[result]])
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(store.showSuccess).toHaveBeenCalledWith('admin.accounts.owner.saved')
  })

  it('shows the current owner and clears it by sending null', async () => {
    api.setAccountOwner.mockResolvedValue({ account_id: 12, owner_user_id: null, owner_email: null })
    const wrapper = makeModal({ owner_user_id: 5, owner_email: 'five@example.com' })
    expect(wrapper.get('[data-testid="owner-current"]').text()).toBe('admin.accounts.owner.current')
    // The current owner is preselected, so saving it again is pointless.
    expect(wrapper.get('[data-testid="pick"]').text()).toBe('5')
    expect(wrapper.get('[data-testid="owner-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="owner-clear"]').trigger('click')
    await flushPromises()
    expect(api.setAccountOwner).toHaveBeenCalledWith(12, null)
    expect(wrapper.emitted('updated')).toEqual([[{ account_id: 12, owner_user_id: null, owner_email: null }]])
    expect(store.showSuccess).toHaveBeenCalledWith('admin.accounts.owner.cleared')
  })
})
