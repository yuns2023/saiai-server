import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import axios, { type AxiosAdapter, type InternalAxiosRequestConfig } from 'axios'
import { api } from '../client'
import { SESSION_KEYS } from '../session'

/** Session of user `id` with access token `token` and refresh token `refresh`. */
function signIn(id: number, token: string, refresh: string): void {
  localStorage.setItem(SESSION_KEYS.token, token)
  localStorage.setItem(SESSION_KEYS.refresh, refresh)
  localStorage.setItem(SESSION_KEYS.user, JSON.stringify({ id, email: `u${id}@x` }))
}

const ok = (config: InternalAxiosRequestConfig, data: unknown) => ({ data: { code: 0, data }, status: 200, statusText: 'OK', headers: {}, config })
function unauthorized(config: InternalAxiosRequestConfig): Promise<never> {
  const err = Object.assign(new Error('401'), { config, response: { status: 401, data: { code: 'UNAUTHORIZED' }, headers: {}, config } })
  return Promise.reject(err)
}

describe('401 handling is bound to the sending session', () => {
  const original = api.defaults.adapter
  const seen: string[] = []
  beforeEach(() => {
    localStorage.clear()
    seen.length = 0
    vi.spyOn(window, 'location', 'get').mockReturnValue({ ...window.location, pathname: '/', search: '', href: '' } as Location)
  })
  afterEach(() => {
    api.defaults.adapter = original
    vi.restoreAllMocks()
  })

  it('refreshes and replays for the same session', async () => {
    signIn(1, 'a1', 'r1')
    vi.spyOn(axios, 'post').mockResolvedValue({ data: { code: 0, data: { access_token: 'a2', refresh_token: 'r2', expires_in: 60 } } })
    api.defaults.adapter = (async (config) => {
      seen.push(String(config.headers.Authorization))
      return config.headers.Authorization === 'Bearer a2' ? ok(config, 'done') : unauthorized(config)
    }) as AxiosAdapter
    const res = await api.post('/keys', {})
    expect(res.data).toBe('done')
    expect(seen).toEqual(['Bearer a1', 'Bearer a2'])
  })

  it('never replays an old account’s request with a new account’s token', async () => {
    signIn(1, 'a1', 'r1')
    const refresh = vi.spyOn(axios, 'post')
    api.defaults.adapter = (async (config) => {
      seen.push(String(config.headers.Authorization))
      signIn(2, 'b1', 'rb1') // user switched accounts before this 401 came back
      return unauthorized(config)
    }) as AxiosAdapter
    await expect(api.post('/redeem', { code: 'x' })).rejects.toMatchObject({ code: 'SESSION_EXPIRED' })
    expect(seen).toEqual(['Bearer a1'])
    expect(refresh).not.toHaveBeenCalled()
    expect(localStorage.getItem(SESSION_KEYS.token)).toBe('b1') // B's session untouched
  })

  it('drops a replay if another account signs in after the refresh', async () => {
    signIn(1, 'a1', 'r1')
    // A's refresh succeeds…
    vi.spyOn(axios, 'post').mockResolvedValue({ data: { code: 0, data: { access_token: 'a2', refresh_token: 'r2', expires_in: 60 } } })
    // …and B signs in exactly when the replay is about to be sent. Interceptors
    // registered later run first, so this one runs before the client's guard.
    const hook = api.interceptors.request.use((config) => {
      if ((config as { _retry?: boolean })._retry) signIn(2, 'b1', 'rb1')
      return config
    })
    api.defaults.adapter = (async (config) => {
      seen.push(String(config.headers.Authorization))
      return seen.length === 1 ? unauthorized(config) : ok(config, 'should-not-happen')
    }) as AxiosAdapter
    try {
      await expect(api.post('/keys', { name: 'x' })).rejects.toMatchObject({ code: 'SESSION_EXPIRED' })
    } finally {
      api.interceptors.request.eject(hook)
    }
    expect(seen).toEqual(['Bearer a1']) // the replay never left the browser
    expect(localStorage.getItem(SESSION_KEYS.token)).toBe('b1')
  })

  it('replays once with a token the same account rotated elsewhere', async () => {
    signIn(1, 'a1', 'r1')
    api.defaults.adapter = (async (config) => {
      seen.push(String(config.headers.Authorization))
      if (config.headers.Authorization === 'Bearer a1') {
        signIn(1, 'a2', 'r2') // another tab refreshed the same account
        return unauthorized(config)
      }
      return ok(config, 'done')
    }) as AxiosAdapter
    const res = await api.get('/keys')
    expect(res.data).toBe('done')
    expect(seen).toEqual(['Bearer a1', 'Bearer a2'])
  })
})
