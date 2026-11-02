import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ClaudeEntrypointPolicyEditor from '../ClaudeEntrypointPolicyEditor.vue'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'

const api = vi.hoisted(() => ({ list: vi.fn() }))
const toast = vi.hoisted(() => ({ success: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: { getClaudeEntrypointObservations: api.list } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: toast.success }) }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key })
}))

const observation = (ua: string, result = 'violation_observed') => ({
  ua_entrypoint: ua,
  body_entrypoint: ua,
  body_status: 'valid',
  cli_version: '2.1.316',
  result,
  reason: 'ua_not_allowed',
  requests: 4,
  first_seen_at: '2026-11-02T00:00:00Z',
  last_seen_at: '2026-11-02T01:00:00Z',
  sample_user_agent: 'claude-cli/2.1.316 (external, t3-code, agent-sdk/0.3.276)'
})

const mountEditor = (props: Record<string, unknown>) =>
  mount(ClaudeEntrypointPolicyEditor, {
    props: { policy: 'observe', allowlist: ['cli', 'claude-vscode', 'sdk-cli'], testidPrefix: 'editForm', ...props }
  })

beforeEach(() => {
  vi.clearAllMocks()
  api.list.mockResolvedValue({ days: 7, items: [observation('t3-code'), observation('<none>'), observation('cli', 'allowed')] })
})

describe('ClaudeEntrypointPolicyEditor', () => {
  it('loads observations for the edited group and adds a UA entrypoint to the allowlist', async () => {
    const wrapper = mountEditor({ groupId: 42 })
    await flushPromises()
    expect(api.list).toHaveBeenCalledWith(42, 7)
    const rows = wrapper.findAll('[data-testid="editForm-claude-entrypoint-observations"] tbody tr')
    expect(rows).toHaveLength(3)
    expect(rows[0].text()).toContain('t3-code')
    // <none> and already-allowed entrypoints get no add button.
    expect(wrapper.find('[data-testid="editForm-claude-entrypoint-add-1"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="editForm-claude-entrypoint-add-2"]').exists()).toBe(false)
    await wrapper.find('[data-testid="editForm-claude-entrypoint-add-0"]').trigger('click')
    expect(wrapper.emitted('update:allowlist')?.[0]).toEqual([['cli', 'claude-vscode', 'sdk-cli', 't3-code']])
    expect(toast.success).toHaveBeenCalled()
  })

  it('does not load observations on create or when the policy is off', async () => {
    mountEditor({ groupId: null })
    mountEditor({ groupId: 42, policy: 'off' })
    await flushPromises()
    expect(api.list).not.toHaveBeenCalled()
  })

  it('normalizes typed entrypoints and rejects invalid ones', async () => {
    const wrapper = mountEditor({ allowlist: ['cli'] })
    const input = wrapper.find('[data-testid="editForm-claude-entrypoint-input"]')
    await input.setValue(' T3-Code , cli ')
    await input.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('update:allowlist')?.[0]).toEqual([['cli', 't3-code']])

    await input.setValue('bad value')
    await input.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('update:allowlist')).toHaveLength(1)
    expect(wrapper.text()).toContain('admin.groups.claudeEntrypoint.allowlistInvalid')
  })

  it('emits the policy and restores the default list', async () => {
    const wrapper = mountEditor({ allowlist: [] })
    await wrapper.find('[data-testid="editForm-claude-entrypoint-policy"]').setValue('enforce')
    expect(wrapper.emitted('update:policy')?.[0]).toEqual(['enforce'])
    await wrapper.find('[data-testid="editForm-claude-entrypoint-restore"]').trigger('click')
    expect(wrapper.emitted('update:allowlist')?.[0]).toEqual([['cli', 'claude-vscode', 'sdk-cli']])
  })

  it('has matching zh/en locale keys', () => {
    const keys = (value: unknown, prefix = ''): string[] =>
      value && typeof value === 'object'
        ? Object.entries(value as Record<string, unknown>).flatMap(([key, child]) => keys(child, `${prefix}${key}.`))
        : [prefix]
    const zhKeys = keys((zh as any).admin.groups.claudeEntrypoint).sort()
    expect(zhKeys.length).toBeGreaterThan(10)
    expect(keys((en as any).admin.groups.claudeEntrypoint).sort()).toEqual(zhKeys)
  })
})
