import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const { getById } = vi.hoisted(() => ({ getById: vi.fn() }))
vi.mock('@/api/admin/users', () => ({ getById }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))
import UserIdentityTooltip from '../UserIdentityTooltip.vue'
import { resetUserIdentityCache } from '../userIdentityCache'

const tooltip = () => document.body.querySelector('[role="tooltip"]') as HTMLElement | null

function mountTip(props: Record<string, unknown>) {
  return mount(UserIdentityTooltip, { props, slots: { default: '<span class="email">alice@example.com</span>' }, attachTo: document.body })
}

async function hover(wrapper: ReturnType<typeof mountTip>) {
  await wrapper.find('span').trigger('mouseenter')
  await wrapper.find('.group').trigger('mouseenter')
  vi.advanceTimersByTime(250)
  await flushPromises()
}

describe('UserIdentityTooltip', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    resetUserIdentityCache()
    getById.mockReset()
  })
  afterEach(() => {
    vi.useRealTimers()
    document.body.innerHTML = ''
  })

  it('fetches username and notes on hover and caches them per user', async () => {
    getById.mockResolvedValue({ id: 7, username: 'alice', notes: 'VIP 客户' })
    const first = mountTip({ userId: 7 })
    await hover(first)
    expect(tooltip()?.textContent).toContain('alice')
    expect(tooltip()?.textContent).toContain('VIP 客户')
    expect(tooltip()?.textContent).toContain('#7')
    first.unmount()

    await hover(mountTip({ userId: 7 }))
    expect(getById).toHaveBeenCalledTimes(1)
  })

  it('uses caller-provided fields without fetching', async () => {
    const wrapper = mountTip({ userId: 7, username: 'bob', notes: '' })
    await hover(wrapper)
    expect(getById).not.toHaveBeenCalled()
    expect(tooltip()?.textContent).toContain('bob')
    expect(tooltip()?.textContent).toContain('admin.users.identityTooltip.empty')
  })

  it('does not fetch when the pointer only sweeps past', async () => {
    const wrapper = mountTip({ userId: 7 })
    await wrapper.find('span').trigger('mouseenter')
    await wrapper.find('span').trigger('mouseleave')
    vi.advanceTimersByTime(500)
    await flushPromises()
    expect(getById).not.toHaveBeenCalled()
  })

  it('reports a failed load and retries on the next hover', async () => {
    getById.mockRejectedValueOnce(new Error('boom')).mockResolvedValueOnce({ id: 7, username: 'alice', notes: '' })
    const first = mountTip({ userId: 7 })
    await hover(first)
    expect(tooltip()?.textContent).toContain('admin.users.identityTooltip.loadFailed')
    first.unmount()

    await hover(mountTip({ userId: 7 }))
    expect(getById).toHaveBeenCalledTimes(2)
    expect(tooltip()?.textContent).toContain('alice')
  })

  it('does not pin open on click when the trigger has its own click action', async () => {
    const wrapper = mountTip({ userId: 7, username: 'a', notes: '', pinOnClick: false })
    await wrapper.find('.group').trigger('click')
    await flushPromises()
    expect(tooltip()).toBeNull()
  })

  it('loads the new user when a hovered row is reused for someone else mid-request', async () => {
    let resolveFirst!: (u: object) => void
    getById.mockImplementationOnce(() => new Promise((r) => { resolveFirst = r }))
      .mockResolvedValueOnce({ id: 8, username: 'bob', notes: 'second' })
    const wrapper = mountTip({ userId: 7 })
    await hover(wrapper)
    await wrapper.setProps({ userId: 8 })
    await flushPromises()
    resolveFirst({ id: 7, username: 'alice', notes: 'first' })
    await flushPromises()
    expect(getById).toHaveBeenLastCalledWith(8)
    expect(tooltip()?.textContent).toContain('bob')
    expect(tooltip()?.textContent).not.toContain('alice')
  })

  it('renders the slot alone when there is no user to describe', () => {
    const wrapper = mountTip({ userId: null })
    expect(wrapper.find('.group').exists()).toBe(false)
    expect(wrapper.text()).toContain('alice@example.com')
  })
})
