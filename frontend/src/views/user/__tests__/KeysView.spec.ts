import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h, type PropType } from 'vue'
import type { ApiKey, Group } from '@/types'
import KeysView from '../KeysView.vue'

const mocks = vi.hoisted(() => ({
  list: vi.fn(), create: vi.fn(), update: vi.fn(), toggleStatus: vi.fn(), deleteKey: vi.fn(),
  getAvailable: vi.fn(), getUserGroupRates: vi.fn(), getPublicSettings: vi.fn(), getUsage: vi.fn(),
  showSuccess: vi.fn(), showError: vi.fn(), copyToClipboard: vi.fn(),
  isCurrentStep: vi.fn(), nextStep: vi.fn()
}))

vi.mock('@/api', () => ({
  keysAPI: { list: mocks.list, create: mocks.create, update: mocks.update, toggleStatus: mocks.toggleStatus, delete: mocks.deleteKey },
  userGroupsAPI: { getAvailable: mocks.getAvailable, getUserGroupRates: mocks.getUserGroupRates },
  authAPI: { getPublicSettings: mocks.getPublicSettings },
  usageAPI: { getDashboardApiKeysUsage: mocks.getUsage }
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: mocks.showSuccess, showError: mocks.showError }) }))
vi.mock('@/stores/onboarding', () => ({ useOnboardingStore: () => ({ isCurrentStep: mocks.isCurrentStep, nextStep: mocks.nextStep }) }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: mocks.copyToClipboard }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const groups = [
  { id: 1, name: 'Claude', platform: 'anthropic', status: 'active', subscription_type: 'payg', rate_multiplier: 1 },
  { id: 2, name: 'Codex', platform: 'openai', status: 'active', subscription_type: 'payg', rate_multiplier: 1 }
] as Group[]

function makeKey(overrides: Partial<ApiKey> = {}): ApiKey {
  return {
    id: 10, user_id: 1, name: 'Existing', key: 'TEST_ONLY_EXISTING_KEY', group_id: 1, group: groups[0],
    status: 'active', ip_whitelist: [], ip_blacklist: [], quota: 0, quota_used: 0,
    rate_limit_5h: 0, rate_limit_1d: 0, rate_limit_7d: 0, usage_5h: 0, usage_1d: 0, usage_7d: 0,
    window_5h_start: null, window_1d_start: null, window_7d_start: null,
    reset_5h_at: null, reset_1d_at: null, reset_7d_at: null,
    last_used_at: null, expires_at: null, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
    ...overrides
  }
}

const DataTableStub = defineComponent({
  props: {
    columns: { type: Array as PropType<{ key: string; label: string }[]>, required: true },
    data: { type: Array as PropType<ApiKey[]>, required: true }
  },
  setup(props, { slots }) {
    return () => h('table', [
      h('thead', h('tr', props.columns.map((column) => h('th', column.label)))),
      h('tbody', props.data.map((row) => h('tr', props.columns.map((column) => h('td',
        slots[`cell-${column.key}`]?.({ row, value: row[column.key as keyof ApiKey] }) ?? String(row[column.key as keyof ApiKey] ?? '')
      )))))
    ])
  }
})

const SelectStub = defineComponent({
  props: {
    modelValue: [String, Number, Boolean],
    options: { type: Array as PropType<{ value: string | number; label: string }[]>, default: () => [] }
  },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    return () => h('select', {
      value: String(props.modelValue ?? ''),
      onChange: (event: Event) => {
        const value = (event.target as HTMLSelectElement).value
        emit('update:modelValue', props.options.find((option) => String(option.value) === value)?.value ?? null)
      }
    }, [h('option', { value: '' }, 'Select'), ...props.options.map((option) => h('option', { value: option.value }, option.label))])
  }
})

const wrappers: VueWrapper[] = []
async function mountView() {
  const wrapper = mount(KeysView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="actions" /><slot name="table" /><slot name="pagination" /></div>' },
        BaseDialog: { props: ['show'], template: '<div v-if="show" role="dialog"><slot /><slot name="footer" /></div>' },
        DataTable: DataTableStub, Select: SelectStub,
        Icon: true, GroupBadge: true, GroupOptionItem: true, Pagination: true,
        SearchInput: true, EmptyState: true, ConfirmDialog: true
      }
    }
  })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

function buttonWithText(wrapper: VueWrapper, text: string) {
  const button = wrapper.findAll('button').find((candidate) => candidate.text() === text)
  if (!button) throw new Error(`Missing button: ${text}`)
  return button
}

async function fillCreateForm(wrapper: VueWrapper, groupId = 1) {
  await wrapper.find('[data-tour="keys-create-btn"]').trigger('click')
  await wrapper.find('[data-tour="key-form-name"]').setValue('New key')
  await wrapper.find('[data-tour="key-form-group"]').setValue(String(groupId))
}

describe('user KeysView setup guidance', () => {
  beforeEach(() => {
    Object.values(mocks).forEach((mock) => mock.mockReset())
    mocks.list.mockResolvedValue({ items: [makeKey()], total: 1, pages: 1 })
    mocks.getAvailable.mockResolvedValue(groups)
    mocks.getUserGroupRates.mockResolvedValue({})
    mocks.getPublicSettings.mockResolvedValue({ api_base_url: 'https://example.com' })
    mocks.getUsage.mockResolvedValue({ stats: {} })
    mocks.copyToClipboard.mockResolvedValue(true)
    mocks.isCurrentStep.mockReturnValue(false)
    mocks.create.mockResolvedValue(makeKey({ id: 99, name: 'New key', key: 'TEST_ONLY_CREATED_KEY' }))
    mocks.update.mockResolvedValue(makeKey())
  })

  afterEach(() => {
    wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
    vi.restoreAllMocks()
  })

  it('removes the raw-key column and makes Use Key primary', async () => {
    const wrapper = await mountView()
    expect(wrapper.findAll('th').map((header) => header.text())).not.toContain('keys.apiKey')
    expect(wrapper.text()).not.toContain('TEST_ONLY_EXISTING_KEY')
    expect(buttonWithText(wrapper, 'keys.useKey').classes()).toContain('btn-primary')
    expect(mocks.copyToClipboard).not.toHaveBeenCalled()
  })

  it('opens setup for the create response even when the refreshed list does not contain it', async () => {
    const wrapper = await mountView()
    await fillCreateForm(wrapper)
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.find('pre code').text()).toContain('TEST_ONLY_CREATED_KEY')
    expect(wrapper.text()).toContain('keys.useKeyModal.createdTitle')
    expect(wrapper.find('pre code').text()).not.toContain('TEST_ONLY_EXISTING_KEY')
    expect(mocks.copyToClipboard).not.toHaveBeenCalled()
  })

  it('resolves the created key platform from available groups when the response omits group details', async () => {
    mocks.create.mockResolvedValue(makeKey({ id: 99, key: 'TEST_ONLY_CODEX_KEY', group_id: 2, group: undefined }))
    const wrapper = await mountView()
    await fillCreateForm(wrapper, 2)
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(wrapper.find('pre code').text()).toContain("init-codex 'https://example.com/v1' 'TEST_ONLY_CODEX_KEY'")
    expect(wrapper.text()).not.toContain('keys.useKeyModal.noGroupTitle')
  })

  it('advances an active creation tour to the displayed setup command', async () => {
    mocks.isCurrentStep.mockReturnValue(true)
    const wrapper = await mountView()
    await fillCreateForm(wrapper)
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(mocks.nextStep).toHaveBeenCalledWith(500)
    expect(wrapper.find('[data-tour="key-setup-command"]').exists()).toBe(true)
  })

  it('keeps the create form open on failure without opening setup or advancing the tour', async () => {
    mocks.create.mockRejectedValue({ response: { data: { detail: 'TEST_ONLY_CREATE_FAILED' } } })
    mocks.isCurrentStep.mockReturnValue(true)
    const wrapper = await mountView()
    await fillCreateForm(wrapper)
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(wrapper.find('form').exists()).toBe(true)
    expect(wrapper.find('pre').exists()).toBe(false)
    expect(mocks.showError).toHaveBeenCalledWith('TEST_ONLY_CREATE_FAILED')
    expect(mocks.nextStep).not.toHaveBeenCalled()
  })

  it('does not open setup after editing an existing key', async () => {
    const wrapper = await mountView()
    await buttonWithText(wrapper, 'common.edit').trigger('click')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(mocks.update).toHaveBeenCalled()
    expect(mocks.create).not.toHaveBeenCalled()
    expect(wrapper.find('pre').exists()).toBe(false)
  })

  it('keeps raw reveal and copy behind More actions', async () => {
    const wrapper = await mountView()
    await buttonWithText(wrapper, 'keys.moreActions').trigger('click')
    expect(wrapper.text()).not.toContain('TEST_ONLY_EXISTING_KEY')
    await buttonWithText(wrapper, 'keys.copyKey').trigger('click')
    await flushPromises()
    expect(mocks.copyToClipboard).toHaveBeenCalledWith('TEST_ONLY_EXISTING_KEY', 'keys.copied')
    await buttonWithText(wrapper, 'keys.showKey').trigger('click')
    expect(wrapper.text()).toContain('TEST_ONLY_EXISTING_KEY')
    await buttonWithText(wrapper, 'common.close').trigger('click')
    expect(wrapper.text()).not.toContain('TEST_ONLY_EXISTING_KEY')
  })

  it('does not show a creation banner when reopening setup for an existing key', async () => {
    const wrapper = await mountView()
    await buttonWithText(wrapper, 'keys.useKey').trigger('click')
    expect(wrapper.find('pre code').text()).toContain('TEST_ONLY_EXISTING_KEY')
    expect(wrapper.text()).not.toContain('keys.useKeyModal.createdTitle')
  })
})
