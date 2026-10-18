import { describe, expect, it } from 'vitest'
import { cacheReadRate, formatCacheReadRate, formatExactTokens, formatTokenCount, tokenBreakdown } from '../usageTokens'

describe('shared Token presentation', () => {
  it.each([
    [0, '0'], [54, '54'], [999, '999'], [1000, '1K'], [14906, '14.91K'],
    [21620, '21.62K'], [4900, '4.9K'], [999999, '1M'], [1000000, '1M'],
    [999999999, '1B'], [1234567890, '1.23B'], [null, '—'], [undefined, '—'], [NaN, '—']
  ])('formats %s as %s', (value, result) => expect(formatTokenCount(value)).toBe(result))

  it('uses exact integers without compact rounding for reconciliation', () => {
    expect(formatExactTokens(21620)).toBe('21,620')
    expect(formatExactTokens(14906)).toBe('14,906')
  })

  it.each(['', 'today', 'total'] as const)('includes both cache categories for %s data', period => {
    const prefix = period ? `${period}_` : ''
    const source = {
      [`${prefix}input_tokens`]: 14906, [`${prefix}output_tokens`]: 54,
      [`${prefix}cache_read_tokens`]: 6500, [`${prefix}cache_creation_tokens`]: 160,
      [period === 'today' ? 'today_tokens' : 'total_tokens']: 21620
    }
    expect(tokenBreakdown(source, period)).toEqual({ input: 14906, output: 54, cacheRead: 6500, cacheWrite: 160, total: 21620 })
  })

  it('includes caches in record totals without replacing an explicit server total', () => {
    expect(tokenBreakdown({ input_tokens: 10, output_tokens: 5, cache_read_tokens: 100, cache_creation_tokens: 20 }).total).toBe(135)
    expect(tokenBreakdown({ total_tokens: 0, input_tokens: 10 }).total).toBe(0)
  })
})


describe('cache read rate', () => {
  it('excludes output, includes writes, and uses full precision', () => {
    expect(cacheReadRate({ input_tokens: 10, cache_read_tokens: 80, cache_creation_tokens: 10, output_tokens: 1000 })).toBe(80)
    expect(formatCacheReadRate(cacheReadRate({ input_tokens: 8390000, cache_read_tokens: 419130000, cache_creation_tokens: 1570000 }))).toBe('97.68%')
  })
  it.each(['today', 'total'] as const)('supports %s counters', period => {
    expect(cacheReadRate({ [`${period}_input_tokens`]: 10, [`${period}_cache_read_tokens`]: 80, [`${period}_cache_creation_tokens`]: 10 }, period)).toBe(80)
  })
  it('distinguishes cold input from missing data and no input', () => {
    expect(cacheReadRate({ input_tokens: 100, cache_read_tokens: 0, cache_creation_tokens: 0 })).toBe(0)
    for (const data of [null, {}, { input_tokens: 10 }, { input_tokens: 0, cache_read_tokens: 0, cache_creation_tokens: 0 }, { input_tokens: -1, cache_read_tokens: 10, cache_creation_tokens: 0 }]) {
      expect(cacheReadRate(data)).toBeNull()
    }
    expect(formatCacheReadRate(null)).toBe('—')
  })
})
