import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type { OwnedAccount } from '@/api'

const hosted = vi.hoisted(() => ({ listOwnedAccounts: vi.fn(), listOwnedAccountUsage: vi.fn() }))
vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return { ...actual, hostedApi: hosted, announcementsApi: { list: vi.fn().mockResolvedValue([]), markRead: vi.fn() } }
})

import { SESSION_EXPIRED_EVENT } from '@/api/client'
import { useAuthStore } from '../auth'
import { HOSTED_RETRY_MS, HOSTED_TTL_MS, useHostedStore } from '../hosted'

const acct = (id: number) => ({ account_id: id, current: true, display_name: `#${id}`, windows: [] }) as unknown as OwnedAccount

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('hosted store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    hosted.listOwnedAccounts.mockReset()
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-11-16T04:00:00Z'))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  const later = (ms: number) => vi.setSystemTime(Date.now() + ms)

  it('re-checks a good list only after the TTL', async () => {
    hosted.listOwnedAccounts.mockResolvedValueOnce([]).mockResolvedValueOnce([acct(1)])
    const s = useHostedStore()
    s.ensureLoaded()
    await flushPromises()
    later(HOSTED_TTL_MS - 1)
    s.ensureLoaded()
    await flushPromises()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(1)
    later(1)
    s.ensureLoaded()
    await flushPromises()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(2)
    expect(s.hasAny).toBe(true)
  })

  it('retries a failure no sooner than the backoff, then trusts the success for the TTL', async () => {
    hosted.listOwnedAccounts.mockRejectedValueOnce({ status: 500 }).mockResolvedValueOnce([acct(1)])
    const s = useHostedStore()
    s.ensureLoaded()
    await flushPromises()
    expect(s.failed).toBe(true)
    later(HOSTED_RETRY_MS - 1)
    s.ensureLoaded()
    await flushPromises()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(1)
    later(1)
    s.ensureLoaded()
    await flushPromises()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(2)
    expect(s.failed).toBe(false)
    expect(s.hasAny).toBe(true)
    later(HOSTED_RETRY_MS)
    s.ensureLoaded()
    await flushPromises()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(2)
  })

  it('checks again right away after a reset (new session)', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([acct(1)])
    const s = useHostedStore()
    s.ensureLoaded()
    await flushPromises()
    s.reset()
    s.ensureLoaded()
    await flushPromises()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(2)
  })

  it('shares one request between concurrent callers', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([acct(1)])
    const s = useHostedStore()
    s.ensureLoaded()
    await s.load()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(1)
    expect(s.hasAny).toBe(true)
  })

  it('keeps the new session list when the old one lands late (success or failure)', async () => {
    const old = deferred<OwnedAccount[]>()
    const fresh = deferred<OwnedAccount[]>()
    hosted.listOwnedAccounts.mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise)
    const s = useHostedStore()
    s.ensureLoaded()
    s.reset()
    s.ensureLoaded()
    expect(hosted.listOwnedAccounts).toHaveBeenCalledTimes(2)
    old.resolve([acct(1)])
    await flushPromises()
    expect(s.accounts).toEqual([])
    expect(s.loaded).toBe(false)
    fresh.resolve([acct(2)])
    await flushPromises()
    expect(s.accounts.map((a) => a.account_id)).toEqual([2])
    expect(s.loaded).toBe(true)

    // A late failure from an older session does not mark the current one failed.
    const older = deferred<OwnedAccount[]>()
    hosted.listOwnedAccounts.mockReturnValueOnce(older.promise).mockResolvedValueOnce([acct(3)])
    s.reset()
    s.ensureLoaded()
    s.reset()
    s.ensureLoaded()
    older.reject({ status: 500 })
    await flushPromises()
    expect(s.failed).toBe(false)
    expect(s.accounts.map((a) => a.account_id)).toEqual([3])
  })

  it('is cleared when the session expires', async () => {
    hosted.listOwnedAccounts.mockResolvedValue([acct(1)])
    useAuthStore()
    const s = useHostedStore()
    await s.load()
    expect(s.hasAny).toBe(true)
    window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT))
    expect(s.accounts).toEqual([])
    expect(s.loaded).toBe(false)
  })
})
