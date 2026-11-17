import { describe, expect, it, vi, beforeEach } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const { resetQuotaMock, showErrorMock } = vi.hoisted(() => ({
  resetQuotaMock: vi.fn(),
  showErrorMock: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: showErrorMock, showSuccess: vi.fn() })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { subscriptions: { resetQuota: resetQuotaMock } }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

import SubscriptionResetQuotaDialog from '../SubscriptionResetQuotaDialog.vue'

const BaseDialogStub = defineComponent({
  props: { show: Boolean },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

function subscription(overrides: Record<string, unknown> = {}) {
  return {
    id: 23,
    user: { email: 'user@example.com' },
    five_hour_usage_usd: 40,
    daily_usage_usd: 82,
    weekly_usage_usd: 300,
    monthly_usage_usd: 1000,
    group: { five_hour_limit_usd: 40, daily_limit_usd: 100, weekly_limit_usd: null, monthly_limit_usd: 1000 },
    ...overrides
  } as any
}

function mountDialog(sub = subscription()) {
  return mount(SubscriptionResetQuotaDialog, {
    props: { show: true, subscription: sub },
    global: { stubs: { BaseDialog: BaseDialogStub } }
  })
}

describe('SubscriptionResetQuotaDialog', () => {
  beforeEach(() => {
    resetQuotaMock.mockReset()
    resetQuotaMock.mockResolvedValue({})
    showErrorMock.mockReset()
  })

  it('lists only limited windows and preselects used-up ones except monthly', () => {
    const wrapper = mountDialog()
    const boxes = wrapper.findAll('input[type="checkbox"]')
    expect(boxes.map((b) => b.attributes('data-window'))).toEqual(['five_hour', 'daily', 'monthly'])
    const checked = Object.fromEntries(boxes.map((b) => [b.attributes('data-window'), (b.element as HTMLInputElement).checked]))
    expect(checked).toEqual({ five_hour: true, daily: false, monthly: false })
    expect(wrapper.find('[data-testid="monthly-warning"]').exists()).toBe(false)
  })

  it('sends only the chosen windows with the trimmed note', async () => {
    const wrapper = mountDialog()
    await wrapper.get('input[data-window="monthly"]').setValue(true)
    expect(wrapper.find('[data-testid="monthly-warning"]').exists()).toBe(true)
    await wrapper.get('textarea').setValue('  上游故障补偿  ')
    await wrapper.get('form#reset-quota-form').trigger('submit.prevent')
    await flushPromises()

    expect(resetQuotaMock).toHaveBeenCalledWith(23, {
      five_hour: true,
      daily: false,
      weekly: false,
      monthly: true,
      notes: '上游故障补偿'
    })
    expect(wrapper.emitted('reset')).toHaveLength(1)
  })

  it('refuses to submit with nothing selected', async () => {
    const wrapper = mountDialog(subscription({ five_hour_usage_usd: 1 }))
    await wrapper.get('form#reset-quota-form').trigger('submit.prevent')
    await flushPromises()
    expect(resetQuotaMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('admin.subscriptions.resetSelectAtLeastOne')
  })

  it('explains when the group has no limits', () => {
    const wrapper = mountDialog(subscription({ group: {} }))
    expect(wrapper.findAll('input[type="checkbox"]')).toHaveLength(0)
    expect(wrapper.text()).toContain('admin.subscriptions.resetNoLimits')
  })

  it('stays open while the request is in flight', async () => {
    let resolve!: () => void
    resetQuotaMock.mockReturnValue(new Promise<void>((r) => { resolve = r }))
    const wrapper = mountDialog()
    await wrapper.get('form#reset-quota-form').trigger('submit.prevent')
    await wrapper.get('button.btn-secondary').trigger('click')
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.get('button.btn-secondary').attributes('disabled')).toBeDefined()

    resolve()
    await flushPromises()
    expect(wrapper.emitted('reset')).toHaveLength(1)
    await wrapper.get('button.btn-secondary').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('shows the server message from the API client error shape', async () => {
    resetQuotaMock.mockRejectedValue({ status: 400, code: 'ADMIN_NOTES_TOO_LONG', message: 'notes must be at most 500 characters' })
    const wrapper = mountDialog()
    await wrapper.get('form#reset-quota-form').trigger('submit.prevent')
    await flushPromises()
    expect(showErrorMock).toHaveBeenCalledWith('notes must be at most 500 characters')
    expect(wrapper.emitted('reset')).toBeUndefined()
  })
})
