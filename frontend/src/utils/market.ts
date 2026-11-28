/**
 * Sharing market helpers for the admin console: tier eligibility of a group
 * form, the 套餐 (plan) of tiers and accounts, error-code → message keys, and
 * exact checks of the market settings the server validates (so 0 is a value,
 * never "empty").
 */
import type { AdminGroup } from '@/types'

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

/* ---------- 套餐 (plan) ---------- */

/** Server slug rule for a 套餐 (groups.market_plan, approval and listing plan). */
export const MARKET_PLAN_RE = /^[a-z0-9][a-z0-9_-]{0,31}$/

export function validMarketPlan(v: unknown): v is string {
  return typeof v === 'string' && MARKET_PLAN_RE.test(v)
}

type PlanGroup = Pick<AdminGroup, 'id' | 'name' | 'platform' | 'status' | 'rate_multiplier' | 'market_tier' | 'market_plan'>

/** Active market tiers that carry a 套餐 (optionally of one platform): the tiers an account can be sold in. */
export function planTiers<G extends PlanGroup>(groups: readonly G[], platform?: string): G[] {
  return groups.filter((g) => g.market_tier === true && g.status === 'active' && !!g.market_plan && (!platform || g.platform === platform))
}

/** Distinct 套餐 of those tiers, sorted: the choices offered to an administrator. */
export function marketPlanOptions(groups: readonly PlanGroup[], platform?: string): string[] {
  return [...new Set(planTiers(groups, platform).map((g) => g.market_plan as string))].sort()
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
  'MARKET_PLAN_REQUIRES_TIER',
  'MARKET_PLAN_UNKNOWN',
  'MARKET_PLAN_INVALID',
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
 * plain-language text, plus the server's own wording for the codes whose
 * detail is only there (which parameter, why not eligible); anything else
 * gets the server message, or the fallback.
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
/** Owner 自用 / 共享 switch cooldown, hours (0 = no limit). */
export const MARKET_SWITCH_COOLDOWN_MAX = 168
