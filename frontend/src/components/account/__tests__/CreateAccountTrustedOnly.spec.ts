import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, shallowMount } from '@vue/test-utils'

const { createAccountMock } = vi.hoisted(() => ({ createAccountMock: vi.fn() }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isSimpleMode: true }) }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { create: createAccountMock, checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false }) } } }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

import CreateAccountModal from '../CreateAccountModal.vue'
import OAuthAuthorizationFlow from '../OAuthAuthorizationFlow.vue'

function mountModal() {
  return shallowMount(CreateAccountModal, {
    props: { show: true, proxies: [], groups: [] },
    global: { stubs: { OAuthAuthorizationFlow: defineComponent({
      name: 'OAuthAuthorizationFlow', methods: { reset() {} }, template: '<div />'
    }), BaseDialog: defineComponent({
      props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>'
    }) } }
  })
}

describe('new trusted-only carpool account', () => {
  beforeEach(() => createAccountMock.mockReset().mockResolvedValue({ id: 1 }))
  it.each(['oauth', 'setup-token'])('submits the restriction for %s and resets on reopen', async (kind) => {
    const wrapper = mountModal()
    const toggle = wrapper.get('[data-testid="carpool-trusted-only"]')
    expect((toggle.element as HTMLInputElement).checked).toBe(false)
    await toggle.setValue(true)
    await wrapper.get('[data-tour="account-form-name"]').setValue('Trusted account')
    await wrapper.get('#create-account-form').trigger('submit.prevent')
    await flushPromises()
    const flow = wrapper.getComponent(OAuthAuthorizationFlow)
    if (kind === 'setup-token') flow.vm.$emit('import-claude-setup-token', 'sk-ant-test-placeholder')
    else flow.vm.$emit('import-claude-credentials-json', JSON.stringify({ claudeAiOauth: {
      accessToken: 'sk-ant-test-placeholder', refreshToken: 'sk-ant-test-placeholder', expiresAt: 2000000000000, scopes: ['user:inference']
    } }))
    await flushPromises()
    expect(createAccountMock).toHaveBeenCalledWith(expect.objectContaining({
      type: kind, extra: expect.objectContaining({ claude_oauth_carpool_trusted_only: true })
    }))
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    expect((wrapper.get('[data-testid="carpool-trusted-only"]').element as HTMLInputElement).checked).toBe(false)
    wrapper.unmount()
  })
})
