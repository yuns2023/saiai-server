/**
 * Sharing market (P3a) helpers for the admin console: tier eligibility of a
 * group form, error-code → message keys, and exact checks of the market
 * settings the server validates (so 0 is a value, never "empty").
 */
import type { MarketProtection } from '@/api/admin/market'

/** Static bounds (service.MarketProtectionParams.ValidRange / migration 134 CHECK). */
export const MARKET_PROTECTION_BOUNDS: Record<keyof MarketProtection, { min: number; max: number }> = {
  reserved_concurrency: { min: 0, max: 100 },
  reserved_devices: { min: 0, max: 32 },
  reserved_admissions: { min: 0, max: 32 }
}

/* ---------- Group tier ---------- */

export interface MarketTierForm {
  platform?: string
  subscription_type: string
  hosted_usage_enabled?: boolean
  rate_multiplier: number | string | null | undefined
  fallback_group_id?: number | null
}

/**
 * Mirrors backend Group.ValidateMarketTier: a standard (balance) anthropic
 * group without hosted metering, a rate > 0 at 4 decimals, and no
 * claude_code_only fallback of its own.
 */
export function isMarketTierEligible(form: MarketTierForm): boolean {
  const rate = form.rate_multiplier
  const n = rate === '' || rate === null || rate === undefined ? NaN : Number(rate)
  return (
    form.platform === 'anthropic' &&
    form.subscription_type === 'standard' &&
    !form.hosted_usage_enabled &&
    Number.isFinite(n) &&
    Math.round(n * 10000) > 0 &&
    (form.fallback_group_id === null || form.fallback_group_id === undefined || form.fallback_group_id === 0)
  )
}

/* ---------- Errors ---------- */

/** Server error codes with their own message under market.errors. */
export const MARKET_ADMIN_ERROR_CODES = [
  'INVALID_MARKET_TIER',
  'MARKET_TIER_FALLBACK',
  'MARKET_TIER_RATE_OVERRIDE',
  'MARKET_TIER_GUARD_UNAVAILABLE',
  'MARKET_MEMBERSHIP_CONFLICT',
  'MARKET_LISTING_NOT_FOUND',
  'MARKET_ACCOUNT_NO_OWNER',
  'MARKET_INVALID_TIER',
  'MARKET_INVALID_PARAMS',
  'MARKET_ACCOUNT_NOT_ELIGIBLE',
  'MARKET_EARNING_REVERSE_SCOPE_REQUIRED',
  'MARKET_EARNING_REVERSE_AS_OF_INVALID',
  'MARKET_EARNING_NOTE_TOO_LONG',
  'MARKET_EARNING_INVALID_QUERY',
  'MARKET_EARNING_UNAVAILABLE'
] as const

/** The market error code of a rejected call, or ''. */
export function marketErrorCode(err: unknown): string {
  const e = (err ?? {}) as { reason?: unknown; code?: unknown }
  for (const v of [e.reason, e.code]) {
    if (typeof v === 'string' && (MARKET_ADMIN_ERROR_CODES as readonly string[]).includes(v)) return v
  }
  return ''
}

/**
 * Message for a failed market-related admin call: a known code gets its
 * plain-language text plus the server's own wording (it carries the exact
 * bound, e.g. "reserved_concurrency must be at most …"); anything else gets
 * the server message, or the fallback.
 */
export function marketErrorText(err: unknown, t: (key: string) => string, fallback: string): string {
  const code = marketErrorCode(err)
  const message = (err as { message?: unknown } | null)?.message
  const server = typeof message === 'string' ? message.trim() : ''
  if (code) {
    const text = t(`market.errors.${code}`)
    // These carry the exact bound / reason only in the server wording.
    return server && (code === 'MARKET_INVALID_PARAMS' || code === 'MARKET_ACCOUNT_NOT_ELIGIBLE') ? `${text}（${server}）` : text
  }
  return server || fallback
}

/* ---------- Protection parameters ---------- */

export const PROTECTION_KEYS = Object.keys(MARKET_PROTECTION_BOUNDS) as Array<keyof MarketProtection>

/** Integer within the static bound. */
export function protectionValueOk(key: keyof MarketProtection, v: unknown): boolean {
  const b = MARKET_PROTECTION_BOUNDS[key]
  return typeof v === 'number' && Number.isInteger(v) && v >= b.min && v <= b.max
}

/** First protection key that is not a valid integer in range, or null. */
export function firstBadProtection(p: Record<keyof MarketProtection, unknown>): keyof MarketProtection | null {
  return PROTECTION_KEYS.find((k) => !protectionValueOk(k, p[k])) ?? null
}

/* ---------- Settings ---------- */

/** Exact decimal places of a finite number as typed (0.2 → 1, 0.1234 → 4). */
function decimals(n: number): number {
  const s = String(n)
  if (/e-/i.test(s)) {
    const [base, exp] = s.toLowerCase().split('e-')
    return (base.split('.')[1]?.length ?? 0) + Number(exp)
  }
  return s.split('.')[1]?.length ?? 0
}

/** 0 ≤ x ≤ 1 with at most 4 decimals; 0 is valid (no cut). */
export function validCutRate(v: unknown): boolean {
  return typeof v === 'number' && Number.isFinite(v) && v >= 0 && v <= 1 && decimals(v) <= 4
}

/** 0.01 ≤ x ≤ 10000 with at most 2 decimals. */
export function validMinConvert(v: unknown): boolean {
  return typeof v === 'number' && Number.isFinite(v) && v >= 0.01 && v <= 10000 && decimals(v) <= 2
}

export function validIntIn(v: unknown, min: number, max: number): boolean {
  return typeof v === 'number' && Number.isInteger(v) && v >= min && v <= max
}

export const MARKET_FREEZE_DAYS_MAX = 90
