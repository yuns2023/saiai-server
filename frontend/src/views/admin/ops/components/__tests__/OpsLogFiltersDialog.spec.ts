import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import type { OpsAdvancedSettings, OpsLogFilterConfig, OpsLogFilterRule } from '@/api/admin/ops'
import OpsLogFiltersDialog from '../OpsLogFiltersDialog.vue'
import { legacyLogFilters } from '../../utils/opsLogFilters'

const mockAPI = vi.hoisted(() => ({
  getLogFilters: vi.fn(), updateLogFilters: vi.fn(), getLogFilterProposal: vi.fn(),
  getAdvancedSettings: vi.fn(), updateAdvancedSettings: vi.fn()
}))
const getAllGroups = vi.hoisted(() => vi.fn())
vi.mock('@/api/admin/ops', () => ({ opsAPI: mockAPI }))
vi.mock('@/api/admin/groups', () => ({ groupsAPI: { getAll: getAllGroups } }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => `${key}${params ? ` ${JSON.stringify(params)}` : ''}` })
}))

const DialogStub = defineComponent({
  props: ['show'], emits: ['close'],
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})
const rule: OpsLogFilterRule = {
  id: 'local-user', name: 'Disabled user', enabled: false,
  source: 'local_auth', reason: 'user_inactive', group_id: 7, match_mode: 'all'
}
const emptyConfig: OpsLogFilterConfig = { rules: [], revision: 'revision-1', counts: {}, count_scope: 'process' }
const advanced: OpsAdvancedSettings = {
  data_retention: { cleanup_enabled: false, cleanup_schedule: '0 0 * * *', error_log_retention_days: 30, minute_metrics_retention_days: 30, hourly_metrics_retention_days: 30 },
  aggregation: { aggregation_enabled: true },
  ignore_count_tokens_errors: false, ignore_context_canceled: false, ignore_no_available_accounts: false,
  ignore_invalid_api_key_errors: false, ignore_insufficient_balance_errors: false,
  display_openai_token_stats: false, display_alert_events: true, auto_refresh_enabled: false, auto_refresh_interval_seconds: 30
}

function mountDialog(props: { groupId?: number | null; platform?: string; errorId?: number; show?: boolean } = {}) {
  return mount(OpsLogFiltersDialog, { props: { show: true, ...props }, global: { stubs: { BaseDialog: DialogStub } } })
}

describe('OpsLogFiltersDialog', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    mockAPI.getLogFilters.mockResolvedValue(emptyConfig)
    getAllGroups.mockResolvedValue([
      { id: 7, name: 'Claude team', platform: 'anthropic' },
      { id: 8, name: 'Backup team', platform: 'anthropic' },
      { id: 99, name: 'OpenAI team', platform: 'openai' }
    ])
    mockAPI.getAdvancedSettings.mockResolvedValue(advanced)
    mockAPI.getLogFilterProposal.mockResolvedValue({ rule: null, verified: false, requires_global_confirmation: false })
    mockAPI.updateLogFilters.mockImplementation(async (payload: { rules: OpsLogFilterRule[] }) => ({ ...emptyConfig, ...payload, revision: 'revision-2' }))
    mockAPI.updateAdvancedSettings.mockImplementation(async (payload: OpsAdvancedSettings) => payload)
  })

  it('starts empty, shows process-only counts and makes no save request on load', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    expect(wrapper.text()).toContain('admin.ops.logFilters.empty')
    expect(wrapper.text()).toContain('admin.ops.logFilters.countScope')
    expect(wrapper.text()).toContain('admin.ops.logFilters.futureOnly')
    expect(mockAPI.updateLogFilters).not.toHaveBeenCalled()
    expect(mockAPI.getLogFilterProposal).not.toHaveBeenCalled()
  })

  it('previews a disabled group preset and enables/persists with one confirmation', async () => {
    const wrapper = mountDialog({ groupId: 7, platform: 'anthropic' })
    await flushPromises()
    await wrapper.get('[data-testid="preset-user_inactive"]').trigger('click')
    expect(wrapper.get('[data-testid="scope"]').element).toHaveProperty('value', 'group')
    expect(wrapper.get('[data-testid="group"]').text()).toContain('Claude team (#7)')
    expect(wrapper.get('[data-testid="apply-rule"]').text()).toBe('admin.ops.logFilters.confirmEnable')
    expect(mockAPI.updateLogFilters).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    await flushPromises()
    expect(mockAPI.updateLogFilters).toHaveBeenCalledWith({ revision: 'revision-1', rules: [expect.objectContaining({ enabled: true, source: 'local_auth', reason: 'user_inactive', group_id: 7 })] })
    expect(mockAPI.updateLogFilters.mock.calls[0][0].rules[0].platform).toBeUndefined()
    expect(wrapper.emitted('saved')).toHaveLength(1)
  })

  it('uses platform scope for the invalid key preset', async () => {
    const wrapper = mountDialog({ platform: 'openai' })
    await flushPromises()
    await wrapper.get('[data-testid="preset-invalid_api_key"]').trigger('click')
    expect(wrapper.get('[data-testid="scope"]').element).toHaveProperty('value', 'platform')
    expect(wrapper.get('[data-testid="platform"]').element).toHaveProperty('value', 'openai')
    expect(wrapper.get('[data-testid="platform"]').text()).toContain('OpenAI')
    expect(wrapper.get('[data-testid="platform"]').element.tagName).toBe('SELECT')
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    await flushPromises()
    expect(mockAPI.updateLogFilters.mock.calls[0][0].rules[0]).toMatchObject({ enabled: true, source: 'local_auth', reason: 'invalid_api_key', platform: 'openai' })
  })

  it.each([
    { platform: undefined, expectedPlatform: 'anthropic' },
    { platform: 'openai', expectedPlatform: 'openai' }
  ])('defaults a Key preset to $expectedPlatform rather than the selected dashboard group', async ({ platform, expectedPlatform }) => {
    const wrapper = mountDialog({ groupId: 7, platform })
    await flushPromises()
    await wrapper.get('[data-testid="preset-invalid_api_key"]').trigger('click')
    expect(wrapper.get('[data-testid="scope"]').element).toHaveProperty('value', 'platform')
    expect(wrapper.get('[data-testid="platform"]').element).toHaveProperty('value', expectedPlatform)
    expect(wrapper.get('[data-testid="scope"] option[value="group"]').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('admin.ops.logFilters.keyReasonScopeHint')
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    await flushPromises()
    expect(mockAPI.updateLogFilters.mock.calls[0][0].rules[0]).toMatchObject({ enabled: true, source: 'local_auth', reason: 'invalid_api_key', platform: expectedPlatform })
    expect(mockAPI.updateLogFilters.mock.calls[0][0].rules[0].group_id).toBeUndefined()
  })

  it.each(['invalid_api_key', 'api_key_required'] as const)('requires explicit scope correction when a group-scoped user rule changes to %s', async reason => {
    const wrapper = mountDialog({ groupId: 7, platform: 'anthropic' })
    await flushPromises()
    await wrapper.get('[data-testid="preset-user_inactive"]').trigger('click')
    await wrapper.get('[data-testid="reason"]').setValue(reason)
    expect(wrapper.get('[data-testid="scope"]').element).toHaveProperty('value', 'group')
    expect(wrapper.get('[data-testid="scope"] option[value="group"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="group"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    expect(wrapper.text()).toContain('admin.ops.logFilters.validation.keyReasonGroupScope')
    expect(mockAPI.updateLogFilters).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="scope"]').setValue('platform')
    await wrapper.get('[data-testid="platform"]').setValue('anthropic')
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    await flushPromises()
    expect(mockAPI.updateLogFilters.mock.calls[0][0].rules[0]).toMatchObject({ enabled: true, source: 'local_auth', reason, platform: 'anthropic' })
    expect(mockAPI.updateLogFilters.mock.calls[0][0].rules[0].group_id).toBeUndefined()
  })

  it('requires explicit global confirmation before a new rule can be added', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get('[data-testid="preset-user_inactive"]').trigger('click')
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    expect(wrapper.text()).toContain('admin.ops.logFilters.validation.globalConfirmation')
    expect(wrapper.get('[data-testid="save-rules"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="confirm-global"]').setValue(true)
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    await flushPromises()
    expect(mockAPI.updateLogFilters.mock.calls[0][0].rules[0]).toMatchObject({ enabled: true, source: 'local_auth', reason: 'user_inactive' })
  })

  it.each([
    { scope: 'group', field: 'group', value: 7, expected: { group_id: 7, platform: undefined } },
    { scope: 'platform', field: 'platform', value: 'anthropic', expected: { group_id: undefined, platform: 'anthropic' } }
  ])('requires consent only for the final scope when a global detail proposal narrows to $scope', async ({ scope, field, value, expected }) => {
    mockAPI.getLogFilterProposal.mockResolvedValue({
      rule: { ...rule, group_id: undefined, platform: undefined },
      verified: false,
      requires_global_confirmation: true
    })
    const wrapper = mountDialog({ errorId: 47 })
    await flushPromises()
    expect(wrapper.find('[data-testid="confirm-global"]').exists()).toBe(true)
    await wrapper.get('[data-testid="scope"]').setValue(scope)
    await wrapper.get(`[data-testid="${field}"]`).setValue(value)
    expect(wrapper.find('[data-testid="confirm-global"]').exists()).toBe(false)
    await wrapper.get('[data-testid="scope"]').setValue('global')
    expect(wrapper.get('[data-testid="confirm-global"]').element).toHaveProperty('checked', false)
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    expect(wrapper.text()).toContain('admin.ops.logFilters.validation.globalConfirmation')
    expect(mockAPI.updateLogFilters).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="scope"]').setValue(scope)
    expect(wrapper.find('[data-testid="confirm-global"]').exists()).toBe(false)
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    await flushPromises()
    expect(mockAPI.updateLogFilters).toHaveBeenCalledWith({
      revision: 'revision-1',
      rules: [expect.objectContaining({ enabled: true, source: 'local_auth', reason: 'user_inactive', ...expected })]
    })
  })

  it('prefills only the backend source and historical scope and warns for unverified local hints', async () => {
    mockAPI.getLogFilterProposal.mockResolvedValue({ rule: { ...rule, platform: 'anthropic' }, verified: false, requires_global_confirmation: false })
    const wrapper = mountDialog({ errorId: 42, groupId: 99, platform: 'openai' })
    await flushPromises()
    expect(mockAPI.getLogFilterProposal).toHaveBeenCalledWith(42, expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(wrapper.text()).toContain('admin.ops.logFilters.unverified')
    expect(wrapper.get('[data-testid="group"]').element).toHaveProperty('value', '7')
    expect(wrapper.get('[data-testid="group"]').text()).toContain('Claude team (#7)')
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    await flushPromises()
    expect(mockAPI.updateLogFilters.mock.calls[0][0].rules[0]).toMatchObject({ enabled: true, source: 'local_auth', reason: 'user_inactive', group_id: 7, platform: 'anthropic' })
  })

  it('opens advanced upstream editing, sanitizes keywords and disallows status-only rules', async () => {
    mockAPI.getLogFilterProposal.mockResolvedValue({ rule: { ...rule, source: 'upstream', status_codes: [403], keywords: ['Denied Bearer secret-token'] }, verified: true, requires_global_confirmation: false })
    const wrapper = mountDialog({ errorId: 43 })
    await flushPromises()
    expect(wrapper.get('section details').attributes('open')).toBeDefined()
    expect(wrapper.text()).toContain('admin.ops.logFilters.upstreamWarning')
    expect(wrapper.text()).toContain('admin.ops.logFilters.verifiedUpstream')
    expect(wrapper.get('[data-testid="keywords"]').element).toHaveProperty('value', 'Denied [redacted]')
    expect(wrapper.get('[data-testid="match-mode"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="keywords"]').setValue('')
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    expect(wrapper.text()).toContain('admin.ops.logFilters.validation.unsafeUpstream')
    expect(mockAPI.updateLogFilters).not.toHaveBeenCalled()
  })

  it('preserves unverified upstream provenance without displaying local or verified-source claims', async () => {
    mockAPI.getLogFilterProposal.mockResolvedValue({ rule: { ...rule, source: 'upstream', reason: undefined, status_codes: [403], keywords: ['Denied'] }, verified: false, requires_global_confirmation: false })
    const wrapper = mountDialog({ errorId: 46 })
    await flushPromises()
    expect(wrapper.text()).toContain('admin.ops.logFilters.unverifiedUpstream')
    expect(wrapper.text()).not.toContain('admin.ops.logFilters.verifiedUpstream')
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    await flushPromises()
    expect(mockAPI.updateLogFilters.mock.calls[0][0].rules[0]).toMatchObject({ source: 'upstream', enabled: true, status_codes: [403], keywords: ['Denied'], match_mode: 'all' })
  })

  it('toggles scoped filters on and off and displays instance counts', async () => {
    mockAPI.getLogFilters.mockResolvedValue({ ...emptyConfig, rules: [rule], counts: { [rule.id]: 12 } })
    const wrapper = mountDialog()
    await flushPromises()
    expect(wrapper.text()).toContain('"count":12')
    await wrapper.get(`[data-testid="rule-${rule.id}"] [role="switch"]`).trigger('click')
    expect(wrapper.text()).toContain('admin.ops.logFilters.pendingChanges')
    await wrapper.get('[data-testid="save-rules"]').trigger('click')
    await flushPromises()
    expect(mockAPI.updateLogFilters.mock.calls[0][0].rules[0].enabled).toBe(true)
    await wrapper.get(`[data-testid="rule-${rule.id}"] [role="switch"]`).trigger('click')
    await wrapper.get('[data-testid="save-rules"]').trigger('click')
    await flushPromises()
    expect(mockAPI.updateLogFilters.mock.calls[1][0]).toMatchObject({ revision: 'revision-2', rules: [{ enabled: false }] })
  })

  it('requires review when enabling an existing global rule', async () => {
    mockAPI.getLogFilters.mockResolvedValue({ ...emptyConfig, rules: [{ ...rule, group_id: undefined }] })
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get(`[data-testid="rule-${rule.id}"] [role="switch"]`).trigger('click')
    expect(wrapper.find('[data-testid="confirm-global"]').exists()).toBe(true)
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    expect(wrapper.text()).toContain('admin.ops.logFilters.validation.globalConfirmation')
    expect(mockAPI.updateLogFilters).not.toHaveBeenCalled()
  })

  it('clears global confirmation if the rejection reason changes', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get('[data-testid="preset-user_inactive"]').trigger('click')
    await wrapper.get('[data-testid="confirm-global"]').setValue(true)
    await wrapper.get('[data-testid="reason"]').setValue('invalid_api_key')
    expect(wrapper.get('[data-testid="confirm-global"]').element).toHaveProperty('checked', false)
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    expect(wrapper.text()).toContain('admin.ops.logFilters.validation.globalConfirmation')
  })

  it('removes a rule from the saved configuration without touching historical logs', async () => {
    mockAPI.getLogFilters.mockResolvedValue({ ...emptyConfig, rules: [rule] })
    const wrapper = mountDialog()
    await flushPromises()
    const remove = wrapper.get(`[data-testid="rule-${rule.id}"]`).findAll('button').find(button => button.text() === 'admin.ops.logFilters.removeRule')
    await remove!.trigger('click')
    await wrapper.get('[data-testid="save-rules"]').trigger('click')
    await flushPromises()
    expect(mockAPI.updateLogFilters).toHaveBeenCalledWith({ revision: 'revision-1', rules: [] })
  })

  it('does not create more than 50 rules or permit duplicate loaded IDs to be saved', async () => {
    mockAPI.getLogFilters.mockResolvedValue({ ...emptyConfig, rules: Array.from({ length: 50 }, (_, index) => ({ ...rule, id: `rule-${index}` })) })
    const wrapper = mountDialog()
    await flushPromises()
    expect(wrapper.get('[data-testid="preset-user_inactive"]').attributes('disabled')).toBeDefined()
    mockAPI.getLogFilters.mockResolvedValue({ ...emptyConfig, rules: [rule, { ...rule }] })
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    await wrapper.get(`[data-testid="rule-${rule.id}"] [role="switch"]`).trigger('click')
    await wrapper.get('[data-testid="save-rules"]').trigger('click')
    expect(wrapper.text()).toContain('admin.ops.logFilters.validation.ruleLimit')
    expect(mockAPI.updateLogFilters).not.toHaveBeenCalled()
  })

  it('blocks stale saves on 409 and reloads without automatically overwriting', async () => {
    mockAPI.getLogFilters.mockResolvedValue({ ...emptyConfig, rules: [rule] })
    mockAPI.updateLogFilters.mockRejectedValue({ status: 409 })
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get(`[data-testid="rule-${rule.id}"] [role="switch"]`).trigger('click')
    await wrapper.get('[data-testid="save-rules"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.ops.logFilters.conflict')
    expect(wrapper.get('[data-testid="save-rules"]').attributes('disabled')).toBeDefined()
    expect(mockAPI.updateLogFilters).toHaveBeenCalledTimes(1)
    mockAPI.getLogFilters.mockResolvedValue({ ...emptyConfig, revision: 'revision-3', rules: [{ ...rule, name: 'Another admin edit' }] })
    await wrapper.get('[data-testid="reload"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Another admin edit')
    expect(wrapper.get(`[data-testid="rule-${rule.id}"] [role="switch"]`).attributes('aria-checked')).toBe('false')
    expect(mockAPI.updateLogFilters).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[data-testid="save-rules"]').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).not.toContain('admin.ops.logFilters.conflict')
  })

  it('disables saves when loading fails and safely retries', async () => {
    mockAPI.getLogFilters.mockRejectedValue(new Error('unavailable'))
    const wrapper = mountDialog()
    await flushPromises()
    expect(wrapper.text()).toContain('admin.ops.logFilters.loadFailed')
    expect(wrapper.get('[data-testid="save-rules"]').attributes('disabled')).toBeDefined()
    expect(mockAPI.updateLogFilters).not.toHaveBeenCalled()
    mockAPI.getLogFilters.mockResolvedValue(emptyConfig)
    await wrapper.get('[data-testid="reload"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.ops.logFilters.empty')
  })

  it('retains the rule draft on a non-conflict save failure', async () => {
    mockAPI.getLogFilters.mockResolvedValue({ ...emptyConfig, rules: [rule] })
    mockAPI.updateLogFilters.mockRejectedValue(new Error('unavailable'))
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get(`[data-testid="rule-${rule.id}"] [role="switch"]`).trigger('click')
    await wrapper.get('[data-testid="save-rules"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.ops.logFilters.saveFailed')
    expect(wrapper.get(`[data-testid="rule-${rule.id}"] [role="switch"]`).attributes('aria-checked')).toBe('true')
    expect(wrapper.emitted('saved')).toBeUndefined()
  })

  it('retains a new proposal on a failed direct confirmation and never enables it locally', async () => {
    mockAPI.updateLogFilters.mockRejectedValue({ response: { status: 409 } })
    const wrapper = mountDialog({ groupId: 7 })
    await flushPromises()
    await wrapper.get('[data-testid="preset-user_inactive"]').trigger('click')
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="editor"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('admin.ops.logFilters.conflict')
    expect(wrapper.findAll('[data-testid^="rule-"]').filter(element => element.attributes('data-testid') !== 'rule-name')).toHaveLength(0)
    expect(mockAPI.updateLogFilters).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('saved')).toBeUndefined()
  })

  it('saves an existing edit immediately while preserving the enabled choice', async () => {
    mockAPI.getLogFilters.mockResolvedValue({ ...emptyConfig, rules: [rule] })
    const wrapper = mountDialog()
    await flushPromises()
    const editButton = wrapper.get(`[data-testid="rule-${rule.id}"]`).findAll('button').find(button => button.text() === 'common.edit')
    await editButton!.trigger('click')
    expect(wrapper.get('[data-testid="apply-rule"]').text()).toBe('admin.ops.logFilters.saveRule')
    await wrapper.get('[data-testid="rule-name"]').setValue('Renamed rule')
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    await flushPromises()
    expect(mockAPI.updateLogFilters).toHaveBeenCalledWith({ revision: 'revision-1', rules: [expect.objectContaining({ id: rule.id, name: 'Renamed rule', enabled: false })] })
    expect(wrapper.find('[data-testid="editor"]').exists()).toBe(false)
  })

  it('keeps the selected group scope if the group name list cannot be loaded', async () => {
    getAllGroups.mockRejectedValue(new Error('unavailable'))
    const wrapper = mountDialog({ groupId: 7 })
    await flushPromises()
    await wrapper.get('[data-testid="preset-user_inactive"]').trigger('click')
    expect(wrapper.get('[data-testid="group"]').element).toHaveProperty('value', '7')
    expect(wrapper.text()).toContain('admin.ops.logFilters.groupsLoadFailed')
    await wrapper.get('[data-testid="apply-rule"]').trigger('click')
    await flushPromises()
    expect(mockAPI.updateLogFilters.mock.calls[0][0].rules[0]).toMatchObject({ enabled: true, group_id: 7 })
  })

  it('does not infer a rule when a proposal is absent or fails', async () => {
    const wrapper = mountDialog({ errorId: 44 })
    await flushPromises()
    expect(wrapper.text()).toContain('admin.ops.logFilters.noProposal')
    expect(wrapper.find('[data-testid="editor"]').exists()).toBe(false)
    mockAPI.getLogFilterProposal.mockRejectedValue(new Error('unavailable'))
    await wrapper.setProps({ errorId: 45 })
    await flushPromises()
    expect(wrapper.text()).toContain('admin.ops.logFilters.proposalFailed')
    expect(wrapper.find('[data-testid="editor"]').exists()).toBe(false)
  })

  it('preserves unrelated advanced settings while saving all five legacy toggles separately', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    for (const field of legacyLogFilters) await wrapper.get(`[data-testid="${field.key}"]`).trigger('click')
    mockAPI.getAdvancedSettings.mockResolvedValue({ ...advanced, auto_refresh_enabled: true, display_openai_token_stats: true, additional_setting: 'preserve' })
    await wrapper.get('[data-testid="save-legacy"]').trigger('click')
    await flushPromises()
    const saved = mockAPI.updateAdvancedSettings.mock.calls[0][0]
    for (const field of legacyLogFilters) expect(saved[field.key]).toBe(true)
    expect(saved).toMatchObject({ auto_refresh_enabled: true, display_openai_token_stats: true, additional_setting: 'preserve', data_retention: advanced.data_retention })
    expect(mockAPI.updateLogFilters).not.toHaveBeenCalled()
  })

  it('does not allow legacy writes after a failed legacy load', async () => {
    mockAPI.getAdvancedSettings.mockRejectedValue(new Error('unavailable'))
    const wrapper = mountDialog()
    await flushPromises()
    expect(wrapper.text()).toContain('admin.ops.logFilters.legacyLoadFailed')
    expect(wrapper.find('[data-testid="save-legacy"]').exists()).toBe(false)
    expect(mockAPI.updateAdvancedSettings).not.toHaveBeenCalled()
  })

  it('reports legacy save failures without assuming the rule configuration was changed', async () => {
    mockAPI.updateAdvancedSettings.mockRejectedValue(new Error('unavailable'))
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get('[data-testid="ignore_context_canceled"]').trigger('click')
    await wrapper.get('[data-testid="save-legacy"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.ops.logFilters.legacySaveFailed')
    expect(wrapper.get('[data-testid="ignore_context_canceled"]').attributes('aria-checked')).toBe('true')
    expect(mockAPI.updateLogFilters).not.toHaveBeenCalled()
    expect(wrapper.emitted('saved')).toBeUndefined()
  })

  it('ignores a stale load after the dialog closes and reopens', async () => {
    let resolveFirst!: (value: OpsLogFilterConfig) => void
    mockAPI.getLogFilters.mockReturnValueOnce(new Promise<OpsLogFilterConfig>(resolve => { resolveFirst = resolve }))
    const wrapper = mountDialog()
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    resolveFirst({ ...emptyConfig, rules: [rule] })
    await flushPromises()
    expect(wrapper.find(`[data-testid="rule-${rule.id}"]`).exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.ops.logFilters.empty')
  })
})
