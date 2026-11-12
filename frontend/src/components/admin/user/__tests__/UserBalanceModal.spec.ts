import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const { updateBalanceMock, showErrorMock } = vi.hoisted(() => ({
  updateBalanceMock: vi.fn(),
  showErrorMock: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: showErrorMock,
    showSuccess: vi.fn()
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { users: { updateBalance: updateBalanceMock } }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

import UserBalanceModal from '../UserBalanceModal.vue'

const BaseDialogStub = defineComponent({
  props: { show: Boolean },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

describe('UserBalanceModal', () => {
  it('shows the server message from the API client error shape', async () => {
    updateBalanceMock.mockReset()
    showErrorMock.mockReset()
    // The API client rejects with { status, code, message } (client.ts).
    updateBalanceMock.mockRejectedValue({
      status: 400,
      code: 'BALANCE_NEGATIVE',
      message: 'balance cannot be negative, current balance: -10.00, requested operation would result in: -5.00'
    })
    const wrapper = mount(UserBalanceModal, {
      props: {
        show: true,
        operation: 'add',
        user: { id: 7, email: 'user@example.com', username: 'user', notes: '', balance: -10 } as any
      },
      global: { stubs: { BaseDialog: BaseDialogStub, UserIdentityTooltip: true } }
    })

    await wrapper.get('input[type="number"]').setValue('5')
    await wrapper.get('form#balance-form').trigger('submit.prevent')
    await flushPromises()

    expect(updateBalanceMock).toHaveBeenCalledWith(7, 5, 'add', '')
    expect(showErrorMock).toHaveBeenCalledWith(
      'balance cannot be negative, current balance: -10.00, requested operation would result in: -5.00'
    )
  })
})
