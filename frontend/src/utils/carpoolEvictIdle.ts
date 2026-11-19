// Mirrors Account.GetCarpoolEvictIdleHours on the server: absent/null/invalid
// means 24 hours, an explicit 0 lifts the restriction, values clamp to 0–720.
export const DEFAULT_CARPOOL_EVICT_IDLE_HOURS = 24

export function normalizeCarpoolEvictIdleHours(value: unknown): number {
  if (value === null || value === undefined || value === '') return DEFAULT_CARPOOL_EVICT_IDLE_HOURS
  const n = typeof value === 'number' ? value : Number(String(value).trim())
  if (!Number.isFinite(n)) return DEFAULT_CARPOOL_EVICT_IDLE_HOURS
  return Math.min(720, Math.max(0, Math.trunc(n)))
}
