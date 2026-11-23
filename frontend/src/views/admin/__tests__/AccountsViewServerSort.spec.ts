import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { flushPromises, shallowMount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  proxiesGetAll: vi.fn(),
  groupsGetAll: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: mocks.list,
      listWithEtag: mocks.listWithEtag,
      getBatchTodayStats: mocks.getBatchTodayStats
    },
    proxies: { getAll: mocks.proxiesGetAll },
    groups: { getAll: mocks.groupsGetAll }
  }
}))
vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  const { ref } = await import('vue')
  return { ...actual, useI18n: () => ({ locale: ref('en'), t: (key: string) => key }) }
})

import AccountsView from '../AccountsView.vue'

const SORT_STORAGE_KEY = 'account-table-sort'

// Stand-in for DataTable: exposes the props the view passes and lets the test
// emit 'sort' like a header click in server-side mode.
const DataTableStub = defineComponent({
  name: 'DataTable',
  props: ['columns', 'data', 'loading', 'serverSideSort', 'sortStorageKey', 'defaultSortKey', 'defaultSortOrder', 'rowKey'],
  emits: ['sort'],
  setup() {
    return () => h('div', { class: 'data-table-stub' })
  }
})
const SlotLayout = defineComponent({
  setup(_, { slots }) {
    return () => h('div', [slots.default?.(), slots.filters?.(), slots.table?.(), slots.pagination?.()])
  }
})

const page = (items: unknown[] = []) => ({ items, total: items.length, page: 1, page_size: 20, pages: 1 })

let wrapper: VueWrapper | undefined
const render = async () => {
  wrapper = shallowMount(AccountsView, {
    global: {
      stubs: { DataTable: DataTableStub, AppLayout: SlotLayout, TablePageLayout: SlotLayout }
    }
  })
  await flushPromises()
  return wrapper
}
const lastListParams = () => mocks.list.mock.calls.at(-1)?.[2] as Record<string, unknown>

beforeEach(() => {
  setActivePinia(createPinia())
  localStorage.clear()
  vi.clearAllMocks()
  mocks.list.mockResolvedValue(page())
  mocks.listWithEtag.mockResolvedValue({ notModified: true, etag: null, data: null })
  mocks.getBatchTodayStats.mockResolvedValue({ stats: {} })
  mocks.proxiesGetAll.mockResolvedValue([])
  mocks.groupsGetAll.mockResolvedValue([])
})
afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
})

describe('AccountsView server-side sort', () => {
  it('enables server-side sort and sends the default name asc on first load', async () => {
    const w = await render()
    expect(w.findComponent(DataTableStub).props('serverSideSort')).toBe(true)
    expect(mocks.list).toHaveBeenCalledTimes(1)
    expect(lastListParams()).toMatchObject({ sort_by: 'name', sort_order: 'asc' })
  })

  it('sends the persisted header sort on first load', async () => {
    localStorage.setItem(SORT_STORAGE_KEY, JSON.stringify({ key: 'last_used_at', order: 'desc' }))
    await render()
    expect(mocks.list).toHaveBeenCalledTimes(1)
    expect(lastListParams()).toMatchObject({ sort_by: 'last_used_at', sort_order: 'desc' })
  })

  it('falls back to name asc when the persisted sort column is hidden (as DataTable does)', async () => {
    // priority is hidden by default, so DataTable shows the default arrow.
    localStorage.setItem(SORT_STORAGE_KEY, JSON.stringify({ key: 'priority', order: 'desc' }))
    await render()
    expect(lastListParams()).toMatchObject({ sort_by: 'name', sort_order: 'asc' })
  })

  it('reloads page 1 with the clicked sort and keeps it for auto refresh', async () => {
    const w = await render()
    mocks.list.mockClear()
    w.findComponent(DataTableStub).vm.$emit('sort', 'last_used_at', 'desc')
    await flushPromises()
    expect(mocks.list).toHaveBeenCalledTimes(1)
    const [pageNo, , params] = mocks.list.mock.calls[0]
    expect(pageNo).toBe(1)
    expect(params).toMatchObject({ sort_by: 'last_used_at', sort_order: 'desc' })
  })

  it('switches back to the default sort when the sorted column is hidden', async () => {
    localStorage.setItem('account-hidden-columns', JSON.stringify([]))
    localStorage.setItem(SORT_STORAGE_KEY, JSON.stringify({ key: 'priority', order: 'desc' }))
    const w = await render()
    expect(lastListParams()).toMatchObject({ sort_by: 'priority', sort_order: 'desc' })
    mocks.list.mockClear()
    // Toggle the priority column off through the view's column menu handler.
    ;(w.vm as unknown as { toggleColumn: (key: string) => void }).toggleColumn('priority')
    await flushPromises()
    expect(mocks.list).toHaveBeenCalledTimes(1)
    expect(lastListParams()).toMatchObject({ sort_by: 'name', sort_order: 'asc' })
  })
})
