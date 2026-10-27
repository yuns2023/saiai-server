export interface UserIdentity {
  username: string
  notes: string
}

// Notes are edited rarely; a short TTL keeps hover tooltips cheap without going stale for long.
const TTL_MS = 5 * 60 * 1000
const cache = new Map<number, { at: number; value: Promise<UserIdentity> }>()

export function loadUserIdentity(userId: number): Promise<UserIdentity> {
  const hit = cache.get(userId)
  if (hit && Date.now() - hit.at < TTL_MS) return hit.value
  // Loaded on first hover so pages embedding the tooltip do not pull the API client in at import time.
  const value = import('@/api/admin/users').then(({ getById }) => getById(userId)).then((u) => ({ username: u.username ?? '', notes: u.notes ?? '' }))
  const entry = { at: Date.now(), value }
  // Drop failures so the next hover retries.
  value.catch(() => {
    if (cache.get(userId) === entry) cache.delete(userId)
  })
  cache.set(userId, entry)
  return value
}

export function resetUserIdentityCache() {
  cache.clear()
}
