import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import QuotaWindowRestartModal from '../QuotaWindowRestartModal.vue'
import type { Account } from '@/types'

const api = vi.hoisted(() => ({ getUsage: vi.fn(), setQuotaWindowRestart: vi.fn(), clearQuotaWindowRestart: vi.fn() }))
const app = vi.hoisted(() => ({ showSuccess: vi.fn() }))
vi.mock('@/api/admin/accounts', () => ({ accountsAPI: api }))
vi.mock('@/stores/app', () => ({ useAppStore: () => app }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const NOW = new Date('2026-11-13T04:00:00Z')
const RESET = '2026-11-15T13:00:00Z'
const anthropic = { id: 287, name: 'ultra', platform: 'anthropic', type: 'setup-token' } as Account
const openai = { id: 9, name: 'codex', platform: 'openai', type: 'oauth' } as Account

const makeModal = (account: Account = anthropic) => mount(QuotaWindowRestartModal, {
  props: { show: true, account },
  global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' } } }
})

beforeEach(() => {
  vi.clearAllMocks()
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(NOW)
  api.getUsage.mockResolvedValue({ seven_day: { utilization: 20, resets_at: RESET, remaining_seconds: 1 } })
  api.setQuotaWindowRestart.mockResolvedValue({ start: NOW.toISOString(), source: 'manual' })
  api.clearQuotaWindowRestart.mockResolvedValue(undefined)
})

afterEach(() => {
  vi.useRealTimers()
})

describe('QuotaWindowRestartModal', () => {
  it('reads the window from passive usage and restarts it at the chosen time with the expected reset', async () => {
    const wrapper = makeModal()
    await flushPromises()
    expect(api.getUsage).toHaveBeenCalledWith(287, 'passive')
    const input = wrapper.get('[data-testid="restart-start"]')
    const chosen = new Date('2026-11-10T08:30:15Z')
    const pad = (n: number) => String(n).padStart(2, '0')
    await input.setValue(`${chosen.getFullYear()}-${pad(chosen.getMonth() + 1)}-${pad(chosen.getDate())}T${pad(chosen.getHours())}:${pad(chosen.getMinutes())}:${pad(chosen.getSeconds())}`)
    await wrapper.get('[data-testid="restart-confirm"]').trigger('click')
    await flushPromises()
    expect(api.setQuotaWindowRestart).toHaveBeenCalledWith(287, {
      window: 'seven_day',
      expected_reset: Date.parse(RESET) / 1000,
      start: '2026-11-10T08:30:15Z'
    })
    expect(wrapper.emitted('updated')).toHaveLength(1)
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('sends no start when the default "now" is kept (server time, no DST ambiguity)', async () => {
    const wrapper = makeModal()
    await flushPromises()
    await wrapper.get('[data-testid="restart-confirm"]').trigger('click')
    await flushPromises()
    expect(api.setQuotaWindowRestart).toHaveBeenCalledWith(287, { window: 'seven_day', expected_reset: Date.parse(RESET) / 1000, start: undefined })
  })

  it.each([
    ['cleared', ''],
    ['not a date', '2026-13-45T99:99'],
  ])('refuses an edited start that is %s instead of falling back to now', async (_, value) => {
    const wrapper = makeModal()
    await flushPromises()
    await wrapper.get('[data-testid="restart-start"]').setValue(value)
    await wrapper.get('[data-testid="restart-confirm"]').trigger('click')
    await flushPromises()
    expect(api.setQuotaWindowRestart).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('admin.accounts.quotaWindowRestart.invalidStart')
  })

  it('keeps a picked time across a failed submit and disables retry when the window cannot be re-read', async () => {
    api.setQuotaWindowRestart.mockRejectedValueOnce({ message: 'Network Error' })
    const wrapper = makeModal()
    await flushPromises()
    const input = wrapper.get('[data-testid="restart-start"]')
    const picked = new Date('2026-11-11T02:00:00Z')
    const pad = (n: number) => String(n).padStart(2, '0')
    await input.setValue(`${picked.getFullYear()}-${pad(picked.getMonth() + 1)}-${pad(picked.getDate())}T${pad(picked.getHours())}:${pad(picked.getMinutes())}:${pad(picked.getSeconds())}`)
    await wrapper.get('[data-testid="restart-confirm"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="restart-confirm"]').trigger('click')
    await flushPromises()
    expect(api.setQuotaWindowRestart).toHaveBeenLastCalledWith(287, expect.objectContaining({ start: '2026-11-11T02:00:00Z' }))

    api.setQuotaWindowRestart.mockRejectedValueOnce({ message: 'boom' })
    api.getUsage.mockRejectedValueOnce({ message: 'reload failed' })
    await wrapper.get('[data-testid="restart-confirm"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="restart-error"]').text()).toContain('boom')
    expect(wrapper.get('[data-testid="restart-error"]').text()).toContain('reload failed')
    expect(wrapper.get('[data-testid="restart-confirm"]').attributes('disabled')).toBeDefined()
  })

  it('uses the active usage source for OpenAI accounts', async () => {
    makeModal(openai)
    await flushPromises()
    expect(api.getUsage).toHaveBeenCalledWith(9, undefined)
  })

  it('shows the current restart and can undo it', async () => {
    api.getUsage.mockResolvedValue({ seven_day: { utilization: 20, resets_at: RESET, remaining_seconds: 1, window_restart: { start: '2026-11-12T00:00:00Z', source: 'auto' } } })
    const wrapper = makeModal()
    await flushPromises()
    expect(wrapper.get('[data-testid="restart-current"]').text()).toContain('admin.accounts.quotaWindowRestart.current')
    await wrapper.get('[data-testid="restart-clear"]').trigger('click')
    await flushPromises()
    expect(api.clearQuotaWindowRestart).toHaveBeenCalledWith(287, Date.parse(RESET) / 1000)
    expect(wrapper.emitted('updated')).toHaveLength(1)
  })

  it('refuses without a valid current window', async () => {
    for (const resets_at of [null, '2026-11-13T03:00:00Z', '2026-11-21T00:00:00Z']) {
      api.getUsage.mockResolvedValue({ seven_day: { utilization: 20, resets_at, remaining_seconds: 0 } })
      const wrapper = makeModal()
      await flushPromises()
      expect(wrapper.find('[data-testid="restart-no-window"]').exists()).toBe(true)
      expect(wrapper.get('[data-testid="restart-confirm"]').attributes('disabled')).toBeDefined()
      wrapper.unmount()
    }
  })

  it('shows the server error (e.g. 409 when the window moved), re-reads the window and retries with the new reset', async () => {
    const NEXT = '2026-11-19T13:00:00Z'
    api.setQuotaWindowRestart.mockRejectedValueOnce({ message: 'quota window reset time changed; reload and retry' })
    const wrapper = makeModal()
    await flushPromises()
    api.getUsage.mockResolvedValue({ seven_day: { utilization: 1, resets_at: NEXT, remaining_seconds: 1 } })
    await wrapper.get('[data-testid="restart-confirm"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="restart-error"]').text()).toContain('reload and retry')
    expect(wrapper.emitted('close')).toBeUndefined()
    await wrapper.get('[data-testid="restart-confirm"]').trigger('click')
    await flushPromises()
    expect(api.setQuotaWindowRestart).toHaveBeenLastCalledWith(287, expect.objectContaining({ expected_reset: Date.parse(NEXT) / 1000 }))
  })
})
