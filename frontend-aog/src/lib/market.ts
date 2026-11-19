/**
 * Sharing market helpers: plain-language status / reason / error keys and
 * exact decimal arithmetic for earnings (the ledger keeps 10 decimals, more
 * than lib/decimal handles).
 */
import type { OwnerEarnings, OwnerMarketView } from '@/api/market'

/* ---------- Status and reasons ---------- */

/** What the owner sees for one account: 未获准 / 可上架 / 已上架 / 暂不可售. */
export type MarketState = 'notAllowed' | 'canList' | 'listed' | 'unavailable'

/** Reason codes with their own message (under market.reason_<code>); others fall back to a generic text. */
export const KNOWN_REASONS = [
  'market_disabled',
  'not_configured',
  'no_allowed_tier',
  'protection_invalid',
  'platform_not_anthropic',
  'not_setup_token',
  'not_carpool',
  'unlimited_devices',
  'max_sessions_enabled',
  'no_owner',
  'owner_paused',
  'withdraw_requested',
  'proxy_unavailable',
  'account_inactive'
] as const

/** i18n key for a reason code; unknown codes get the generic text (never the raw code alone). */
export function reasonKey(code: string): string {
  return (KNOWN_REASONS as readonly string[]).includes(code) ? `market.reason_${code}` : 'market.reason_other'
}

/**
 * The account is (still) on the market or an administrator allows it into at
 * least one tier now. A configuration with every tier removed counts as none.
 */
export function hasPermission(v: OwnerMarketView | null | undefined): boolean {
  if (!v) return false
  if (v.status === 'listed') return true
  return v.status === 'unlisted' && v.allowed_tiers.length > 0
}

export function marketState(v: OwnerMarketView): MarketState {
  if (v.status === 'listed') return v.unavailable_reasons.length ? 'unavailable' : 'listed'
  if (!hasPermission(v)) return 'notAllowed'
  return v.can_list ? 'canList' : 'unavailable'
}

/**
 * Reasons worth showing: "not configured" is already said by the 未获准 label,
 * and duplicates are dropped.
 */
export function visibleReasons(v: OwnerMarketView): string[] {
  const out: string[] = []
  for (const r of v.unavailable_reasons) if (r !== 'not_configured' && !out.includes(r)) out.push(r)
  return out
}

/** Tiers the owner can pick: live and not already an admin binding. */
export function pickableTiers(v: OwnerMarketView) {
  return v.allowed_tiers.filter((t) => t.available && !t.admin_member)
}

/* ---------- Errors ---------- */

export const MARKET_ERROR_CODES = [
  'MARKET_DISABLED',
  'MARKET_GROUP_NOT_ALLOWED',
  'MARKET_ADMIN_MEMBERSHIP_CONFLICT',
  'MARKET_LISTING_REVOKED',
  'MARKET_ACCOUNT_NOT_ELIGIBLE',
  'MARKET_INVALID_PARAMS',
  'HOSTED_ACCOUNT_NOT_FOUND',
  'MARKET_EARNING_INVALID_AMOUNT',
  'MARKET_EARNING_INVALID_REQUEST_ID',
  'MARKET_EARNING_BELOW_MINIMUM',
  'MARKET_EARNING_INSUFFICIENT',
  'MARKET_EARNING_CONVERT_CONFLICT',
  'BALANCE_OUT_OF_RANGE',
  'MARKET_EARNING_UNAVAILABLE',
  'MARKET_EARNING_INVALID_QUERY'
] as const

interface ErrLike {
  status?: unknown
  reason?: unknown
  code?: unknown
  metadata?: Record<string, string>
}

/** i18n key (under market.errors) for a failed market call. */
export function marketErrorKey(err: unknown): string {
  const e = (err ?? {}) as ErrLike
  for (const v of [e.reason, e.code]) {
    if (typeof v === 'string' && (MARKET_ERROR_CODES as readonly string[]).includes(v)) return `market.errors.${v}`
  }
  if (e.status === 0) return 'market.errors.network'
  if (e.status === 404) return 'market.errors.HOSTED_ACCOUNT_NOT_FOUND'
  return 'market.errors.generic'
}

/** metadata.reason of MARKET_ACCOUNT_NOT_ELIGIBLE, when it is a plain code. */
export function errorReason(err: unknown): string {
  const r = ((err ?? {}) as ErrLike).metadata?.reason
  return typeof r === 'string' && /^[a-z_]{1,64}$/.test(r) ? r : ''
}

/* ---------- Exact decimals (10 places) ---------- */

const SCALE = 10
const TEN = BigInt(10)
const ZERO = BigInt(0)
const UNIT = TEN ** BigInt(SCALE)
const CENT = TEN ** BigInt(SCALE - 2)
const DECIMAL_RE = /^(-)?(\d+)(?:\.(\d+))?$/

/** Integer units at 10 decimals, or null when `s` is not a plain decimal (extra digits must be zeros). */
export function units(s: string | null | undefined): bigint | null {
  if (typeof s !== 'string') return null
  const m = DECIMAL_RE.exec(s.trim())
  if (!m) return null
  const frac = m[3] ?? ''
  if (frac.length > SCALE && /[^0]/.test(frac.slice(SCALE))) return null
  const v = BigInt(m[2] + frac.slice(0, SCALE).padEnd(SCALE, '0'))
  return m[1] ? -v : v
}

/** Plain decimal string for units, trailing zeros trimmed down to `minDigits` places. */
export function fromUnits(v: bigint, minDigits = 0): string {
  const neg = v < ZERO
  const abs = neg ? -v : v
  const int = abs / UNIT
  const frac = (abs % UNIT).toString().padStart(SCALE, '0').replace(/0+$/, '').padEnd(minDigits, '0')
  return `${neg ? '-' : ''}${int}${frac ? `.${frac}` : ''}`
}

export function isNonZero(s: string | null | undefined): boolean {
  const v = units(s)
  return v !== null && v !== ZERO
}

/** "$1.20", "$0.0000012" (all significant digits, at least 2), "-$0.80"; "—" when unreadable. */
export function usdExact(s: string | null | undefined): string {
  const v = units(s)
  if (v === null) return '—'
  const text = fromUnits(v < ZERO ? -v : v, 2)
  return `${v < ZERO ? '-' : ''}$${text}`
}

/** Available amount rounded DOWN to whole cents ("全部"); "0" when nothing or unreadable. */
export function floorToCents(s: string | null | undefined): string {
  const v = units(s)
  if (v === null || v <= ZERO) return '0'
  return fromUnits((v / CENT) * CENT)
}

export type AmountProblem = 'invalid' | 'decimals' | 'belowMin' | 'aboveAvailable'

/**
 * Checks a typed conversion amount: a positive plain decimal with at most 2
 * places, ≥ min, ≤ available (both exact strings). Returns the normalised
 * amount ("2.5") or the problem.
 */
export function checkConvertAmount(input: string, min: string, available: string): { amount: string } | { problem: AmountProblem } {
  const s = input.trim()
  // Decimal places are judged on the raw input: "1.2300" and "1." are refused, not normalised.
  if (/^\d+\.\d{3,}$/.test(s)) return { problem: 'decimals' }
  const m = /^(\d+)(?:\.(\d{1,2}))?$/.exec(s)
  if (!m) return { problem: 'invalid' }
  const frac = (m[2] ?? '').replace(/0+$/, '')
  const amount = `${m[1].replace(/^0+(?=\d)/, '')}${frac ? `.${frac}` : ''}`
  const v = units(amount) as bigint
  if (v <= ZERO) return { problem: 'invalid' }
  const lo = units(min)
  if (lo !== null && v < lo) return { problem: 'belowMin' }
  const hi = units(available)
  if (hi === null || v > hi) return { problem: 'aboveAvailable' }
  return { amount }
}

/** Whether the owner has anything in the ledger worth showing. */
export function hasEarnings(e: OwnerEarnings | null | undefined): boolean {
  return !!e && (isNonZero(e.frozen) || isNonZero(e.available) || isNonZero(e.converted_total))
}

/** Available covers at least the minimum conversion. */
export function canConvert(e: OwnerEarnings | null | undefined): boolean {
  if (!e) return false
  const avail = units(floorToCents(e.available)) ?? ZERO
  const min = units(e.min_convert) ?? ZERO
  return avail > ZERO && avail >= min
}

/* ---------- Idempotency key ---------- */

/** RFC 4122 v4 UUID (crypto.randomUUID when available). */
export function newRequestId(): string {
  const c = globalThis.crypto as Crypto | undefined
  if (c && typeof c.randomUUID === 'function') return c.randomUUID()
  const b = new Uint8Array(16)
  if (c && typeof c.getRandomValues === 'function') c.getRandomValues(b)
  else for (let i = 0; i < 16; i++) b[i] = Math.floor(Math.random() * 256)
  b[6] = (b[6] & 0x0f) | 0x40
  b[8] = (b[8] & 0x3f) | 0x80
  const h = Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}
