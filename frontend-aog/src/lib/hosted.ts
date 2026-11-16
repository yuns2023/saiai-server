import { rate } from './format'

/**
 * Date range for the hosted-account usage API: local calendar days in the UI,
 * a half-open UTC window [start of first day, start of the day after the last
 * day) on the wire, at most 31 days long (the server rejects longer spans).
 */

export const MAX_SPAN_DAYS = 31
const DAY_MS = 86_400_000

export type RangeProblem = 'invalid' | 'inverted' | 'tooLong'

/** Local midnight of a YYYY-MM-DD date, or null when the string is not a real date. */
export function localMidnight(ymd: string): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(ymd)
  if (!m) return null
  const [y, mo, d] = [Number(m[1]), Number(m[2]) - 1, Number(m[3])]
  const date = new Date(y, mo, d)
  // Rejects 2026-02-30 and the like, which Date would roll over.
  if (date.getFullYear() !== y || date.getMonth() !== mo || date.getDate() !== d) return null
  return date
}

/** RFC3339 in UTC without fractional seconds. */
export function rfc3339(d: Date): string {
  return d.toISOString().replace(/\.\d{3}Z$/, 'Z')
}

/** Why a from/to pair cannot be sent, or null when it can. */
export function rangeProblem(from: string, to: string): RangeProblem | null {
  const start = localMidnight(from)
  const last = localMidnight(to)
  if (!start || !last) return 'invalid'
  if (last < start) return 'inverted'
  const end = new Date(last.getFullYear(), last.getMonth(), last.getDate() + 1)
  // Both the calendar days and the real length must fit (a DST change can add an hour).
  const days = Math.round((end.getTime() - start.getTime()) / DAY_MS)
  if (days > MAX_SPAN_DAYS || end.getTime() - start.getTime() > MAX_SPAN_DAYS * DAY_MS) return 'tooLong'
  return null
}

/** Inclusive local days from..to as the API's half-open UTC window; null when invalid. */
export function utcWindow(from: string, to: string): { start: string; end: string } | null {
  if (rangeProblem(from, to)) return null
  const start = localMidnight(from) as Date
  const last = localMidnight(to) as Date
  const end = new Date(last.getFullYear(), last.getMonth(), last.getDate() + 1)
  return { start: rfc3339(start), end: rfc3339(end) }
}

/* ---------- Hosted groups (own-account hosting) ---------- */

interface RatedGroup {
  id: number
  rate_multiplier: number
  hosted_usage_enabled?: boolean
}

export function isHostedGroup(g: { hosted_usage_enabled?: boolean } | null | undefined): boolean {
  return g?.hosted_usage_enabled === true
}

/**
 * The rate a key on this group is billed at. A hosted group's rate is its
 * service fee (standard price × rate); user overrides never apply to it.
 */
export function groupRate(g: RatedGroup, overrides: Record<number, number>): number {
  return isHostedGroup(g) ? g.rate_multiplier : overrides[g.id] ?? g.rate_multiplier
}

/** A fee rate as "1.2" (no trailing zeros, at most 4 decimals). */
export function feeRate(value: number): string {
  return String(Number(value.toFixed(4)))
}

type Translate = (key: string, named: Record<string, unknown>) => string

/** What a group costs, for key lists and pickers: "×0.8", or the hosting fee / "not charged" for hosted groups. */
export function groupRateText(g: RatedGroup, overrides: Record<number, number>, t: Translate): string {
  const r = groupRate(g, overrides)
  if (!isHostedGroup(g)) return rate(r)
  return r > 0 ? t('keys.hostedFee', { rate: feeRate(r) }) : t('keys.hostedFree', {})
}
