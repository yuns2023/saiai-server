import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import CodexResetModal from '../CodexResetModal.vue'
import type { Account } from '@/types'

const api = vi.hoisted(() => ({ listCodexResetCredits: vi.fn(), consumeCodexResetCredit: vi.fn() }))
vi.mock('@/api/admin/accounts', () => ({ accountsAPI: api }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const credit = { id: 'credit-1', reset_type: 'codex_rate_limits', status: 'available', expires_at: '2099-01-01T00:00:00Z', granted_at: '2026-01-01T00:00:00Z' }
const account = { id: 42, name: 'mock-account', platform: 'openai', type: 'oauth' } as Account
const makeModal = () => mount(CodexResetModal, {
  props: { show: true, account },
  global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' } } }
})
const button = (wrapper: ReturnType<typeof makeModal>, key: string) => wrapper.findAll('button').find(item => item.text().includes(`codexReset.${key}`))!

beforeEach(() => {
  vi.clearAllMocks()
  sessionStorage.clear()
  api.listCodexResetCredits.mockResolvedValue({ available_count: 1, credits: [credit] })
  api.consumeCodexResetCredit.mockResolvedValue({ outcome: 'reset', windows_reset: 2, quota_synced: true })
})

describe('Codex reset credits', () => {
  it('lists credits without consuming; hides unsupported, expired and redeemed actions', async () => {
    api.listCodexResetCredits.mockResolvedValue({ available_count: 4, credits: [credit,
      { ...credit, id: 'expired', expires_at: '2000-01-01T00:00:00Z' },
      { ...credit, id: 'unknown', reset_type: 'unknown' },
      { ...credit, id: 'used', status: 'redeemed' }
    ] })
    const wrapper = makeModal()
    await flushPromises()
    expect(api.consumeCodexResetCredit).not.toHaveBeenCalled()
    expect(wrapper.findAll('button').filter(item => item.text().includes('codexReset.use'))).toHaveLength(1)
    wrapper.unmount()
  })

  it('consumes the selected credit once and refreshes usage on success', async () => {
    const wrapper = makeModal()
    await flushPromises()
    const use = button(wrapper, 'use')
    await Promise.all([use.trigger('click'), use.trigger('click')])
    await flushPromises()
    expect(api.consumeCodexResetCredit).toHaveBeenCalledTimes(1)
    expect(api.consumeCodexResetCredit).toHaveBeenCalledWith(42, 'credit-1', expect.any(String))
    expect(wrapper.emitted('updated')).toHaveLength(1)
    expect(sessionStorage.getItem('codex-reset-pending:42')).toBeNull()
    expect(wrapper.text()).toContain('codexReset.reset')
    wrapper.unmount()
  })

  it('retains the original key across timeout and dialog reopening', async () => {
    api.consumeCodexResetCredit.mockRejectedValueOnce({ message: 'Request timed out' })
    const wrapper = makeModal()
    await flushPromises()
    await button(wrapper, 'use').trigger('click')
    await flushPromises()
    const original = api.consumeCodexResetCredit.mock.calls[0]
    expect(wrapper.text()).toContain('Request timed out')
    expect(button(wrapper, 'use').attributes('disabled')).toBeDefined()
    wrapper.unmount()
    const reopened = makeModal()
    await flushPromises()
    await button(reopened, 'retry').trigger('click')
    await flushPromises()
    expect(api.consumeCodexResetCredit.mock.calls[1]).toEqual(original)
    expect(sessionStorage.getItem('codex-reset-pending:42')).toBeNull()
    reopened.unmount()
  })

  it('keeps a safe retry after confirmed consumption with failed local sync', async () => {
    api.consumeCodexResetCredit.mockResolvedValue({ outcome: 'reset', windows_reset: 2, quota_synced: false })
    const wrapper = makeModal()
    await flushPromises()
    await button(wrapper, 'use').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('codexReset.reset')
    expect(wrapper.text()).toContain('codexReset.syncFailed')
    expect(button(wrapper, 'retry').exists()).toBe(true)
    expect(sessionStorage.getItem('codex-reset-pending:42')).not.toBeNull()
    wrapper.unmount()
  })

  it('reports no-credit without claiming success', async () => {
    api.consumeCodexResetCredit.mockResolvedValue({ outcome: 'no_credit', windows_reset: 0, quota_synced: false })
    const wrapper = makeModal()
    await flushPromises()
    await button(wrapper, 'use').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('codexReset.no_credit')
    expect(wrapper.text()).not.toContain('codexReset.syncFailed')
    expect(sessionStorage.getItem('codex-reset-pending:42')).toBeNull()
    wrapper.unmount()
  })

  it('shows list failures and permits retry without redeeming', async () => {
    api.listCodexResetCredits.mockRejectedValueOnce({ message: 'Authentication failed' })
    const wrapper = makeModal()
    await flushPromises()
    expect(wrapper.text()).toContain('Authentication failed')
    expect(api.consumeCodexResetCredit).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
