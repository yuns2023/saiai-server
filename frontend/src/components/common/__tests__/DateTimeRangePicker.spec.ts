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
  it('uses exact 24 hours through the current minute and an exclusive midnight for yesterday', () => {
    const now = new Date(2026, 9, 18, 18, 13, 45)
    const rolling = presetTimeRange('last24Hours', now)
    expect(+new Date(rolling.endTime) - +new Date(rolling.startTime)).toBe(86400000)
    expect(rolling.endTime).toBe(new Date(2026, 9, 18, 18, 14).toISOString())
    const yesterday = presetTimeRange('yesterday', now)
    expect(new Date(yesterday.startTime).getDate()).toBe(17)
    expect(new Date(yesterday.endTime).getDate()).toBe(18)
    expect(new Date(yesterday.endTime).getHours()).toBe(0)
    const lastMonth = presetTimeRange('lastMonth', now)
    expect([lastMonth.startTime, lastMonth.endTime]).toEqual([new Date(2026, 8, 1).toISOString(), new Date(2026, 9, 1).toISOString()])
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
  it.each([
    ['7days', new Date(2026, 9, 12)], ['14days', new Date(2026, 9, 5)], ['30days', new Date(2026, 8, 19)],
    ['thisMonth', new Date(2026, 9, 1)],
  ])('runs %s through the end of today so new rows keep matching', (preset, start) => {
    const range = presetTimeRange(preset, new Date(2026, 9, 18, 16, 19, 30))
    expect(range.startTime).toBe(start.toISOString())
    expect(range.endTime).toBe(new Date(2026, 9, 19).toISOString())
  })
  it('keeps the current minute and calendar days in the repeated DST hour', () => {
    const tz = process.env.TZ
    process.env.TZ = 'America/Los_Angeles'
    try {
      const now = new Date('2026-11-01T09:30:45Z') // the second 01:30, PST
      expect(presetTimeRange('last24Hours', now).endTime).toBe('2026-11-01T09:31:00.000Z')
      const today = presetTimeRange('today', now)
      expect([today.startTime, today.endTime]).toEqual(['2026-11-01T07:00:00.000Z', '2026-11-02T08:00:00.000Z'])
    } finally {
      if (tz === undefined) delete process.env.TZ
      else process.env.TZ = tz
    }
  })
  it('applies a preset without losing the DST offset', async () => {
    const tz = process.env.TZ
    process.env.TZ = 'America/Los_Angeles'
    vi.useFakeTimers()
    try {
      vi.setSystemTime(new Date('2026-11-01T08:59:45Z')) // 01:59:45 PDT; the next minute is 01:00 PST
      const wrapper = mount(DateTimeRangePicker, { props })
      await wrapper.find('button').trigger('click')
      await wrapper.findAll('.grid button').find((button) => button.text() === 'dates.last24Hours')!.trigger('click')
      await wrapper.findAll('button').at(-1)!.trigger('click')
      const range = wrapper.emitted('change')![0][0] as { startTime: string; endTime: string }
      expect(range.endTime).toBe('2026-11-01T09:00:00.000Z')
      expect(+new Date(range.endTime) - +new Date(range.startTime)).toBe(86400000)
    } finally {
      vi.useRealTimers()
      if (tz === undefined) delete process.env.TZ
      else process.env.TZ = tz
    }
  })
  it('re-evaluates an unedited preset when it is applied', async () => {
    vi.useFakeTimers()
    try {
      vi.setSystemTime(new Date(2026, 9, 28, 16, 19, 30))
      const wrapper = mount(DateTimeRangePicker, { props })
      await wrapper.find('button').trigger('click')
      await wrapper.findAll('.grid button').find((button) => button.text() === 'dates.last24Hours')!.trigger('click')
      vi.setSystemTime(new Date(2026, 9, 28, 16, 25, 5))
      await wrapper.findAll('button').at(-1)!.trigger('click')
      expect(wrapper.emitted('change')![0]).toEqual([presetTimeRange('last24Hours', new Date(2026, 9, 28, 16, 25, 5))])
      expect(wrapper.emitted('change')![0][0]).toMatchObject({ endTime: new Date(2026, 9, 28, 16, 26).toISOString(), preset: 'last24Hours' })
    } finally {
      vi.useRealTimers()
    }
  })
})
