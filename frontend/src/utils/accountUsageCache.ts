import type { AccountUsageInfo } from '@/types'

/**
 * Per-account cache + in-flight dedupe for the accounts page usage cells.
 *
 * The table is virtualized and shows a skeleton while the list reloads, so a
 * cell is unmounted and remounted many times (refresh, scroll, row height
 * measurement). Without a cache every mount re-requested /usage: one page
 * refresh produced 2–5 requests per visible account and tripped the edge
 * per-IP rate limit. A remount now reuses the last result unless the user
 * refreshed since it was fetched.
 */
interface CacheEntry {
  data: AccountUsageInfo
  // manualRefreshToken of the page when this result was requested.
  refreshToken: number
  fetchedAt: number
}

// A remounted cell (scrolled back into view) refetches after this long.
export const ACCOUNT_USAGE_CACHE_TTL_MS = 60_000

const cache = new Map<number, CacheEntry>()
const inflight = new Map<string, Promise<AccountUsageInfo>>()

export function getCachedAccountUsage(accountId: number, refreshToken: number): AccountUsageInfo | null {
  const entry = cache.get(accountId)
  if (!entry || entry.refreshToken !== refreshToken) return null
  if (Date.now() - entry.fetchedAt > ACCOUNT_USAGE_CACHE_TTL_MS) return null
  return entry.data
}

/**
 * Fetches usage once per (account, source) at a time; concurrent callers share
 * the same request. The result becomes the account's cached value.
 */
export function fetchAccountUsageShared(
  accountId: number,
  source: 'passive' | 'active' | undefined,
  refreshToken: number,
  fetcher: () => Promise<AccountUsageInfo>
): Promise<AccountUsageInfo> {
  const key = `${accountId}:${source ?? 'default'}:${refreshToken}`
  const pending = inflight.get(key)
  if (pending) return pending

  const request = fetcher()
    .then((data) => {
      cache.set(accountId, { data, refreshToken, fetchedAt: Date.now() })
      return data
    })
    .finally(() => {
      inflight.delete(key)
    })
  inflight.set(key, request)
  return request
}

export function clearAccountUsageCache(): void {
  cache.clear()
  inflight.clear()
}
