import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const { updateAccountMock, checkMixedChannelRiskMock, listClaudeCarpoolDevicesMock } = vi.hoisted(() => ({
  updateAccountMock: vi.fn(),
  checkMixedChannelRiskMock: vi.fn(),
  listClaudeCarpoolDevicesMock: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    isSimpleMode: true
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      update: updateAccountMock,
      checkMixedChannelRisk: checkMixedChannelRiskMock,
      listClaudeCarpoolDevices: listClaudeCarpoolDevicesMock
    }
  }
}))

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn()
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

import EditAccountModal from '../EditAccountModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: {
    show: {
      type: Boolean,
      default: false
    }
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const ModelWhitelistSelectorStub = defineComponent({
  name: 'ModelWhitelistSelector',
  props: {
    modelValue: {
      type: Array,
      default: () => []
    }
  },
  emits: ['update:modelValue'],
  template: `
    <div>
      <button
        type="button"
        data-testid="rewrite-to-snapshot"
        @click="$emit('update:modelValue', ['gpt-5.2-2025-12-11'])"
      >
        rewrite
      </button>
      <span data-testid="model-whitelist-value">
        {{ Array.isArray(modelValue) ? modelValue.join(',') : '' }}
      </span>
    </div>
  `
})

function buildAccount() {
  return {
    id: 1,
    name: 'Anthropic Key',
    notes: '',
    platform: 'anthropic',
    type: 'apikey',
    credentials: {
      api_key: 'sk-ant-test',
      base_url: 'https://api.anthropic.com',
      model_mapping: {
        'claude-sonnet-4-5': 'claude-sonnet-4-5'
      }
    },
    extra: {},
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    rate_multiplier: 1,
    status: 'active',
    group_ids: [],
    expires_at: null,
    auto_pause_on_expired: false
  } as any
}

function mountModal(account = buildAccount()) {
  return mount(EditAccountModal, {
    props: {
      show: true,
      account,
      proxies: [],
      groups: []
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Select: true,
        Icon: true,
        ProxySelector: true,
        GroupSelector: true,
        ModelWhitelistSelector: ModelWhitelistSelectorStub
      }
    }
  })
}

describe('EditAccountModal', () => {
  it('reopening the same account rehydrates the model whitelist from props', async () => {
    const account = buildAccount()
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    expect(wrapper.get('[data-testid="model-whitelist-value"]').text()).toBe('claude-sonnet-4-5')

    await wrapper.get('[data-testid="rewrite-to-snapshot"]').trigger('click')
    expect(wrapper.get('[data-testid="model-whitelist-value"]').text()).toBe('gpt-5.2-2025-12-11')

    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })

    expect(wrapper.get('[data-testid="model-whitelist-value"]').text()).toBe('claude-sonnet-4-5')

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.model_mapping).toEqual({
      'claude-sonnet-4-5': 'claude-sonnet-4-5'
    })
  })

  it('persists unlimited carpool devices and hides the bounded registry', async () => {
    const account = {
      ...buildAccount(),
      type: 'oauth',
      credentials: { access_token: 'test-token' },
      extra: {
        claude_oauth_mode: 'carpool',
        claude_oauth_carpool_device_limit: 5
      },
      claude_oauth_mode: 'carpool',
      claude_oauth_carpool_device_limit: 5,
      claude_oauth_carpool_unlimited_devices: false
    } as any

    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    listClaudeCarpoolDevicesMock.mockReset()
    updateAccountMock.mockResolvedValue(account)
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    listClaudeCarpoolDevicesMock.mockResolvedValue({
      unlimited_devices: false,
      recorded_limit: 5,
      recorded_count: 0,
      overflow_count: 0,
      recorded_items: [],
      overflow_items: []
    })

    const wrapper = mountModal(account)
    await flushPromises()

    const unlimitedToggle = wrapper.get('[data-testid="carpool-unlimited-devices"]')
    expect((unlimitedToggle.element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.find('[data-testid="claude-oauth-current-limit"]').exists()).toBe(true)

    await unlimitedToggle.setValue(true)
    await flushPromises()

    expect(wrapper.find('[data-testid="claude-oauth-current-limit"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="carpool-unlimited-summary"]').exists()).toBe(true)

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).toMatchObject({
      claude_oauth_mode: 'carpool',
      claude_oauth_carpool_device_limit: 5,
      claude_oauth_carpool_unlimited_devices: true
    })
  })
})
