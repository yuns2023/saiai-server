import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ModerationPolicyEditor from '../group/ModerationPolicyEditor.vue'
const mocks = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/admin/settings', () => ({ getJevCategories: mocks.get }))
vi.mock('vue-i18n', async () => { const { ref } = await import('vue'); return { useI18n: () => ({ t: (key: string) => key, locale: ref('zh') }) } })
const catalog = { legacy_version: 'jev-v2', expanded_version: 'jev-expanded-v1', categories: [
  { id: 'PII', label_zh: '隐私', label_en: 'Privacy', legacy: true, enforcement_allowed: true },
  { id: 'violence', label_zh: '暴力', label_en: 'Violence', legacy: false, enforcement_allowed: false },
  { id: 'self-harm/intent', label_zh: '自伤意图', label_en: 'Self-harm intent', legacy: false, enforcement_allowed: false }
] }
describe('ModerationPolicyEditor', () => {
 beforeEach(() => { vi.resetAllMocks(); mocks.get.mockResolvedValue(catalog) })
 it('does not silently migrate a legacy group on load', async () => {
  const wrapper = mount(ModerationPolicyEditor, { props: { modelValue: null, legacyCategories: ['PII', 'Violent'], minConfidence: 90 } })
  await flushPromises()
  expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  expect(wrapper.find('[data-category="violence"]').exists()).toBe(false)
  expect(wrapper.text()).toContain('Violent')
 })
 it('opts into expanded detection while new categories remain ineligible for enforcement', async () => {
  const wrapper = mount(ModerationPolicyEditor, { props: { modelValue: null, legacyCategories: ['PII'], minConfidence: 90 } })
  await flushPromises()
  await wrapper.get('select').setValue('jev-expanded-v1')
  const policy = wrapper.emitted('update:modelValue')![0][0] as any
  expect(policy.audit_categories).toEqual(['PII', 'violence', 'self-harm/intent'])
  expect(policy.enforcement_categories).toEqual(['PII'])
  await wrapper.setProps({ modelValue: policy })
  expect(wrapper.get('[aria-label="violence enforcement"]').attributes('disabled')).toBeDefined()
  expect(wrapper.get('[aria-label="self-harm/intent enforcement"]').attributes('disabled')).toBeDefined()
  await wrapper.get('[aria-label="PII audit"]').setValue(false)
  const changed = wrapper.emitted('update:modelValue')![1][0] as any
  expect(changed.enforcement_categories).toEqual([])
  expect(changed.audit_categories).not.toContain('PII')
 })
 it('preserves an explicitly empty enforcement selection and stores per-category thresholds', async () => {
  const wrapper = mount(ModerationPolicyEditor, { props: { modelValue: { rules_version: 'jev-expanded-v1', audit_categories: ['PII'], enforcement_categories: [], thresholds: {} }, legacyCategories: [], minConfidence: 90 } })
  await flushPromises()
  expect((wrapper.get('[aria-label="PII enforcement"]').element as HTMLInputElement).checked).toBe(false)
  await wrapper.get('[aria-label="PII threshold"]').setValue(95)
  expect((wrapper.emitted('update:modelValue')![0][0] as any).thresholds).toEqual({ PII: 95 })
 })
 it('preserves policy on catalog failure and offers retry', async () => {
  mocks.get.mockRejectedValueOnce(new Error('TEST_ONLY'))
  const wrapper = mount(ModerationPolicyEditor, { props: { legacyCategories: [], minConfidence: 90 } })
  await flushPromises()
  expect(wrapper.find('select').exists()).toBe(false)
  expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  await wrapper.get('button').trigger('click')
  await flushPromises()
  expect(wrapper.find('select').exists()).toBe(true)
 })
})
