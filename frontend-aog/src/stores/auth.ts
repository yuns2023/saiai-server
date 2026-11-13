import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { authApi, needs2FA, type LoginResult } from '@/api'
import { refreshAccessToken, SESSION_EXPIRED_EVENT } from '@/api/client'
import { clearSession, getExpiresAt, getRefreshToken, getToken, readUser, saveTokens, saveUser } from '@/api/session'
import type { AuthResponse, RegisterRequest, User } from '@/types'
import { useNoticesStore } from './notices'

const USER_REFRESH_MS = 60_000
const TOKEN_REFRESH_BUFFER_MS = 120_000

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const token = ref<string | null>(null)
  /** Bumped on every sign-in and sign-out; late responses from an older session are dropped. */
  let generation = 0
  let userTimer: ReturnType<typeof setInterval> | null = null
  let tokenTimer: ReturnType<typeof setTimeout> | null = null

  const isAuthenticated = computed(() => !!token.value && !!user.value)
  const isAdmin = computed(() => user.value?.role === 'admin')

  function stopTimers(): void {
    if (userTimer) clearInterval(userTimer)
    if (tokenTimer) clearTimeout(tokenTimer)
    userTimer = null
    tokenTimer = null
  }

  function startTimers(): void {
    stopTimers()
    userTimer = setInterval(() => {
      if (token.value) refreshUser().catch(() => undefined)
    }, USER_REFRESH_MS)
    scheduleTokenRefresh()
  }

  function scheduleTokenRefresh(): void {
    const expiresAt = getExpiresAt()
    if (!getRefreshToken() || expiresAt === null) return
    const wait = Math.max(0, expiresAt - Date.now() - TOKEN_REFRESH_BUFFER_MS)
    const issuedIn = generation
    tokenTimer = setTimeout(async () => {
      try {
        const fresh = await refreshAccessToken()
        if (issuedIn !== generation) return // signed out or switched while refreshing
        token.value = fresh
        scheduleTokenRefresh()
      } catch {
        // The response interceptor signs out on the next 401.
      }
    }, wait)
  }

  function dropLocalState(): void {
    generation++
    useNoticesStore().reset()
    stopTimers()
    token.value = null
    user.value = null
  }
  window.addEventListener(SESSION_EXPIRED_EVENT, dropLocalState)

  /** Restore a session saved by this app or by the classic console. */
  function restore(): void {
    const saved = getToken()
    const savedUser = readUser<User>()
    if (!saved || !savedUser) return
    token.value = saved
    user.value = savedUser
    refreshUser().catch(() => undefined)
    startTimers()
  }

  function adopt(response: AuthResponse): void {
    generation++
    useNoticesStore().reset()
    saveTokens(response.access_token, response.refresh_token, response.expires_in)
    const { run_mode: _runMode, ...rest } = response.user
    token.value = response.access_token
    user.value = rest
    saveUser(rest)
    startTimers()
  }

  async function login(email: string, password: string, turnstileToken?: string): Promise<LoginResult> {
    const result = await authApi.login(email, password, turnstileToken)
    if (!needs2FA(result)) adopt(result)
    return result
  }

  async function login2FA(tempToken: string, code: string): Promise<void> {
    adopt(await authApi.login2FA(tempToken, code))
  }

  async function register(req: RegisterRequest): Promise<void> {
    adopt(await authApi.register(req))
  }

  async function refreshUser(): Promise<User> {
    const issuedIn = generation
    const me = await authApi.me()
    const { run_mode: _runMode, ...rest } = me
    // Signed out or switched account meanwhile: keep the newer state.
    if (issuedIn !== generation || !token.value) return rest
    user.value = rest
    saveUser(rest)
    return rest
  }

  async function logout(): Promise<void> {
    await authApi.logout(getRefreshToken())
    clearSession()
    dropLocalState()
  }

  return { user, token, isAuthenticated, isAdmin, restore, login, login2FA, register, refreshUser, logout }
})
