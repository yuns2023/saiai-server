import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { i18n } from '@/i18n'
import type { UserInviteOverview } from '@/types'

const api = vi.hoisted(() => ({ overview: vi.fn(), create: vi.fn(), revoke: vi.fn() }))
vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return { ...actual, invitesApi: api }
})

import InvitesView from '../InvitesView.vue'

const base: UserInviteOverview = {
  enabled: true,
  eligible: true,
  max_unused: 5,
  max_total: 0,
  unused: 1,
  invited: 2,
  daily_limit_reached: false,
  max_per_week: 0,
  created_this_week: 0,
  weekly_limit_reached: false,
  can_create: true,
  codes: [],
  pagination: { total: 0, page: 1, page_size: 20, pages: 1 }
}

async function mountView(over: Partial<UserInviteOverview>) {
  api.overview.mockResolvedValue({ ...base, ...over })
  setActivePinia(createPinia())
  i18n.global.locale.value = 'zh'
  const w = mount(InvitesView, { global: { plugins: [i18n], stubs: { RouterLink: true, Pager: true } } })
  await flushPromises()
  return w
}

describe('InvitesView weekly cap', () => {
  beforeEach(() => api.overview.mockReset())

  it('hides the weekly line when the site sets no cap', async () => {
    const w = await mountView({})
    expect(w.find('[data-testid=invite-week-left]').exists()).toBe(false)
  })

  it('shows what is left in the rolling 7 days', async () => {
    const w = await mountView({ max_per_week: 5, created_this_week: 2 })
    expect(w.find('[data-testid=invite-week-left]').text()).toBe('本周还可生成 3 个（滚动 7 天，上限 5）')
  })

  it('explains a reached weekly cap', async () => {
    const w = await mountView({ max_per_week: 2, created_this_week: 2, weekly_limit_reached: true, can_create: false })
    expect(w.find('[data-testid=invite-week-left]').text()).toContain('0')
    expect(w.find('[data-testid=invite-blocker]').text()).toContain('最近 7 天')
  })
})
