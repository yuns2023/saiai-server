import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import DateTimeRangePicker from '../DateTimeRangePicker.vue'
import { presetTimeRange } from '@/utils/dateTimeRange'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const props = { startTime: '2026-10-17T18:13:00Z', endTime: '2026-10-18T18:13:00Z', preset: 'last24Hours' }
describe('DateTimeRangePicker', () => {
  it('applies custom times without guessing a preset from dates', async () => {
    const wrapper = mount(DateTimeRangePicker, { props })
    await wrapper.find('button').trigger('click')
    const inputs = wrapper.findAll('input')
    await inputs[0].setValue('2026-10-17T10:30')
    await inputs[1].setValue('2026-10-17T11:45')
    await wrapper.findAll('button').at(-1)!.trigger('click')
    expect(wrapper.emitted('change')![0]).toEqual([{
      startTime: new Date('2026-10-17T10:30').toISOString(),
      endTime: new Date('2026-10-17T11:45').toISOString(), preset: null
    }])
  })
  it('rejects reversed and oversized ranges and discards cancelled edits', async () => {
    const wrapper = mount(DateTimeRangePicker, { props: { ...props, maxDays: 90 } })
    await wrapper.find('button').trigger('click')
    await wrapper.findAll('input')[0].setValue('2026-10-19T10:00')
    expect(wrapper.find('[role="alert"]').text()).toBe('dates.invalidTimeRange')
    expect(wrapper.findAll('button').at(-1)!.attributes('disabled')).toBeDefined()
    await wrapper.findAll('input')[0].setValue('2026-01-01T00:00')
    expect(wrapper.find('[role="alert"]').text()).toBe('dates.maxRangeDays')
    await wrapper.find('input').trigger('keydown', { key: 'Escape' })
    await wrapper.find('button').trigger('click')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.emitted('change')).toBeUndefined()
  })
  it('uses exact 24 hours and an exclusive midnight for yesterday', () => {
    const now = new Date(2026, 9, 18, 18, 13)
    const rolling = presetTimeRange('last24Hours', now)
    expect(+new Date(rolling.endTime) - +new Date(rolling.startTime)).toBe(86400000)
    const yesterday = presetTimeRange('yesterday', now)
    expect(new Date(yesterday.startTime).getDate()).toBe(17)
    expect(new Date(yesterday.endTime).getDate()).toBe(18)
    expect(new Date(yesterday.endTime).getHours()).toBe(0)
  })
  it('treats today as the whole calendar day, even in the first minute', () => {
    for (const now of [new Date(2026, 9, 18, 0, 0, 30), new Date(2026, 9, 31, 23, 59)]) {
      const today = presetTimeRange('today', now)
      const start = new Date(today.startTime)
      const end = new Date(today.endTime)
      expect([start.getDate(), start.getHours(), start.getMinutes()]).toEqual([now.getDate(), 0, 0])
      expect(end.getHours()).toBe(0)
      expect(end > now).toBe(true)
      expect(new Date(end.getTime() - 1).getDate()).toBe(now.getDate())
    }
  })
})
