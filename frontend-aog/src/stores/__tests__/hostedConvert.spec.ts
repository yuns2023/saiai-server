import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { pendingConvertKey, readPendingConvert, useHostedStore } from '../hosted'

const USER = 7
const KEY = pendingConvertKey(USER)
const A = { requestId: '11111111-1111-4111-8111-111111111111', amount: '3' }
const B = { requestId: '22222222-2222-4222-8222-222222222222', amount: '1' }

/** A serializing Web Locks stand-in: one holder at a time per name, in request order. */
function installLocks() {
  const chains = new Map<string, Promise<unknown>>()
  const held: string[] = []
  const request = vi.fn((name: string, fn: () => unknown) => {
    const prev = chains.get(name) ?? Promise.resolve()
    const run = prev.then(async () => {
      held.push(name)
      try {
        return await fn()
      } finally {
        held.splice(held.indexOf(name), 1)
      }
    })
    chains.set(name, run.catch(() => undefined))
    return run
  })
  Object.defineProperty(navigator, 'locks', { value: { request }, configurable: true })
  return { request, held }
}

/** One browser tab: its own Pinia store, the shared localStorage. */
function tab() {
  setActivePinia(createPinia())
  const store = useHostedStore()
  store.loadPendingConvert(USER)
  return store
}

describe('hosted store: unsettled conversion record', () => {
  beforeEach(() => localStorage.clear())
  afterEach(() => {
    Reflect.deleteProperty(navigator, 'locks')
    vi.restoreAllMocks()
  })

  it('reads absent / valid / unreadable', () => {
    expect(readPendingConvert(USER)).toEqual({ state: 'absent' })
    localStorage.setItem(KEY, JSON.stringify(A))
    expect(readPendingConvert(USER)).toEqual({ state: 'valid', value: A })
    for (const bad of ['{not json', '{"requestId":"x","amount":"1"}', '{"requestId":"' + A.requestId + '","amount":"1.234"}', 'null', '""']) {
      localStorage.setItem(KEY, bad)
      expect(readPendingConvert(USER)).toEqual({ state: 'unreadable' })
    }
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('denied')
    })
    expect(readPendingConvert(USER)).toEqual({ state: 'unreadable' })
  })

  it('two tabs starting at the same time: the lock lets exactly one start', async () => {
    const { request } = installLocks()
    const tabA = tab()
    const tabB = tab()
    const [ra, rb] = await Promise.all([tabA.beginConvert(A), tabB.beginConvert(B)])
    expect([ra, rb].sort()).toEqual(['pending', 'started'])
    expect(request).toHaveBeenCalledWith(`ccyou-market-convert-${USER}`, expect.any(Function))
    const stored = readPendingConvert(USER)
    expect(stored.state).toBe('valid')
    // Both tabs now show the one that started.
    tabA.syncPendingConvert()
    tabB.syncPendingConvert()
    expect(tabA.pendingConvert).toEqual(tabB.pendingConvert)
  })

  it('compare-and-delete under the lock never removes a record written by another tab in between', async () => {
    installLocks()
    const tabA = tab()
    expect(await tabA.beginConvert(A)).toBe('started')
    const tabB = tab()
    // A settles while B, queued first, replaces the record (e.g. after A's earlier settle elsewhere).
    localStorage.removeItem(KEY)
    const bStart = tabB.beginConvert(B)
    const aSettle = tabA.settleConvert(A.requestId)
    expect(await bStart).toBe('started')
    await aSettle
    expect(readPendingConvert(USER)).toEqual({ state: 'valid', value: B })
    await tabB.settleConvert(B.requestId)
    expect(readPendingConvert(USER)).toEqual({ state: 'absent' })
  })

  it('an unreadable record blocks new conversions and is never overwritten; only the explicit clear removes it', async () => {
    installLocks()
    localStorage.setItem(KEY, '{broken')
    const t = tab()
    expect(t.pendingUnreadable).toBe(true)
    expect(await t.beginConvert(A)).toBe('unreadable')
    expect(localStorage.getItem(KEY)).toBe('{broken')
    await t.settleConvert(A.requestId)
    expect(localStorage.getItem(KEY)).toBe('{broken')
    await t.clearUnreadableConvert()
    expect(localStorage.getItem(KEY)).toBeNull()
    expect(t.pendingUnreadable).toBe(false)
    expect(await t.beginConvert(A)).toBe('started')
  })

  it('without navigator.locks the same steps still run (accepted limitation for old browsers)', async () => {
    const t = tab()
    expect(await t.beginConvert(A)).toBe('started')
    expect(await t.beginConvert(B)).toBe('pending')
  })
})
