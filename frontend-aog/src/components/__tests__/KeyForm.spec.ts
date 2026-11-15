import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { i18n } from '@/i18n'
import KeyForm, { type KeyFormValue } from '../KeyForm.vue'
import type { ApiKey, Group } from '@/types'

const group = { id: 2, name: 'Claude', platform: 'anthropic', rate_multiplier: 0.8 } as Group
const existing = {
  id: 9,
  name: 'laptop',
  group_id: 2,
  quota: 50,
  max_concurrency: 5,
  rate_limit_5h: 3,
  rate_limit_1d: 0,
  rate_limit_7d: 0,
  ip_whitelist: [],
  ip_blacklist: []
} as unknown as ApiKey

function mountForm(initial?: ApiKey) {
  return mount(KeyForm, { props: { groups: [group], rates: {}, initial }, global: { plugins: [i18n] } })
}

describe('KeyForm numeric fields', () => {
  it('turns a cleared number input into null instead of an empty string', async () => {
    const w = mountForm(existing)
    const quota = w.findAll('input[type=number]')[0]
    await quota.setValue('')
    await w.find('form').trigger('submit')
    const [value] = w.emitted('submit')![0] as [KeyFormValue]
    expect(value.quota).toBeNull()
    expect(value.rate_limit_5h).toBe(3)
  })

  it('blocks submit for negative or fractional values where integers are required', async () => {
    const w = mountForm()
    await w.find('input[maxlength="100"]').setValue('k')
    const inputs = w.findAll('input[type=number]')
    await inputs[2].setValue('2.5') // max concurrency
    await w.find('form').trigger('submit')
    expect(w.emitted('submit')).toBeUndefined()
    expect(w.find('[role=alert]').exists()).toBe(true)

    await inputs[2].setValue('3')
    await inputs[0].setValue('-1') // quota
    await w.find('form').trigger('submit')
    expect(w.emitted('submit')).toBeUndefined()

    await inputs[0].setValue('12.5')
    await w.find('form').trigger('submit')
    const [value] = w.emitted('submit')![0] as [KeyFormValue]
    expect(value).toMatchObject({ name: 'k', quota: 12.5, max_concurrency: 3, expires_in_days: null })
  })
})
