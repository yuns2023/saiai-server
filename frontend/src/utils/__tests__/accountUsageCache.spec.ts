import { beforeEach, describe, expect, it } from 'vitest'
import type { AccountUsageInfo } from '@/types'
import {
  clearAccountUsageCache,
  fetchAccountUsageShared,
  getCachedAccountUsage,
  invalidateAccountUsage,
  markAccountUsageQueried,
  wasAccountUsageQueried
} from '../accountUsageCache'

const usage = (n: number) => ({ five_hour: { utilization: n } }) as unknown as AccountUsageInfo

describe('accountUsageCache', () => {
  beforeEach(() => clearAccountUsageCache())

  it('reuses a result only for the same refresh token and version key', async () => {
    await fetchAccountUsageShared(1, 'passive', 0, 'anthropic:oauth:', async () => usage(1))
    expect(getCachedAccountUsage(1, 0, 'anthropic:oauth:')).toEqual(usage(1))
    expect(getCachedAccountUsage(1, 1, 'anthropic:oauth:')).toBeNull()
    expect(getCachedAccountUsage(1, 0, 'anthropic:setup-token:')).toBeNull()
    invalidateAccountUsage(1)
    expect(getCachedAccountUsage(1, 0, 'anthropic:oauth:')).toBeNull()
  })

  it('a request started before a clear neither fills the cache nor drops the new in-flight request', async () => {
    let resolveOld!: (v: AccountUsageInfo) => void
    const old = fetchAccountUsageShared(2, 'passive', 0, 'v', () => new Promise((r) => { resolveOld = r }))
    clearAccountUsageCache()

    let calls = 0
    let resolveNew!: (v: AccountUsageInfo) => void
    const fresh = fetchAccountUsageShared(2, 'passive', 0, 'v', () => { calls++; return new Promise((r) => { resolveNew = r }) })
    resolveOld(usage(1))
    await old
    expect(getCachedAccountUsage(2, 0, 'v')).toBeNull()

    // still shares the new request
    const again = fetchAccountUsageShared(2, 'passive', 0, 'v', () => { calls++; return Promise.resolve(usage(9)) })
    expect(again).toBe(fresh)
    expect(calls).toBe(1)
    resolveNew(usage(2))
    await fresh
    expect(getCachedAccountUsage(2, 0, 'v')).toEqual(usage(2))
  })

  it('a request in flight during an edit is neither cached nor shared afterwards', async () => {
    let resolveOld!: (v: AccountUsageInfo) => void
    const old = fetchAccountUsageShared(4, 'passive', 0, 'v', () => new Promise((r) => { resolveOld = r }))
    invalidateAccountUsage(4)
    let calls = 0
    const fresh = fetchAccountUsageShared(4, 'passive', 0, 'v', async () => { calls++; return usage(2) })
    expect(fresh).not.toBe(old)
    expect(calls).toBe(1)
    await fresh
    resolveOld(usage(1))
    await old
    expect(getCachedAccountUsage(4, 0, 'v')).toEqual(usage(2))
  })

  it('remembers queried accounts until the cache is cleared', () => {
    markAccountUsageQueried(5)
    expect(wasAccountUsageQueried(5)).toBe(true)
    expect(wasAccountUsageQueried(6)).toBe(false)
    clearAccountUsageCache()
    expect(wasAccountUsageQueried(5)).toBe(false)
    markAccountUsageQueried(5)
    invalidateAccountUsage(5)
    expect(wasAccountUsageQueried(5)).toBe(false)
  })

  it('a failed request is not cached and frees its in-flight slot', async () => {
    await expect(fetchAccountUsageShared(3, undefined, 0, 'v', () => Promise.reject(new Error('x')))).rejects.toThrow('x')
    expect(getCachedAccountUsage(3, 0, 'v')).toBeNull()
    await expect(fetchAccountUsageShared(3, undefined, 0, 'v', async () => usage(3))).resolves.toEqual(usage(3))
  })
})
