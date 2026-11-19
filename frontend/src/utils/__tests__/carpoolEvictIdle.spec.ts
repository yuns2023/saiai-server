import { describe, expect, it } from 'vitest'
import { DEFAULT_CARPOOL_EVICT_IDLE_HOURS, normalizeCarpoolEvictIdleHours } from '../carpoolEvictIdle'

describe('normalizeCarpoolEvictIdleHours', () => {
  it('defaults when absent or invalid', () => {
    for (const v of [undefined, null, '', '  ', 'abc', '0b0', '0x10', 'Infinity', Number.NaN, Number.POSITIVE_INFINITY, false, true, {}]) {
      expect(normalizeCarpoolEvictIdleHours(v)).toBe(DEFAULT_CARPOOL_EVICT_IDLE_HOURS)
    }
  })
  it('keeps an explicit zero', () => {
    expect(normalizeCarpoolEvictIdleHours(0)).toBe(0)
    expect(normalizeCarpoolEvictIdleHours('0')).toBe(0)
  })
  it('truncates and clamps', () => {
    expect(normalizeCarpoolEvictIdleHours(6.9)).toBe(6)
    expect(normalizeCarpoolEvictIdleHours(-1)).toBe(0)
    expect(normalizeCarpoolEvictIdleHours(9999)).toBe(720)
    expect(normalizeCarpoolEvictIdleHours('12')).toBe(12)
    expect(normalizeCarpoolEvictIdleHours(' 1.2e1 ')).toBe(12)
  })
})
