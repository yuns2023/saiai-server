// Mirrors Account.GetCarpoolEvictIdleHours on the server: only finite numbers
// and non-blank numeric strings count; anything else (absent, null, blank,
// booleans, objects) means 24 hours. An explicit 0 lifts the restriction and
// values clamp to 0–720.
export const DEFAULT_CARPOOL_EVICT_IDLE_HOURS = 24

export function normalizeCarpoolEvictIdleHours(value: unknown): number {
  let n: number
  if (typeof value === 'number') {
    n = value
  } else if (typeof value === 'string' && value.trim() !== '') {
    n = Number(value.trim())
  } else {
    return DEFAULT_CARPOOL_EVICT_IDLE_HOURS
  }
  if (!Number.isFinite(n)) return DEFAULT_CARPOOL_EVICT_IDLE_HOURS
  return Math.min(720, Math.max(0, Math.trunc(n)))
}
