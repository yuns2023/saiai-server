import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import axios from 'axios'
import { refreshAccessToken } from '../client'
import { SESSION_KEYS } from '../session'

describe('refreshAccessToken', () => {
  beforeEach(() => localStorage.clear())
  afterEach(() => vi.restoreAllMocks())

  it('stores the rotated pair and shares one request between callers', async () => {
    localStorage.setItem(SESSION_KEYS.refresh, 'r1')
    const post = vi.spyOn(axios, 'post').mockResolvedValue({
      data: { code: 0, data: { access_token: 'a2', refresh_token: 'r2', expires_in: 60 } }
    })
    const [x, y] = await Promise.all([refreshAccessToken(), refreshAccessToken()])
    expect(x).toBe('a2')
    expect(y).toBe('a2')
    expect(post).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem(SESSION_KEYS.token)).toBe('a2')
    expect(localStorage.getItem(SESSION_KEYS.refresh)).toBe('r2')
    expect(Number(localStorage.getItem(SESSION_KEYS.expiresAt))).toBeGreaterThan(Date.now())
  })

  it('does not write a session back after sign-out during the request', async () => {
    localStorage.setItem(SESSION_KEYS.refresh, 'r1')
    let release!: () => void
    vi.spyOn(axios, 'post').mockImplementation(
      () =>
        new Promise((resolve) => {
          release = () => resolve({ data: { code: 0, data: { access_token: 'a2', refresh_token: 'r2', expires_in: 60 } } })
        })
    )
    const pending = refreshAccessToken()
    localStorage.clear() // user signs out while the refresh is in flight
    release()
    await expect(pending).rejects.toThrow()
    expect(localStorage.getItem(SESSION_KEYS.token)).toBeNull()
    expect(localStorage.getItem(SESSION_KEYS.refresh)).toBeNull()
  })
})
