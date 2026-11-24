import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import DataTable from '../DataTable.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const columns = [
  { key: 'name', label: 'Name', sortable: false, sortOptions: [{ key: 'name', label: 'Name' }, { key: 'id', label: 'ID' }] },
  { key: 'last_used_at', label: 'Used / Expires', sortable: false, sortOptions: [{ key: 'last_used_at', label: 'Used' }, { key: 'expires_at', label: 'Expires' }] }
]

describe('DataTable header sortOptions', () => {
  beforeEach(() => localStorage.clear())

  it('sorts by each option of a merged column independently', async () => {
    const wrapper = mount(DataTable, { props: { columns, data: [], serverSideSort: true } })
    await wrapper.get('button[data-sort-key="id"]').trigger('click')
    await wrapper.get('button[data-sort-key="id"]').trigger('click')
    await wrapper.get('button[data-sort-key="expires_at"]').trigger('click')
    expect(wrapper.emitted('sort')).toEqual([
      ['id', 'asc'],
      ['id', 'desc'],
      ['expires_at', 'asc']
    ])
    // The column header itself is not a sort target.
    await wrapper.findAll('th')[0].trigger('click')
    expect(wrapper.emitted('sort')).toHaveLength(3)
  })

  it('accepts a persisted sort key that is a sort option', async () => {
    localStorage.setItem('t-sort', JSON.stringify({ key: 'expires_at', order: 'desc' }))
    const wrapper = mount(DataTable, { props: { columns, data: [], sortStorageKey: 't-sort' } })
    await wrapper.vm.$nextTick()
    expect(wrapper.get('button[data-sort-key="expires_at"]').find('svg').exists()).toBe(true)
    expect(wrapper.get('button[data-sort-key="name"]').find('svg').exists()).toBe(false)
  })
})
