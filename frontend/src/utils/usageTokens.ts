export type TokenPeriod = '' | 'total' | 'today'
type TokenFields = 'input_tokens' | 'output_tokens' | 'cache_read_tokens' | 'cache_creation_tokens'
export type TokenUsageSource = Partial<Record<TokenFields | `total_${TokenFields}` | `today_${TokenFields}` | 'total_tokens' | 'today_tokens', number>>

export interface TokenBreakdown {
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  total: number
}

export function tokenBreakdown(source?: TokenUsageSource | null, period: TokenPeriod = ''): TokenBreakdown {
  const prefix = period ? `${period}_` : ''
  const read = (field: TokenFields) => source?.[`${prefix}${field}` as keyof TokenUsageSource] ?? 0
  const input = read('input_tokens')
  const output = read('output_tokens')
  const cacheRead = read('cache_read_tokens')
  const cacheWrite = read('cache_creation_tokens')
  return {
    input, output, cacheRead, cacheWrite,
    total: source?.[period === 'today' ? 'today_tokens' : 'total_tokens'] ?? input + output + cacheRead + cacheWrite
  }
}

export function formatExactTokens(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value) || value < 0) return '—'
  return value.toLocaleString('en-US', { maximumFractionDigits: 0 })
}

/** Shared Token display: decimal K/M/B, at most two decimals, no trailing zeros. */
export function formatTokenCount(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value) || value < 0) return '—'
  const units = [{ divisor: 1e9, suffix: 'B' }, { divisor: 1e6, suffix: 'M' }, { divisor: 1e3, suffix: 'K' }]
  for (const { divisor, suffix } of units) {
    // Promote rounding boundaries, e.g. 999,999 -> 1M rather than 1000K.
    if (value >= divisor || (divisor > 1e3 && Math.round(value / (divisor / 1000) * 100) / 100 >= 1000)) {
      return `${Number((value / divisor).toFixed(2))}${suffix}`
    }
  }
  return formatExactTokens(value)
}
