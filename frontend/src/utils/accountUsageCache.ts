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
// Disabled/error accounts the user queried on this visit; they follow page
// refreshes even after their cell was unmounted and remounted.
const manuallyQueriedAccounts = new Set<number>()

/** Current edit epoch of an account; pass it back to markAccountUsageQueried. */
export function accountUsageEpoch(accountId: number): number {
  return epochOf(accountId)
}

/**
 * Remember a successful manual query. `epoch` is the account's epoch when the
 * request started: a request that outlived an edit does not set the mark.
 */
export function markAccountUsageQueried(accountId: number, epoch = epochOf(accountId)): void {
  if (epoch !== epochOf(accountId)) return
  manuallyQueriedAccounts.add(accountId)
}

export function wasAccountUsageQueried(accountId: number): boolean {
  return manuallyQueriedAccounts.has(accountId)
}

export function getCachedAccountUsage(
  accountId: number,
  refreshToken: number,
  versionKey: string
): AccountUsageInfo | null {
  const entry = cache.get(accountId)
  if (!entry || entry.refreshToken !== refreshToken || entry.versionKey !== versionKey) return null
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
  fetcher: () => Promise<AccountUsageInfo>
): Promise<AccountUsageInfo> {
  const epoch = epochOf(accountId)
  const key = `${accountId}:${source ?? 'default'}:${refreshToken}:${versionKey}:${epoch}`
  const pending = inflight.get(key)
  if (pending) return pending

  const startedIn = generation
  const request = fetcher()
    .then((data) => {
      if (startedIn === generation && epoch === epochOf(accountId)) {
        cache.set(accountId, { data, refreshToken, versionKey, fetchedAt: Date.now() })
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
  // The edited account (maybe now disabled/error) needs a new explicit query.
  manuallyQueriedAccounts.delete(accountId)
}

export function clearAccountUsageCache(): void {
  generation += 1
  cache.clear()
  inflight.clear()
  // Requests from before the clear are already ignored via generation.
  accountEpochs.clear()
  manuallyQueriedAccounts.clear()
}
