import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { i18n } from '@/i18n'
import type { InstallCodeResponse, PublicSettings } from '@/types'

const api = vi.hoisted(() => ({ installCode: vi.fn() }))
vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return { ...actual, keysApi: { ...actual.keysApi, installCode: api.installCode } }
})

import ConnectPanel from '../ConnectPanel.vue'
import { useAppStore } from '@/stores/app'

const KEY = 'sk-test'

function mountPanel(opts: { supported?: boolean; codes?: boolean; keyId?: number } = {}) {
  setActivePinia(createPinia())
  if (opts.codes !== undefined) {
    useAppStore().$patch({
      settings: { api_base_url: 'https://ccyou.dev/v1', aitp_install_code_enabled: opts.codes } as unknown as PublicSettings
    })
  }
  return mount(ConnectPanel, {
    props: { apiKey: KEY, apiKeyId: opts.keyId ?? 5, supported: opts.supported },
    global: { plugins: [i18n] }
  })
}

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

const inTenMinutes = () => new Date(Date.now() + 10 * 60 * 1000).toISOString()
const reply = (code: string): InstallCodeResponse => ({ code, expires_at: inTenMinutes() })

async function pickShell(w: VueWrapper, label: string) {
  const tab = w.findAll('[role=tab]').find((b) => b.text() === label)
  expect(tab, label).toBeTruthy()
  await tab!.trigger('click')
}

const shortCommand = (w: VueWrapper) => w.find('[data-testid=connect-code-command] pre')
const origin = () => window.location.origin

enableAutoUnmount(afterEach)

beforeEach(() => {
  api.installCode.mockReset()
  i18n.global.locale.value = 'zh'
})

afterEach(() => {
  vi.useRealTimers()
})

describe('ConnectPanel', () => {
  it('leads with the AITP command and keeps manual values collapsed', () => {
    const w = mountPanel()
    const install = w.find('[data-testid=connect-install]')
    expect(install.exists()).toBe(true)
    expect(install.find('pre').text()).toContain('https://aitp.lol/setup.')
    expect(install.find('pre').text()).toContain("'sk-test'")
    expect(install.find('a').attributes('href')).toBe('https://github.com/2742zdz/AITP')
    const manual = w.find('[data-testid=connect-manual]')
    expect(manual.attributes('open')).toBeUndefined()
    expect(manual.text()).toContain('ANTHROPIC_AUTH_TOKEN')
    expect(w.text()).not.toMatch(/openai|codex|gemini/i)
  })

  it('shows a neutral note instead of setup steps for a key outside Claude Code groups', () => {
    const w = mountPanel({ supported: false })
    expect(w.find('[data-testid=connect-unsupported]').exists()).toBe(true)
    expect(w.find('pre').exists()).toBe(false)
  })

  it('explains a misconfigured API address and opens the manual section', async () => {
    const w = mountPanel()
    const app = useAppStore()
    app.$patch({ settings: { api_base_url: 'https://[invalid]/" & echo X & rem "' } as never })
    await w.vm.$nextTick()
    expect(w.find('[data-testid=connect-install]').exists()).toBe(false)
    expect(w.find('[data-testid=connect-no-oneline]').exists()).toBe(true)
    expect(w.find('[data-testid=connect-manual]').attributes('open')).toBeDefined()
  })
})

describe('ConnectPanel install codes', () => {
  it('switch off: renders exactly the panel without codes', async () => {
    const on = mountPanel({ codes: false })
    const html = on.html()
    on.unmount()
    // Same markup as with no setting at all (older servers omit the field).
    const absent = mountPanel()
    useAppStore().$patch({ settings: { api_base_url: 'https://ccyou.dev/v1' } as unknown as PublicSettings })
    await absent.vm.$nextTick()
    expect(absent.html()).toBe(html)
    expect(html).not.toContain('connect-code')
    expect(absent.find('[data-testid=connect-install] pre').text()).toBe(
      "curl -fsSL https://aitp.lol/setup.sh | bash -s -- --base-url 'https://ccyou.dev' --api-key 'sk-test'"
    )
    expect(api.installCode).not.toHaveBeenCalled()
  })

  it('switch on: generates a short command per shell and keeps the full command in a collapsed section', async () => {
    api.installCode.mockResolvedValueOnce(reply('Q7M2K9XW4TRB'))
    const w = mountPanel({ codes: true })
    expect(w.find('[data-testid=connect-install]').exists()).toBe(false)
    const full = w.find('[data-testid=connect-full]')
    expect(full.attributes('open')).toBeUndefined()
    expect(full.text()).toContain('完整命令（不使用安装码）')
    expect(full.find('pre').text()).toBe("curl -fsSL https://aitp.lol/setup.sh | bash -s -- --base-url 'https://ccyou.dev' --api-key 'sk-test'")
    expect(w.find('[data-testid=connect-manual]').text()).toContain('ANTHROPIC_AUTH_TOKEN')

    const button = w.find('[data-testid=connect-code-generate]')
    expect(button.text()).toBe('生成安装命令')
    await button.trigger('click')
    await flushPromises()
    expect(api.installCode).toHaveBeenCalledWith(5, 'unix')
    expect(shortCommand(w).text()).toBe(`curl -fsSL -X POST ${origin()}/i/Q7M2K9XW4TRB | bash`)
    expect(shortCommand(w).text()).not.toContain(KEY)
    const block = w.find('[data-testid=connect-code-command]').text()
    expect(block).toContain('10 分钟内有效，只能使用一次；用过或过期后请重新生成')
    expect(block).toContain('之前生成的命令')
    expect(w.find('[data-testid=connect-code-expiry]').text()).toMatch(/^有效至 \d{2}:\d{2}$/)
    expect(w.find('[data-testid=connect-code-generate]').text()).toBe('重新生成')

    // PowerShell runs another script: the unix result is hidden until generated for it.
    await pickShell(w, 'PowerShell')
    expect(shortCommand(w).exists()).toBe(false)
    api.installCode.mockResolvedValueOnce(reply('0123456789AB'))
    await w.find('[data-testid=connect-code-generate]').trigger('click')
    await flushPromises()
    expect(api.installCode).toHaveBeenLastCalledWith(5, 'powershell')
    expect(shortCommand(w).text()).toBe(`irm ${origin()}/i/0123456789AB -Method Post | iex`)

    // CMD runs the same PowerShell script, so it reuses that code.
    await pickShell(w, 'CMD')
    expect(shortCommand(w).text()).toBe(
      `powershell -NoProfile -ExecutionPolicy Bypass -Command "irm ${origin()}/i/0123456789AB -Method Post | iex"`
    )
    expect(api.installCode).toHaveBeenCalledTimes(2)

    // Back on macOS / Linux, its own code is still bound there.
    await pickShell(w, 'macOS / Linux')
    expect(shortCommand(w).text()).toBe(`curl -fsSL -X POST ${origin()}/i/Q7M2K9XW4TRB | bash`)
  })

  it('a late response never overwrites the current selection', async () => {
    const unix = deferred<InstallCodeResponse>()
    const ps = deferred<InstallCodeResponse>()
    api.installCode.mockReturnValueOnce(unix.promise).mockReturnValueOnce(ps.promise)
    const w = mountPanel({ codes: true })

    await w.find('[data-testid=connect-code-generate]').trigger('click')
    await pickShell(w, 'PowerShell')
    await w.find('[data-testid=connect-code-generate]').trigger('click')
    ps.resolve(reply('PPPPPPPPPPPP'))
    await flushPromises()
    unix.resolve(reply('VVVVVVVVVVVV'))
    await flushPromises()
    expect(shortCommand(w).text()).toBe(`irm ${origin()}/i/PPPPPPPPPPPP -Method Post | iex`)

    // Within one selection, only the latest request counts.
    const first = deferred<InstallCodeResponse>()
    const second = deferred<InstallCodeResponse>()
    api.installCode.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    await pickShell(w, 'macOS / Linux')
    expect(shortCommand(w).text()).toBe(`curl -fsSL -X POST ${origin()}/i/VVVVVVVVVVVV | bash`)
    await w.find('[data-testid=connect-code-generate]').trigger('click')
    // The button is busy while a request runs; a second click comes after it settles.
    expect(w.find('[data-testid=connect-code-generate]').attributes('disabled')).toBeDefined()
    first.reject({ status: 0, message: 'network' })
    await flushPromises()
    await w.find('[data-testid=connect-code-generate]').trigger('click')
    second.resolve(reply('SSSSSSSSSSSS'))
    await flushPromises()
    expect(shortCommand(w).text()).toBe(`curl -fsSL -X POST ${origin()}/i/SSSSSSSSSSSS | bash`)
  })

  it('switching to another key hides the result and drops in-flight responses', async () => {
    const pending = deferred<InstallCodeResponse>()
    api.installCode.mockResolvedValueOnce(reply('Q7M2K9XW4TRB')).mockReturnValueOnce(pending.promise)
    const w = mountPanel({ codes: true, keyId: 5 })
    await w.find('[data-testid=connect-code-generate]').trigger('click')
    await flushPromises()
    expect(shortCommand(w).exists()).toBe(true)

    await w.find('[data-testid=connect-code-generate]').trigger('click')
    await w.setProps({ apiKeyId: 6, apiKey: 'sk-other' })
    expect(shortCommand(w).exists()).toBe(false)
    pending.resolve(reply('XYWZXYWZXYWZ'))
    await flushPromises()
    expect(shortCommand(w).exists()).toBe(false)
    expect(w.find('[data-testid=connect-code-generate]').text()).toBe('生成安装命令')
  })

  it('hides an expired command', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date', 'performance'] })
    api.installCode.mockResolvedValueOnce(reply('Q7M2K9XW4TRB'))
    const w = mountPanel({ codes: true })
    await w.find('[data-testid=connect-code-generate]').trigger('click')
    await flushPromises()
    expect(shortCommand(w).exists()).toBe(true)

    vi.advanceTimersByTime(10 * 60 * 1000 + 5000)
    await w.vm.$nextTick()
    expect(shortCommand(w).exists()).toBe(false)
    expect(w.find('[data-testid=connect-code-expired]').text()).toBe('这条安装命令已过期，请重新生成。')
  })

  it('counts validity on the local monotonic clock, whatever the server or wall clock says', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date', 'performance'] })
    // A client clock running ahead makes the server time look past: still shown.
    api.installCode.mockResolvedValueOnce({ code: 'Q7M2K9XW4TRB', expires_at: new Date(Date.now() - 60_000).toISOString() })
    const w = mountPanel({ codes: true })
    await w.find('[data-testid=connect-code-generate]').trigger('click')
    await flushPromises()
    expect(shortCommand(w).exists()).toBe(true)

    // A wall clock set back does not keep the code alive past 10 minutes.
    vi.setSystemTime(Date.now() - 60 * 60 * 1000)
    vi.advanceTimersByTime(10 * 60 * 1000 + 5000)
    await w.vm.$nextTick()
    expect(shortCommand(w).exists()).toBe(false)
  })

  it('a response that arrives after the panel closed changes nothing', async () => {
    const pending = deferred<InstallCodeResponse>()
    api.installCode.mockReturnValueOnce(pending.promise)
    const intervals = vi.spyOn(window, 'setInterval')
    const w = mountPanel({ codes: true })
    await w.find('[data-testid=connect-code-generate]').trigger('click')
    w.unmount()
    pending.resolve(reply('Q7M2K9XW4TRB'))
    await flushPromises()
    expect(intervals).not.toHaveBeenCalled()
    intervals.mockRestore()
  })

  it('never shows a code it does not recognize', async () => {
    api.installCode.mockResolvedValueOnce(reply('Q7M2K9XW4TRB'))
    const w = mountPanel({ codes: true })
    await w.find('[data-testid=connect-code-generate]').trigger('click')
    await flushPromises()
    expect(shortCommand(w).exists()).toBe(true)

    api.installCode.mockResolvedValueOnce({ code: "abc'; rm -rf ~", expires_at: inTenMinutes() })
    await pickShell(w, 'PowerShell')
    await w.find('[data-testid=connect-code-generate]').trigger('click')
    await flushPromises()
    expect(shortCommand(w).exists()).toBe(false)
    expect(w.find('[data-testid=connect-code-error]').exists()).toBe(true)
  })

  it('shows API errors as text and keeps the full command usable', async () => {
    const cases: Array<[string | undefined, string]> = [
      ['AITP_INSTALL_CODE_RATE_LIMITED', '生成太频繁'],
      ['AITP_INSTALL_CODE_DISABLED', '本站已关闭安装码'],
      ['AITP_INSTALL_CODE_BASE_URL_INVALID', 'API 地址未配置或配置有误'],
      ['AITP_INSTALL_CODE_KEY_UNUSABLE', '已停用或过期'],
      [undefined, '生成失败']
    ]
    for (const [reason, text] of cases) {
      api.installCode.mockRejectedValueOnce({ status: 400, reason, message: 'network' })
      const w = mountPanel({ codes: true })
      await w.find('[data-testid=connect-code-generate]').trigger('click')
      await flushPromises()
      expect(w.find('[data-testid=connect-code-error]').text()).toContain(text)
      expect(w.find('[data-testid=connect-full] pre').text()).toContain("--api-key 'sk-test'")
      w.unmount()
    }

    // A failed regenerate keeps the previous, still valid command.
    api.installCode.mockResolvedValueOnce(reply('Q7M2K9XW4TRB')).mockRejectedValueOnce({ status: 429, reason: 'AITP_INSTALL_CODE_RATE_LIMITED', message: 'x' })
    const w = mountPanel({ codes: true })
    await w.find('[data-testid=connect-code-generate]').trigger('click')
    await flushPromises()
    await w.find('[data-testid=connect-code-generate]').trigger('click')
    await flushPromises()
    expect(shortCommand(w).text()).toContain('/i/Q7M2K9XW4TRB')
    expect(w.find('[data-testid=connect-code-error]').text()).toContain('生成太频繁')
  })

  it('falls back to today’s panel where no one-line command is possible', async () => {
    const w = mountPanel({ codes: true })
    useAppStore().$patch({ settings: { api_base_url: 'ftp://nope', aitp_install_code_enabled: true } as unknown as PublicSettings })
    await w.vm.$nextTick()
    expect(w.find('[data-testid=connect-code]').exists()).toBe(false)
    expect(w.find('[data-testid=connect-no-oneline]').exists()).toBe(true)
  })
})
