import { describe, expect, it } from 'vitest'
import { formatLatency, formatTokensPerSecond, outputTokensPerSecond } from '../usageLatency'

describe('usage latency presentation', () => {
  it.each([
    [0, '0ms'], [12.4, '12ms'], [999, '999ms'], [1000, '1.00s'], [14130, '14.13s'],
    [null, '-'], [undefined, '-'], [NaN, '-'], [-1, '-']
  ])('formats %s as %s', (value, result) => expect(formatLatency(value)).toBe(result))

  it('measures output speed over the generation window after the first token', () => {
    // 639 tokens over 14.13s - 7.75s = 6.38s
    expect(outputTokensPerSecond({ first_token_ms: 7750, duration_ms: 14130, output_tokens: 639 })).toBeCloseTo(100.16, 2)
  })

  it('includes the exact thresholds', () => {
    expect(outputTokensPerSecond({ first_token_ms: 1000, duration_ms: 1200, output_tokens: 20 })).toBe(100)
  })

  it.each([
    ['non-stream (no first token)', { first_token_ms: null, duration_ms: 5000, output_tokens: 500 }],
    ['too few output tokens', { first_token_ms: 100, duration_ms: 5000, output_tokens: 19 }],
    ['generation window too short', { first_token_ms: 1000, duration_ms: 1199, output_tokens: 500 }],
    ['first token after duration', { first_token_ms: 3000, duration_ms: 2000, output_tokens: 500 }]
  ])('omits output speed for %s', (_, log) => expect(outputTokensPerSecond(log)).toBeNull())

  it('formats output speed with one decimal', () => {
    expect(formatTokensPerSecond(56.73)).toBe('56.7 tok/s')
    expect(formatTokensPerSecond(null)).toBe('-')
  })
})
