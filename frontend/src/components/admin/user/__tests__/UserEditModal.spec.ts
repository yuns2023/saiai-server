import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const { updateUserMock } = vi.hoisted(() => ({
  updateUserMock: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn()
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: { update: updateUserMock },
    userAttributes: { updateUserAttributeValues: vi.fn() }
  }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

import UserEditModal from '../UserEditModal.vue'

const BaseDialogStub = defineComponent({
  props: { show: Boolean },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

function mountModal() {
  return mount(UserEditModal, {
    props: {
      show: true,
      user: {
        id: 7,
        email: 'user@example.com',
        username: 'user',
        notes: '',
        role: 'user',
        balance: 10,
        concurrency: 2,
        status: 'active',
        allowed_groups: [],
        payg_discount_multiplier: 0.9,
        sora_storage_quota_bytes: 0,
        sora_storage_used_bytes: 0,
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-01T00:00:00Z'
      }
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        UserAttributeForm: true,
        Icon: true
      }
    }
  })
}

describe('UserEditModal', () => {
  it('loads and updates the user-owned PAYG discount', async () => {
    updateUserMock.mockReset()
    updateUserMock.mockResolvedValue({})
    const wrapper = mountModal()
    const input = wrapper.get('[data-testid="user-payg-discount"]')

    expect((input.element as HTMLInputElement).value).toBe('0.9')
    await input.setValue('0.75')
    await wrapper.get('form#edit-user-form').trigger('submit.prevent')
    await flushPromises()

    expect(updateUserMock).toHaveBeenCalledWith(7, expect.objectContaining({
      payg_discount_multiplier: 0.75
    }))
  })
})
