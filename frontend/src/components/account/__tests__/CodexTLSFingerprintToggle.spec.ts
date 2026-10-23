import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, ref } from 'vue'
import { flushPromises, mount, shallowMount } from '@vue/test-utils'

const { createAccountMock, updateAccountMock } = vi.hoisted(() => ({
  createAccountMock: vi.fn(),
  updateAccountMock: vi.fn()
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isSimpleMode: true }) }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      create: createAccountMock,
      update: updateAccountMock,
      checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false }),
      listClaudeCarpoolDevices: vi.fn().mockResolvedValue([])
    }
  }
}))
vi.mock('@/composables/useOpenAIOAuth', () => ({
  useOpenAIOAuth: () => ({
    authUrl: ref(''),
    sessionId: ref('session'),
    oauthState: ref('state'),
    loading: ref(false),
    error: ref(''),
    resetState: vi.fn(),
    generateAuthUrl: vi.fn(),
    exchangeAuthCode: vi.fn().mockResolvedValue({ access_token: 'oauth-test-placeholder' }),
    validateRefreshToken: vi.fn(),
    buildCredentials: () => ({ access_token: 'oauth-test-placeholder' }),
    buildExtraInfo: () => ({})
  })
}))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

import CreateAccountModal from '../CreateAccountModal.vue'
import EditAccountModal from '../EditAccountModal.vue'

const BaseDialog = defineComponent({
  props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})
const codexToggle = '[data-testid="codex-tls-fingerprint-toggle"]'
const anthropicToggle = '[role="switch"][aria-label="admin.accounts.quotaControl.tlsFingerprint.label"]'

function mountCreate() {
  return shallowMount(CreateAccountModal, {
    props: { show: true, proxies: [], groups: [] },
    global: { stubs: { BaseDialog, OAuthAuthorizationFlow: defineComponent({
      name: 'OAuthAuthorizationFlow',
      data: () => ({ authCode: 'auth-code', oauthState: 'state', inputMethod: 'manual' }),
      methods: { reset() {} },
      template: '<div />'
    }) } }
  })
}

async function selectOpenAI(wrapper: ReturnType<typeof mountCreate>) {
  const openai = wrapper.findAll('[data-tour="account-form-platform"] button').find((b) => b.text().includes('OpenAI'))
  expect(openai, 'OpenAI platform button').toBeTruthy()
  await openai!.trigger('click')
  await flushPromises()
}

async function createOpenAIOAuth(wrapper: ReturnType<typeof mountCreate>) {
  await wrapper.get('[data-tour="account-form-name"]').setValue('Codex TLS')
  await wrapper.get('#create-account-form').trigger('submit.prevent')
  await flushPromises()
  const exchange = wrapper.findAll('button').find((b) => b.text().includes('admin.accounts.oauth.completeAuth'))
  expect(exchange, 'exchange button').toBeTruthy()
  await exchange!.trigger('click')
  await flushPromises()
}

describe('Create: OpenAI OAuth Codex TLS fingerprint', () => {
  beforeEach(() => createAccountMock.mockReset().mockResolvedValue({ id: 1 }))

  it('defaults off for OpenAI OAuth while the Anthropic default stays on', async () => {
    const wrapper = mountCreate()
    expect(wrapper.get(anthropicToggle).attributes('aria-checked')).toBe('true')
    expect(wrapper.find(codexToggle).exists()).toBe(false)
    await selectOpenAI(wrapper)
    expect(wrapper.get(codexToggle).attributes('aria-checked')).toBe('false')
    await createOpenAIOAuth(wrapper)
    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const payload = createAccountMock.mock.calls[0][0]
    expect(payload).toMatchObject({ platform: 'openai', type: 'oauth' })
    expect(payload.extra?.enable_tls_fingerprint).toBeUndefined()
    wrapper.unmount()
  })

  it('submits an explicit opt-in and resets to off on reopen', async () => {
    const wrapper = mountCreate()
    await selectOpenAI(wrapper)
    await wrapper.get(codexToggle).trigger('click')
    expect(wrapper.get(codexToggle).attributes('aria-checked')).toBe('true')
    await createOpenAIOAuth(wrapper)
    expect(createAccountMock).toHaveBeenCalledWith(expect.objectContaining({
      platform: 'openai', type: 'oauth', extra: expect.objectContaining({ enable_tls_fingerprint: true })
    }))
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await selectOpenAI(wrapper)
    expect(wrapper.get(codexToggle).attributes('aria-checked')).toBe('false')
    wrapper.unmount()
  })
})

function openAIOAuthAccount(extra: Record<string, unknown>, dtoFlag?: boolean) {
  return {
    id: 7, name: 'Codex OAuth', notes: '', platform: 'openai', type: 'oauth',
    credentials: { access_token: 'oauth-test-placeholder' }, extra,
    enable_tls_fingerprint: dtoFlag,
    proxy_id: null, concurrency: 1, priority: 1, rate_multiplier: 1, status: 'active',
    group_ids: [], expires_at: null, auto_pause_on_expired: false
  } as any
}

function mountEdit(account: any) {
  return mount(EditAccountModal, {
    props: { show: true, account, proxies: [], groups: [] },
    global: { stubs: { BaseDialog, Select: true, Icon: true, ProxySelector: true, GroupSelector: true, ModelWhitelistSelector: true } }
  })
}

describe('Edit: OpenAI OAuth Codex TLS fingerprint', () => {
  beforeEach(() => updateAccountMock.mockReset().mockImplementation(async (_id, payload) => ({ ...payload })))

  it('shows the DTO value and writes explicit false when turned off', async () => {
    const wrapper = mountEdit(openAIOAuthAccount({ enable_tls_fingerprint: true }, true))
    expect(wrapper.get(codexToggle).attributes('aria-checked')).toBe('true')
    await wrapper.get(codexToggle).trigger('click')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0][1].extra.enable_tls_fingerprint).toBe(false)
    wrapper.unmount()
  })

  it('keeps an untouched default-off account without the key', async () => {
    const wrapper = mountEdit(openAIOAuthAccount({}))
    expect(wrapper.get(codexToggle).attributes('aria-checked')).toBe('false')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[0][1].extra).not.toHaveProperty('enable_tls_fingerprint')
    wrapper.unmount()
  })

  it('opts in explicitly', async () => {
    const wrapper = mountEdit(openAIOAuthAccount({}))
    await wrapper.get(codexToggle).trigger('click')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[0][1].extra.enable_tls_fingerprint).toBe(true)
    wrapper.unmount()
  })
})
