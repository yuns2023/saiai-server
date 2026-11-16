import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { i18n } from '@/i18n'
import type { ApiKey, Group } from '@/types'

const api = vi.hoisted(() => ({ list: vi.fn(), available: vi.fn(), rates: vi.fn() }))
vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return {
    ...actual,
    keysApi: { list: api.list, create: vi.fn(), update: vi.fn(), remove: vi.fn() },
    groupsApi: { available: api.available, rates: api.rates }
  }
})
vi.mock('vue-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-router')>()
  return { ...actual, useRoute: () => ({ query: {} }), useRouter: () => ({ replace: vi.fn(), push: vi.fn() }) }
})

import KeysView from '../KeysView.vue'

const normal = { id: 1, name: 'Claude', platform: 'anthropic', rate_multiplier: 1, hosted_usage_enabled: false } as Group
const charged = { id: 2, name: 'Mine', platform: 'anthropic', rate_multiplier: 1.2, hosted_usage_enabled: true } as Group
const free = { id: 3, name: 'Mine free', platform: 'anthropic', rate_multiplier: 0, hosted_usage_enabled: true } as Group

function key(id: number, group: Group): ApiKey {
  return { id, name: `k${id}`, key: `sk-test-${id}-abcdefghijkl`, group_id: group.id, group, status: 'active', quota: 0, quota_used: 0 } as unknown as ApiKey
}

enableAutoUnmount(afterEach)

describe('KeysView group rate', () => {
  it('shows the group rate (never the user override) and hosting wording for hosted groups', async () => {
    // The key list's group may omit the flag; the available-group list has it.
    const { hosted_usage_enabled: _omit, ...chargedWithoutFlag } = charged
    api.list.mockResolvedValue({ items: [key(1, normal), key(2, chargedWithoutFlag as Group), key(3, free)], page: 1, pages: 1, total: 3 })
    api.available.mockResolvedValue([normal, charged, free])
    api.rates.mockResolvedValue({ 1: 0.5, 2: 0.5, 3: 0.5 })
    setActivePinia(createPinia())
    i18n.global.locale.value = 'zh'
    const w = mount(KeysView, { global: { plugins: [i18n], stubs: { RouterLink: true, Pager: true, ConnectPanel: true } } })
    await flushPromises()
    const rates = w.findAll('[data-testid=key-group-rate]').map((r) => r.text())
    expect(rates).toEqual(['×0.5', '托管服务费 1.2× 标准价', '自有账号托管 · 不扣费'])
  })
})
