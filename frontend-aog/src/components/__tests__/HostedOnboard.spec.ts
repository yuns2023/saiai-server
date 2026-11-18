import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { i18n } from '@/i18n'
import type { HostedApplication, SelfOnboardConfig } from '@/api'

const hosted = vi.hoisted(() => ({ createAuthUrl: vi.fn(), submitApplication: vi.fn() }))
vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return { ...actual, hostedApi: hosted }
})

import HostedOnboard from '../HostedOnboard.vue'

const LINK = { session_id: 's-1', auth_url: 'https://claude.ai/oauth/authorize?x=1', expires_at: '' }
const APP: HostedApplication = {
  id: 9,
  status: 'pending',
  display_name: '我的 Max 号',
  upstream_email: 'me@example.com',
  proxy_display: '',
  reject_reason: '',
  account_id: null,
  created_at: '2026-11-18T01:00:00Z',
  reviewed_at: null
}

function config(over: Partial<SelfOnboardConfig> = {}): SelfOnboardConfig {
  return { enabled: true, exchange_proxy_configured: true, max_pending: 3, ...over }
}

async function mountForm(cfg = config(), pendingCount = 0) {
  setActivePinia(createPinia())
  i18n.global.locale.value = 'zh'
  const w = mount(HostedOnboard, { props: { config: cfg, pendingCount }, global: { plugins: [i18n] } })
  await flushPromises()
  return w
}

async function generate(w: Awaited<ReturnType<typeof mountForm>>) {
  hosted.createAuthUrl.mockResolvedValue({ ...LINK, expires_at: new Date(Date.now() + 10 * 60_000).toISOString() })
  await w.get('[data-testid=onboard-generate]').trigger('click')
  await flushPromises()
}

function code(w: Awaited<ReturnType<typeof mountForm>>): HTMLInputElement {
  return w.get('[data-testid=onboard-code]').element as HTMLInputElement
}

enableAutoUnmount(afterEach)

describe('HostedOnboard', () => {
  beforeEach(() => {
    hosted.createAuthUrl.mockReset()
    hosted.submitApplication.mockReset()
  })

  it('cannot submit before a link exists; the code field is disabled', async () => {
    const w = await mountForm()
    expect(code(w).disabled).toBe(true)
    expect((w.get('[data-testid=onboard-submit]').element as HTMLButtonElement).disabled).toBe(true)
  })

  it('opens the link in a new tab and shows a countdown', async () => {
    const w = await mountForm()
    await generate(w)
    const a = w.get('[data-testid=onboard-link]')
    expect(a.attributes('href')).toBe(LINK.auth_url)
    expect(a.attributes('target')).toBe('_blank')
    expect(a.attributes('rel')).toContain('noopener')
    expect(w.get('[data-testid=onboard-countdown]').text()).toMatch(/^(10:00|9:5\d) 后失效$/)
  })

  it('shows that the link expired once the countdown ends', async () => {
    vi.useFakeTimers()
    try {
      const w = await mountForm()
      hosted.createAuthUrl.mockResolvedValue({ ...LINK, expires_at: new Date(Date.now() + 3000).toISOString() })
      await w.get('[data-testid=onboard-generate]').trigger('click')
      await flushPromises()
      expect(w.find('[data-testid=onboard-countdown]').exists()).toBe(true)
      await w.get('[data-testid=onboard-code]').setValue('abc#xyz')
      await vi.advanceTimersByTimeAsync(4000)
      expect(code(w).value).toBe('')
      expect(w.find('[data-testid=onboard-expired]').exists()).toBe(true)
      expect(code(w).disabled).toBe(true)
    } finally {
      vi.useRealTimers()
    }
  })

  it('submits code, name and proxy, then clears the code and the form', async () => {
    const w = await mountForm()
    await generate(w)
    hosted.submitApplication.mockResolvedValue(APP)
    await w.get('[data-testid=onboard-code]').setValue(' abc#xyz ')
    await w.get('[data-testid=onboard-name]').setValue('我的 Max 号')
    await w.get('[data-testid=onboard-proxy-protocol]').setValue('http')
    await w.get('[data-testid=onboard-proxy-host]').setValue('proxy.example.com')
    await w.get('[data-testid=onboard-proxy-port]').setValue('8080')
    await w.get('[data-testid=onboard-proxy-username]').setValue('u')
    await w.get('[data-testid=onboard-proxy-password]').setValue('p')
    await w.get('form').trigger('submit')
    await flushPromises()

    expect(hosted.submitApplication).toHaveBeenCalledWith({
      session_id: 's-1',
      code: 'abc#xyz',
      display_name: '我的 Max 号',
      proxy: { protocol: 'http', host: 'proxy.example.com', port: 8080, username: 'u', password: 'p' }
    })
    expect(w.emitted('submitted')?.[0]).toEqual([APP])
    expect(code(w).value).toBe('')
    expect((w.get('[data-testid=onboard-name]').element as HTMLInputElement).value).toBe('')
    expect((w.get('[data-testid=onboard-proxy-password]').element as HTMLInputElement).value).toBe('')
    expect(w.find('[data-testid=onboard-link]').exists()).toBe(false)
    expect(w.get('[data-testid=onboard-done]').text()).toContain('等待管理员审核')
  })

  it('leaves the proxy out when it is optional and blank', async () => {
    const w = await mountForm()
    await generate(w)
    hosted.submitApplication.mockResolvedValue(APP)
    await w.get('[data-testid=onboard-code]').setValue('abc#xyz')
    await w.get('[data-testid=onboard-name]').setValue('n')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(hosted.submitApplication.mock.calls[0][0]).toEqual({ session_id: 's-1', code: 'abc#xyz', display_name: 'n', proxy: undefined })
  })

  it('requires a proxy when the site has no exchange proxy', async () => {
    const w = await mountForm(config({ exchange_proxy_configured: false }))
    expect(w.get('[data-testid=onboard-proxy]').text()).toContain('代理（必填）')
    expect(w.get('[data-testid=onboard-proxy-why]').text()).toContain('必须填写你自己的代理')
    await generate(w)
    await w.get('[data-testid=onboard-code]').setValue('abc#xyz')
    await w.get('[data-testid=onboard-name]').setValue('n')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(hosted.submitApplication).not.toHaveBeenCalled()
    expect(w.get('[data-testid=onboard-proxy-error]').text()).toContain('本站需要填写代理')
    // A blocked submission sends nothing, so the code stays for the user to finish the form.
    expect(code(w).value).toBe('abc#xyz')
  })

  it('blocks a half-filled proxy and an invalid port before sending', async () => {
    const w = await mountForm()
    await generate(w)
    await w.get('[data-testid=onboard-code]').setValue('abc#xyz')
    await w.get('[data-testid=onboard-name]').setValue('n')
    await w.get('[data-testid=onboard-proxy-host]').setValue('proxy.example.com')
    await w.get('[data-testid=onboard-proxy-port]').setValue('70000')
    await w.get('[data-testid=onboard-proxy-username]').setValue('u')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(hosted.submitApplication).not.toHaveBeenCalled()
    expect(w.get('[data-testid=onboard-proxy-error]').text()).toContain('1–65535')
    expect(w.get('[data-testid=onboard-auth-hint]').text()).toContain('要么都填，要么都不填')
  })

  it('on SESSION_INVALID asks for a new link, clears the code and keeps name and proxy', async () => {
    const w = await mountForm()
    await generate(w)
    hosted.submitApplication.mockRejectedValue({ status: 400, code: 400, reason: 'SELF_ONBOARD_SESSION_INVALID', message: 'x' })
    await w.get('[data-testid=onboard-code]').setValue('abc#xyz')
    await w.get('[data-testid=onboard-name]').setValue('我的号')
    await w.get('[data-testid=onboard-proxy-host]').setValue('proxy.example.com')
    await w.get('[data-testid=onboard-proxy-port]').setValue('1080')
    await w.get('[data-testid=onboard-proxy-username]').setValue('u')
    await w.get('[data-testid=onboard-proxy-password]').setValue('secret')
    await w.get('form').trigger('submit')
    await flushPromises()

    const err = w.get('[data-testid=onboard-error]').text()
    expect(err).toContain('授权链接已失效或已用过')
    expect(err).toContain('请重新生成授权链接')
    expect(code(w).value).toBe('')
    expect(code(w).disabled).toBe(true)
    expect(w.find('[data-testid=onboard-link]').exists()).toBe(false)
    expect((w.get('[data-testid=onboard-name]').element as HTMLInputElement).value).toBe('我的号')
    expect((w.get('[data-testid=onboard-proxy-host]').element as HTMLInputElement).value).toBe('proxy.example.com')
    // The proxy password is a secret too: it goes with the used link; the username stays.
    expect((w.get('[data-testid=onboard-proxy-username]').element as HTMLInputElement).value).toBe('u')
    expect((w.get('[data-testid=onboard-proxy-password]').element as HTMLInputElement).value).toBe('')
    expect(w.get('[data-testid=onboard-error]').text()).toContain('代理密码需要重新填写')
  })

  it('regenerating drops the old link and its code, and cannot run while a submission is out', async () => {
    const w = await mountForm()
    await generate(w)
    await w.get('[data-testid=onboard-code]').setValue('abc#xyz')
    hosted.createAuthUrl.mockReturnValue(new Promise(() => undefined))
    await w.get('[data-testid=onboard-generate]').trigger('click')
    await flushPromises()
    expect(code(w).value).toBe('')
    expect(w.find('[data-testid=onboard-link]').exists()).toBe(false)
  })

  it('locks the code field and the generate button while submitting', async () => {
    const w = await mountForm()
    await generate(w)
    let finish!: (v: unknown) => void
    hosted.submitApplication.mockReturnValue(new Promise((r) => { finish = r }))
    await w.get('[data-testid=onboard-code]').setValue('abc#xyz')
    await w.get('[data-testid=onboard-name]').setValue('n')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(code(w).disabled).toBe(true)
    expect((w.get('[data-testid=onboard-generate]').element as HTMLButtonElement).disabled).toBe(true)
    finish(APP)
    await flushPromises()
    expect(code(w).value).toBe('')
  })

  it('on EXCHANGE_FAILED also needs a new link', async () => {
    const w = await mountForm()
    await generate(w)
    hosted.submitApplication.mockRejectedValue({ status: 400, reason: 'SELF_ONBOARD_EXCHANGE_FAILED', metadata: { correlation_id: '9f2c4e1ab03d7781' } })
    await w.get('[data-testid=onboard-code]').setValue('abc#xyz')
    await w.get('[data-testid=onboard-name]').setValue('n')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(w.get('[data-testid=onboard-error]').text()).toContain('授权失败，请重新生成链接')
    expect(w.get('[data-testid=onboard-correlation]').text()).toContain('9f2c4e1ab03d7781')
    expect(w.find('[data-testid=onboard-link]').exists()).toBe(false)
    expect(code(w).value).toBe('')
  })

  it('on PROXY_INVALID keeps the link but still clears the code', async () => {
    const w = await mountForm()
    await generate(w)
    hosted.submitApplication.mockRejectedValue({ status: 400, reason: 'SELF_ONBOARD_PROXY_INVALID' })
    await w.get('[data-testid=onboard-code]').setValue('abc#xyz')
    await w.get('[data-testid=onboard-name]').setValue('n')
    await w.get('[data-testid=onboard-proxy-host]').setValue('10.0.0.1')
    await w.get('[data-testid=onboard-proxy-port]').setValue('1080')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(w.get('[data-testid=onboard-error]').text()).toContain('只接受公网地址')
    expect(w.get('[data-testid=onboard-error]').text()).not.toContain('请重新生成授权链接')
    expect(w.find('[data-testid=onboard-link]').exists()).toBe(true)
    expect(code(w).value).toBe('')
  })

  it('on PROXY_UNAVAILABLE keeps the link and the proxy password (raised before the link is used)', async () => {
    const w = await mountForm()
    await generate(w)
    hosted.submitApplication.mockRejectedValue({ status: 400, reason: 'SELF_ONBOARD_PROXY_UNAVAILABLE' })
    await w.get('[data-testid=onboard-code]').setValue('abc#xyz')
    await w.get('[data-testid=onboard-name]').setValue('n')
    await w.get('[data-testid=onboard-proxy-host]').setValue('proxy.example.com')
    await w.get('[data-testid=onboard-proxy-port]').setValue('1080')
    await w.get('[data-testid=onboard-proxy-username]').setValue('u')
    await w.get('[data-testid=onboard-proxy-password]').setValue('secret')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(w.find('[data-testid=onboard-link]').exists()).toBe(true)
    expect((w.get('[data-testid=onboard-proxy-password]').element as HTMLInputElement).value).toBe('secret')
    expect(w.get('[data-testid=onboard-error]').text()).not.toContain('请重新生成授权链接')
    expect(code(w).value).toBe('')
  })

  it('on PENDING_LIMIT treats the link as used up', async () => {
    const w = await mountForm()
    await generate(w)
    hosted.submitApplication.mockRejectedValue({ status: 409, reason: 'SELF_ONBOARD_PENDING_LIMIT' })
    await w.get('[data-testid=onboard-code]').setValue('abc#xyz')
    await w.get('[data-testid=onboard-name]').setValue('n')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(w.find('[data-testid=onboard-link]').exists()).toBe(false)
    expect(w.get('[data-testid=onboard-error]').text()).toContain('请重新生成授权链接')
    expect(code(w).value).toBe('')
  })

  it('at max_pending says so and cannot generate a link', async () => {
    const w = await mountForm(config({ max_pending: 3 }), 3)
    expect(w.get('[data-testid=onboard-pending-full]').text()).toContain('你已有 3 个待审核的申请')
    expect((w.get('[data-testid=onboard-generate]').element as HTMLButtonElement).disabled).toBe(true)
    await w.get('[data-testid=onboard-generate]').trigger('click')
    expect(hosted.createAuthUrl).not.toHaveBeenCalled()
    await w.setProps({ pendingCount: 2 })
    expect(w.find('[data-testid=onboard-pending-full]').exists()).toBe(false)
    expect((w.get('[data-testid=onboard-generate]').element as HTMLButtonElement).disabled).toBe(false)
  })

  it('shows the pending limit with the configured number and ALREADY_HOSTED in plain words', async () => {
    const w = await mountForm(config({ max_pending: 3 }))
    await generate(w)
    hosted.submitApplication.mockRejectedValueOnce({ status: 429, reason: 'SELF_ONBOARD_PENDING_LIMIT' })
    await w.get('[data-testid=onboard-code]').setValue('abc#xyz')
    await w.get('[data-testid=onboard-name]').setValue('n')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(w.get('[data-testid=onboard-error]').text()).toContain('你已有 3 个待审核的申请')

    hosted.submitApplication.mockRejectedValueOnce({ status: 409, reason: 'SELF_ONBOARD_ALREADY_HOSTED' })
    // The pending-limit failure used up the link: generate a new one first.
    await generate(w)
    await w.get('[data-testid=onboard-code]').setValue('abc#xyz')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(w.get('[data-testid=onboard-error]').text()).toContain('该 Claude 号已在托管中或审核中，请联系管理员')
  })

  it('never shows the server message text for an unknown failure', async () => {
    const w = await mountForm()
    await generate(w)
    hosted.submitApplication.mockRejectedValue({ status: 500, message: 'dial tcp 10.1.2.3:1080: refused' })
    await w.get('[data-testid=onboard-code]').setValue('abc#xyz')
    await w.get('[data-testid=onboard-name]').setValue('n')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(w.get('[data-testid=onboard-error]').text()).not.toContain('10.1.2.3')
    expect(w.get('[data-testid=onboard-error]').text()).toContain('操作失败')
  })

  it('rejects a malformed code without sending it', async () => {
    const w = await mountForm()
    await generate(w)
    await w.get('[data-testid=onboard-code]').setValue('just-a-code')
    await w.get('[data-testid=onboard-name]').setValue('n')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(hosted.submitApplication).not.toHaveBeenCalled()
    expect(w.text()).toContain('授权码格式不对')
  })
})
