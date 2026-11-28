/**
 * Sharing market helpers: plain-language status / reason / error keys and
 * exact decimal arithmetic for earnings (the ledger keeps 10 decimals, more
 * than lib/decimal handles).
 */
import type { OwnerEarnings, OwnerMarketView } from '@/api/market'

/* ---------- Status and reasons ---------- */

/**
 * What the owner sees for one account: 未开通 (no 套餐 / revoked / never
 * set up), 自用 (not on the market) or 共享中. A shared account is always
 * 共享中, so it can be switched back even if its 套餐 went away meanwhile.
 */
export type ShareState = 'notGranted' | 'selfUse' | 'shared'

/** Reason codes with their own message (under market.reason_<code>); others fall back to a generic text. */
export const KNOWN_REASONS = [
  'market_disabled',
  'not_configured',
  'no_plan',
  'switch_cooldown',
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

export function shareState(v: OwnerMarketView): ShareState {
  if (v.mode === 'shared' || v.status === 'listed') return 'shared'
  if (v.status !== 'unlisted' || !v.plan || v.unavailable_reasons.includes('no_plan')) return 'notGranted'
  return 'selfUse'
}

/** The site-wide market switch is off (said on a line of its own, apart from 未开通). */
export function marketClosed(v: OwnerMarketView): boolean {
  return !v.market_enabled || v.unavailable_reasons.includes('market_disabled')
}

/** The account is shared now or may be shared right away: the 共享收益 section is relevant. */
export function sharesOrCanShare(v: OwnerMarketView | null | undefined): boolean {
  if (!v) return false
  const s = shareState(v)
  return s === 'shared' || (s === 'selfUse' && v.can_list)
}

/**
 * Reasons worth listing: those already said by a line of their own (未开通,
 * market closed, the next-switch time) are dropped, and duplicates.
 */
export function visibleReasons(v: OwnerMarketView): string[] {
  const own = ['not_configured', 'no_plan', 'market_disabled']
  if (v.next_switch_at) own.push('switch_cooldown')
  const out: string[] = []
  for (const r of v.unavailable_reasons) if (!own.includes(r) && !out.includes(r)) out.push(r)
  return out
}

/** Tiers the owner can pick: live and not already an admin binding. */
export function pickableTiers(v: OwnerMarketView) {
  return v.allowed_tiers.filter((t) => t.available && !t.admin_member)
}

/** Upstream 5h / 7d utilization in percent; null = unknown (no sample). */
export function upstreamUsage(v: OwnerMarketView): { fiveHour: number | null; sevenDay: number | null } {
  const pick = (key: string) => {
    const u = v.upstream_windows.find((w) => w.key === key)?.utilization
    return typeof u === 'number' && Number.isFinite(u) ? u : null
  }
  return { fiveHour: pick('five_hour'), sevenDay: pick('seven_day') }
}

/* ---------- Errors ---------- */

export const MARKET_ERROR_CODES = [
  'MARKET_DISABLED',
  'MARKET_SWITCH_COOLDOWN',
  'MARKET_TIER_REQUIRED',
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

function errorCode(err: unknown): string {
  const e = (err ?? {}) as ErrLike
  for (const v of [e.reason, e.code]) if (typeof v === 'string' && v) return v
  return ''
}

/** A switch refused by the cooldown (429 MARKET_SWITCH_COOLDOWN; a bare 429 from elsewhere is not one). */
export function isCooldownError(err: unknown): boolean {
  return errorCode(err) === 'MARKET_SWITCH_COOLDOWN'
}

/** 400 MARKET_TIER_REQUIRED: the 套餐 has several tiers, one must be picked. */
export function isTierRequiredError(err: unknown): boolean {
  return errorCode(err) === 'MARKET_TIER_REQUIRED'
}

/** metadata.next_switch_at of a cooldown error, when it is a readable time. */
export function errorNextSwitchAt(err: unknown): string | null {
  const at = ((err ?? {}) as ErrLike).metadata?.next_switch_at
  return typeof at === 'string' && at && !Number.isNaN(Date.parse(at)) ? at : null
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
