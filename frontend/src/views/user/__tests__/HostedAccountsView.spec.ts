import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import HostedAccountsView from '../HostedAccountsView.vue'
import type { HostedAccount } from '@/api/hosted-accounts'

const mocks = vi.hoisted(() => ({ groups: vi.fn(), list: vi.fn() }))
vi.mock('@/api/hosted-accounts', () => ({ hostedAccountsAPI: mocks }))
vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  const { default: messages } = await import('@/i18n/locales/en')
  const { ref } = await import('vue')
  return { ...actual, useI18n: () => ({
    locale: ref('en'),
    t: (key: string, params: Record<string, unknown> = {}) => {
      const message = key.split('.').reduce<unknown>((value, part) => (value as Record<string, unknown>)?.[part], messages)
      return String(message ?? key).replace(/\{(\w+)\}/g, (_, name: string) => String(params[name] ?? ''))
    }
  }) }
})


const sampledAt = '2026-10-01T12:00:00Z'
const makeAccount = (overrides: Partial<HostedAccount> = {}): HostedAccount => ({
  id: 1, name: 'My hosted account', platform: 'anthropic', status: 'available',
  updated_at: sampledAt, stale: false,
  windows: [{ key: 'five_hour', utilization: 35, resets_at: '2026-10-01T13:00:00Z', expired: false }],
  ...overrides
})
const result = (items: HostedAccount[], total = items.length) => ({ items, total, page: 1, page_size: 20, pages: Math.ceil(total / 20) })

let wrapper: VueWrapper | undefined
const render = () => {
  wrapper = mount(HostedAccountsView, {
    global: {
      stubs: { AppLayout: { template: '<main><slot /></main>' }, Pagination: true }
    }
  })
  return wrapper
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
  vi.setSystemTime(new Date(sampledAt))
  mocks.groups.mockResolvedValue([{ id: 10, name: 'My exclusive group', platform: 'anthropic' }])
  mocks.list.mockResolvedValue(result([makeAccount()]))
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.useRealTimers() })

describe('HostedAccountsView', () => {
  it('shows account snapshots and only uses user endpoints', async () => {
    const view = render(); await flushPromises()
    expect(view.text()).toContain('My hosted account')
    expect(view.text()).toContain('35.0%')
    expect(view.text()).toContain('Data updated:')
    expect(view.text()).toContain('Expected reset in 1h 0m')
    expect(mocks.list).toHaveBeenCalledWith(10, 1, expect.any(AbortSignal))
  })

  it('does not request any accounts when no groups are visible', async () => {
    mocks.groups.mockResolvedValue([])
    const view = render(); await flushPromises()
    expect(view.text()).toContain('No hosted groups available')
    expect(mocks.list).not.toHaveBeenCalled()
  })

  it('keeps unknown usage distinct from measured zero', async () => {
    mocks.list.mockResolvedValue(result([
      makeAccount({ windows: [], updated_at: null, stale: true }),
      makeAccount({ id: 2, name: 'Measured zero', windows: [{ key: 'five_hour', utilization: 0, resets_at: null, expired: false }] })
    ]))
    const view = render(); await flushPromises()
    const cards = view.findAll('[data-testid="hosted-account"]')
    expect(cards[0].text()).toContain('No quota data available')
    expect(cards[0].text()).not.toContain('0.0%')
    expect(cards[1].text()).toContain('0.0%')
    expect(cards[1].text()).toContain('Reset time unknown')
  })

  it('marks data stale as time passes without sending requests', async () => {
    const view = render(); await flushPromises()
    expect(view.text()).not.toContain('Older data')
    vi.advanceTimersByTime(16 * 60 * 1000); await flushPromises()
    expect(view.text()).toContain('Older data')
    expect(mocks.list).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(45 * 60 * 1000); await flushPromises()
    expect(view.text()).toContain('Reset time reached; awaiting new data')
    expect(view.text()).toContain('35.0%')
    expect(mocks.list).toHaveBeenCalledTimes(1)
  })

  it('clears previously visible accounts when authorization is revoked', async () => {
    const view = render(); await flushPromises()
    mocks.list.mockRejectedValue({ status: 404 })
    await view.get('button').trigger('click'); await flushPromises()
    expect(view.find('[data-testid="hosted-account"]').exists()).toBe(false)
    expect(view.get('[role="alert"]').text()).toContain('Group access or visibility has changed')
    expect(view.text()).not.toContain('My hosted account')
  })

  it('refreshes group authorization and removes groups that are no longer visible', async () => {
    const view = render(); await flushPromises()
    mocks.groups.mockResolvedValue([])
    await view.get('button').trigger('click'); await flushPromises()
    expect(view.text()).toContain('No hosted groups available')
    expect(view.text()).not.toContain('My hosted account')
    expect(mocks.list).toHaveBeenCalledTimes(1)
  })

  it('ignores a late response from a previously selected group', async () => {
    mocks.groups.mockResolvedValue([{ id: 10, name: 'First', platform: 'anthropic' }, { id: 20, name: 'Second', platform: 'openai' }])
    let resolveFirst!: (value: ReturnType<typeof result>) => void
    mocks.list.mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve }))
    const view = render(); await flushPromises()
    const firstSignal = mocks.list.mock.calls[0][2] as AbortSignal
    mocks.list.mockResolvedValue(result([makeAccount({ name: 'Second account' })]))
    await view.get('select').setValue('20'); await flushPromises()
    expect(firstSignal.aborted).toBe(true)
    resolveFirst(result([makeAccount({ name: 'First account' })])); await flushPromises()
    expect(view.text()).toContain('Second account')
    expect(view.text()).not.toContain('First account')
  })

  it('clears old data after an error and allows retry', async () => {
    const view = render(); await flushPromises()
    mocks.groups.mockRejectedValueOnce(new Error('TEST_ONLY_ERROR'))
    await view.get('button').trigger('click'); await flushPromises()
    expect(view.text()).not.toContain('My hosted account')
    expect(view.get('[role="alert"]').text()).toContain('Failed to load hosted accounts')
    expect(view.text()).not.toContain('TEST_ONLY_ERROR')
    await view.get('button').trigger('click'); await flushPromises()
    expect(view.text()).toContain('My hosted account')
  })

  it('loads the requested page and aborts pending requests on unmount', async () => {
    mocks.list.mockResolvedValue(result([makeAccount()], 21))
    const view = render(); await flushPromises()
    view.findComponent({ name: 'Pagination' }).vm.$emit('update:page', 2); await flushPromises()
    expect(mocks.list).toHaveBeenLastCalledWith(10, 2, expect.any(AbortSignal))
    const signal = mocks.list.mock.calls[mocks.list.mock.calls.length - 1][2] as AbortSignal
    view.unmount(); wrapper = undefined
    expect(signal.aborted).toBe(true)
  })
})
