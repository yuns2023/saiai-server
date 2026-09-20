import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import UsageFilters from '../UsageFilters.vue'

const { listGroups, getModelStats } = vi.hoisted(() => ({
  listGroups: vi.fn(),
  getModelStats: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: { list: listGroups },
    dashboard: { getModelStats },
    usage: {
      searchUsers: vi.fn(),
      searchApiKeys: vi.fn(),
      searchAccounts: vi.fn(),
    },
  },
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

describe('UsageFilters', () => {
  it('keeps precise timestamps exclusive from legacy calendar dates', async () => {
    listGroups.mockResolvedValue({ items: [] })
    getModelStats.mockResolvedValue({ models: [] })
    const modelValue: Record<string, unknown> = {
      start_date: '2026-09-01',
      end_date: '2026-09-02',
      start_time: '2026-09-18T14:00:00Z',
      end_time: '2026-09-19T14:00:00Z',
    }

    const wrapper = mount(UsageFilters, {
      props: {
        modelValue,
        exporting: false,
        startDate: '2026-09-18',
        endDate: '2026-09-19',
        startTime: modelValue.start_time as string,
        endTime: modelValue.end_time as string,
        showActions: false,
      },
      global: {
        stubs: { Select: true },
      },
    })

    await flushPromises()
    expect(modelValue.start_date).toBeUndefined()
    expect(modelValue.end_date).toBeUndefined()
    expect(getModelStats).toHaveBeenCalledWith({
      start_date: undefined,
      end_date: undefined,
      start_time: '2026-09-18T14:00:00Z',
      end_time: '2026-09-19T14:00:00Z',
    })

    await wrapper.setProps({ startTime: undefined, endTime: undefined })
    expect(modelValue.start_date).toBe('2026-09-18')
    expect(modelValue.end_date).toBe('2026-09-19')
  })
})
