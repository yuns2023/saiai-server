import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { i18n } from '@/i18n'

const api = vi.hoisted(() => ({
  me: vi.fn(),
  login2FA: vi.fn(),
  completeOAuthRegistration: vi.fn(),
  publicSettings: vi.fn()
}))

vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return { ...actual, authApi: { ...actual.authApi, ...api } }
})

import OAuthCallbackView from '../OAuthCallbackView.vue'

const user = { id: 7, email: 'google-1@google-connect.invalid', username: 'Alice', role: 'user', balance: 0, concurrency: 1, status: 'active' }
const pending = 'b'.repeat(64)

async function mountAt(provider: string, hash: string) {
  window.history.replaceState(null, '', `/auth/oauth/${provider}/callback${hash}`)
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/auth/oauth/:provider/callback', component: OAuthCallbackView },
      { path: '/dashboard', component: { template: '<div>dash</div>' } },
      { path: '/keys', component: { template: '<div>keys</div>' } },
      { path: '/login', component: { template: '<div>login</div>' } }
    ]
  })
  await router.push(`/auth/oauth/${provider}/callback`)
  await router.isReady()
  const wrapper = mount(OAuthCallbackView, { global: { plugins: [router, i18n] } })
  await flushPromises()
  return { wrapper, router }
}

beforeEach(() => {
  setActivePinia(createPinia())
  localStorage.clear()
  i18n.global.locale.value = 'zh'
  Object.values(api).forEach((fn) => fn.mockReset())
})

describe('OAuthCallbackView', () => {
  it('signs in with handed-over tokens, clears the fragment and follows a safe redirect', async () => {
    api.me.mockResolvedValue(user)
    const { router } = await mountAt('google', '#access_token=at-1&refresh_token=rt-1&expires_in=900&token_type=Bearer&redirect=%2Fkeys')
    expect(window.location.hash).toBe('')
    expect(localStorage.getItem('auth_token')).toBe('at-1')
    expect(localStorage.getItem('refresh_token')).toBe('rt-1')
    expect(JSON.parse(localStorage.getItem('auth_user') || '{}').id).toBe(7)
    expect(router.currentRoute.value.path).toBe('/keys')
  })

  it('ignores an off-site redirect', async () => {
    api.me.mockResolvedValue(user)
    const { router } = await mountAt('github', '#access_token=at&refresh_token=rt&redirect=%2F%2Fevil.example')
    expect(router.currentRoute.value.path).toBe('/dashboard')
  })

  it('asks for the TOTP code before issuing a session', async () => {
    api.login2FA.mockResolvedValue({ access_token: 'at-2', refresh_token: 'rt-2', expires_in: 900, token_type: 'Bearer', user })
    const { wrapper, router } = await mountAt('github', '#requires_2fa=1&temp_token=tmp-1&redirect=%2Fkeys')
    expect(window.location.hash).toBe('')
    expect(localStorage.getItem('auth_token')).toBeNull()
    await wrapper.find('input[autocomplete="one-time-code"]').setValue('123456')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(api.login2FA).toHaveBeenCalledWith('tmp-1', '123456')
    expect(localStorage.getItem('auth_token')).toBe('at-2')
    expect(router.currentRoute.value.path).toBe('/keys')
  })

  it('collects the invitation code for a pending registration', async () => {
    api.completeOAuthRegistration
      .mockRejectedValueOnce({ status: 400, code: 400, reason: 'INVITATION_CODE_INVALID', message: '邀请码无效' })
      .mockResolvedValueOnce({ access_token: 'at-3', refresh_token: 'rt-3', expires_in: 900, token_type: 'Bearer', user })
    const { wrapper, router } = await mountAt('google', `#error=invitation_required&pending_oauth_token=${pending}&redirect=%2Fdashboard`)
    expect(window.location.hash).toBe('')

    await wrapper.find('input[autocomplete="off"]').setValue('WRONG')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(wrapper.find('[role=alert]').text()).toContain('邀请码无效')
    expect(localStorage.getItem('auth_token')).toBeNull()

    await wrapper.find('input[autocomplete="off"]').setValue('INVITE-OK')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(api.completeOAuthRegistration).toHaveBeenLastCalledWith('google', pending, 'INVITE-OK')
    expect(localStorage.getItem('auth_token')).toBe('at-3')
    expect(router.currentRoute.value.path).toBe('/dashboard')
  })

  it('ends the flow when the one-time session is gone', async () => {
    api.completeOAuthRegistration.mockRejectedValueOnce({ status: 400, code: 400, reason: 'OAUTH_PENDING_INVALID', message: 'x' })
    const { wrapper } = await mountAt('github', `#error=invitation_required&pending_oauth_token=${pending}`)
    await wrapper.find('input[autocomplete="off"]').setValue('INVITE-OK')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.find('[role=alert]').text()).toContain('重新发起 GitHub 登录')
  })

  it('explains errors from the backend', async () => {
    const { wrapper } = await mountAt('github', '#error=email_not_verified&error_message=OAUTH_EMAIL_NOT_VERIFIED')
    expect(wrapper.find('[role=alert]').text()).toContain('GitHub 账号没有已验证的主邮箱')
    expect(localStorage.getItem('auth_token')).toBeNull()

    const disabled = await mountAt('google', '#error=login_failed&error_message=USER_NOT_ACTIVE')
    expect(disabled.wrapper.find('[role=alert]').text()).toContain('该账号已停用')
  })

  it('treats a reload without a fragment as an error, not a sign-in', async () => {
    const { wrapper } = await mountAt('google', '')
    expect(wrapper.find('[role=alert]').exists()).toBe(true)
    expect(api.me).not.toHaveBeenCalled()
  })
})
