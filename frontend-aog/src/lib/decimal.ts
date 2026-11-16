/**
 * Exact helpers for decimal money strings ("12.345678") from the crypto-pay API.
 * Values are scaled to integer units with BigInt; never through Number, so
 * nothing is rounded or lost to binary floating point.
 */

/** Fractional digits kept internally (token units are 6 decimals). */
const SCALE = 6
const DECIMAL_RE = /^(-)?(\d+)(?:\.(\d+))?$/

/** Integer units at `SCALE` decimals, or null if `s` is not a plain decimal / has more than `SCALE` decimals. */
export function toUnits(s: string | null | undefined): bigint | null {
  if (typeof s !== 'string') return null
  const m = DECIMAL_RE.exec(s.trim())
  if (!m) return null
  const frac = m[3] ?? ''
  if (frac.length > SCALE) {
    // Extra digits are only accepted when they are zeros ("1.0000000").
    if (/[^0]/.test(frac.slice(SCALE))) return null
  }
  const units = BigInt(m[2] + frac.slice(0, SCALE).padEnd(SCALE, '0'))
  return m[1] ? -units : units
}

/** Decimal string for integer units, trailing zeros trimmed to at least `minDigits` places. */
export function fromUnits(units: bigint, minDigits = 0): string {
  const neg = units < BigInt(0)
  const abs = neg ? -units : units
  const raw = abs.toString().padStart(SCALE + 1, '0')
  const int = raw.slice(0, raw.length - SCALE)
  const frac = raw.slice(raw.length - SCALE).replace(/0+$/, '').padEnd(minDigits, '0')
  return `${neg ? '-' : ''}${int}${frac ? `.${frac}` : ''}`
}

/** -1 / 0 / 1; an unparsable value sorts as zero. */
export function compareDecimal(a: string | null | undefined, b: string | null | undefined): number {
  const x = toUnits(a) ?? BigInt(0)
  const y = toUnits(b) ?? BigInt(0)
  return x < y ? -1 : x > y ? 1 : 0
}

export function isPositive(s: string | null | undefined): boolean {
  const u = toUnits(s)
  return u !== null && u > BigInt(0)
}

/** "$10.00", "$12.345678": at least two decimals, the rest only when non-zero. */
export function usd(s: string | null | undefined): string {
  const u = toUnits(s)
  if (u === null) return '—'
  const text = fromUnits(u < BigInt(0) ? -u : u, 2)
  return `${u < BigInt(0) ? '-' : ''}$${text}`
}

/**
 * Validates a user-typed amount: plain decimal, at most `maxDecimals` places,
 * within [min, max]. Returns the normalised string ("10", "10.5") or null.
 */
export function normaliseAmount(input: string, min: string, max: string, maxDecimals = 2): string | null {
  const s = input.trim()
  const m = /^(\d+)(?:\.(\d*))?$/.exec(s)
  if (!m) return null
  const frac = (m[2] ?? '').replace(/0+$/, '')
  if (frac.length > maxDecimals) return null
  const value = `${m[1].replace(/^0+(?=\d)/, '')}${frac ? `.${frac}` : ''}`
  if (compareDecimal(value, min) < 0 || compareDecimal(value, max) > 0) return null
  return value
}
