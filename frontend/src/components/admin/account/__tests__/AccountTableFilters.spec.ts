import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, h, reactive } from 'vue'
import AccountTableFilters from '../AccountTableFilters.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

// Mirrors AccountsView: filters live in the parent and are merged back from update:filters.
const mountWithParent = (ownerUserId?: number) => {
  const params = reactive<Record<string, any>>({ platform: '', type: '', status: '', group: '', search: '', owner_user_id: ownerUserId })
  const onChange = vi.fn()
  const Parent = defineComponent({
    setup: () => () => h(AccountTableFilters, {
      searchQuery: '',
      filters: params,
      groups: [],
      'onUpdate:filters': (next: Record<string, any>) => Object.assign(params, next),
      onChange
    })
  })
  const wrapper = mount(Parent, { global: { stubs: { Select: true, SearchInput: true } } })
  return { wrapper, params, onChange, input: () => wrapper.get('[data-testid="owner-filter"]') }
}

describe('AccountTableFilters owner filter', () => {
  it('applies a positive user ID as a number and triggers a reload', async () => {
    const { params, onChange, input } = mountWithParent()
    await input().setValue(' 42 ')
    expect(params.owner_user_id).toBe(42)
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(input().classes()).not.toContain('input-error')
  })

  it('keeps the current filter and the typed text on invalid input', async () => {
    const { params, onChange, input } = mountWithParent(42)
    expect((input().element as HTMLInputElement).value).toBe('42')
    for (const value of ['42x', '0', '-3', '4.5']) {
      await input().setValue(value)
      expect((input().element as HTMLInputElement).value).toBe(value)
      expect(input().classes()).toContain('input-error')
    }
    expect(params.owner_user_id).toBe(42)
    expect(onChange).not.toHaveBeenCalled()
  })

  it('clears the filter (param not sent) when the field is emptied', async () => {
    const { params, onChange, input } = mountWithParent(42)
    await input().setValue('')
    expect(params.owner_user_id).toBeUndefined()
    expect(onChange).toHaveBeenCalledTimes(1)
  })

  it('follows an external filter change unless the field holds invalid text', async () => {
    const { wrapper, params, input } = mountWithParent(42)
    params.owner_user_id = 7
    await wrapper.vm.$nextTick()
    expect((input().element as HTMLInputElement).value).toBe('7')
    await input().setValue('7x')
    params.owner_user_id = undefined
    await wrapper.vm.$nextTick()
    expect((input().element as HTMLInputElement).value).toBe('7x')
  })

  it('does not reload when the value is unchanged', async () => {
    const { onChange, input } = mountWithParent(42)
    await input().setValue('042')
    expect(onChange).not.toHaveBeenCalled()
  })
})
