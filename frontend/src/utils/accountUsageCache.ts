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
  // Account shape the result belongs to (platform/type/snapshot); a change
  // means the cached result describes a different account state.
  versionKey: string
  fetchedAt: number
  // The user asked for this result (deferred 查询 / active query), as opposed
  // to an automatic load; only such results are reused for skipped accounts.
  manual: boolean
}

// A remounted cell (scrolled back into view) refetches after this long.
export const ACCOUNT_USAGE_CACHE_TTL_MS = 60_000

const cache = new Map<number, CacheEntry>()
const inflight = new Map<string, Promise<AccountUsageInfo>>()
// Bumped by clearAccountUsageCache(); requests started before a clear must not
// write into (or delete from) the new generation's maps.
let generation = 0
// Per-account epoch bumped by invalidateAccountUsage(); requests started in an
// older epoch (before an edit) must not be cached or shared.
const accountEpochs = new Map<number, number>()
const epochOf = (accountId: number) => accountEpochs.get(accountId) ?? 0

export function getCachedAccountUsage(
  accountId: number,
  refreshToken: number,
  versionKey: string,
  options: { manualOnly?: boolean } = {}
): AccountUsageInfo | null {
  const entry = cache.get(accountId)
  if (!entry || entry.refreshToken !== refreshToken || entry.versionKey !== versionKey) return null
  if (options.manualOnly && !entry.manual) return null
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
  versionKey: string,
  fetcher: () => Promise<AccountUsageInfo>,
  options: { manual?: boolean } = {}
): Promise<AccountUsageInfo> {
  const epoch = epochOf(accountId)
  const key = `${accountId}:${source ?? 'default'}:${refreshToken}:${versionKey}:${epoch}`
  const manual = Boolean(options.manual)
  const pending = inflight.get(key)
  if (pending) {
    if (manual) {
      // A manual query joining an automatic request still marks the result manual.
      return pending.then((data) => {
        const entry = cache.get(accountId)
        if (entry && entry.data === data) entry.manual = true
        return data
      })
    }
    return pending
  }

  const startedIn = generation
  const request = fetcher()
    .then((data) => {
      if (startedIn === generation && epoch === epochOf(accountId)) {
        cache.set(accountId, { data, refreshToken, versionKey, fetchedAt: Date.now(), manual })
      }
      return data
    })
    .finally(() => {
      if (startedIn === generation && inflight.get(key) === request) inflight.delete(key)
    })
  inflight.set(key, request)
  return request
}

/** Drop one account's cached result (after it was edited or re-authorized). */
export function invalidateAccountUsage(accountId: number): void {
  accountEpochs.set(accountId, epochOf(accountId) + 1)
  cache.delete(accountId)
}

export function clearAccountUsageCache(): void {
  generation += 1
  cache.clear()
  inflight.clear()
}
