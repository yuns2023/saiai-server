import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

const { copyToClipboard } = vi.hoisted(() => ({ copyToClipboard: vi.fn() }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard }) }))

import UseKeyModal from '../UseKeyModal.vue'

const mountedModals: VueWrapper[] = []

function mountModal(props: Record<string, unknown>): VueWrapper {
  const wrapper = mount(UseKeyModal, {
    props: {
      show: true,
      apiKey: 'TEST_ONLY_API_KEY',
      baseUrl: 'https://example.com/v1',
      platform: 'anthropic',
      ...props
    },
    global: {
      stubs: {
        BaseDialog: {
          template: '<div><slot /><slot name="footer" /></div>'
        },
        Icon: {
          template: '<span />'
        }
      }
    }
  })
  mountedModals.push(wrapper)
  return wrapper
}

const command = (wrapper: VueWrapper) => wrapper.find('pre code').text()

describe('UseKeyModal', () => {
  beforeEach(() => { copyToClipboard.mockReset().mockResolvedValue(true) })
  afterEach(() => {
    mountedModals.splice(0).forEach((wrapper) => wrapper.unmount())
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it.each(['anthropic', 'openai'])('defaults Windows browsers to PowerShell for %s', async (platform) => {
    vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue('Mozilla/5.0 (Windows NT 10.0; Win64; x64)')
    const wrapper = mountModal({ platform })
    await nextTick()
    expect(command(wrapper)).toContain('setup.ps1')
    expect(command(wrapper)).not.toContain('setup.sh')
    const unixTab = wrapper.findAll('button').find((button) => button.text() === 'macOS / Linux')
    await unixTab!.trigger('click')
    expect(command(wrapper)).toContain('setup.sh')
  })

  it.each(['MacIntel', 'Linux x86_64'])('defaults %s to the terminal command', async (platform) => {
    vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue('Mozilla/5.0')
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue(platform)
    const wrapper = mountModal({})
    await nextTick()
    expect(command(wrapper)).toContain('setup.sh')
  })

  it('resets shell and copy feedback when reopened for another key', async () => {
    vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue('Mozilla/5.0 (Windows NT 10.0)')
    const wrapper = mountModal({})
    await nextTick()
    const unixTab = wrapper.findAll('button').find((button) => button.text() === 'macOS / Linux')
    await unixTab!.trigger('click')
    await wrapper.findAll('button').find((button) => button.text() === 'keys.useKeyModal.copySetup')!.trigger('click')
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true, apiKey: 'TEST_ONLY_NEXT_KEY' })
    expect(command(wrapper)).toContain('setup.ps1')
    expect(command(wrapper)).toContain('TEST_ONLY_NEXT_KEY')
    expect(wrapper.findAll('button').some((button) => button.text() === 'keys.useKeyModal.copied')).toBe(false)
  })

  it('shows creation and execution guidance without treating a copied command as completed setup', async () => {
    const wrapper = mountModal({ newlyCreated: true, platform: 'openai' })
    await nextTick()
    expect(wrapper.text()).toContain('keys.useKeyModal.createdTitle')
    expect(wrapper.text()).toContain('keys.useKeyModal.steps.runHint')
    expect(wrapper.text()).toContain('keys.useKeyModal.launch.codexVscode')
    expect(wrapper.find('[data-tour="key-setup-command"]').exists()).toBe(true)
    await wrapper.findAll('button').find((button) => button.text() === 'keys.useKeyModal.copySetup')!.trigger('click')
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.find('[data-tour="key-setup-command"]').exists()).toBe(true)
  })

  it('renders one repeatable Claude command containing the escaped Gateway and Key', async () => {
    const wrapper = mountModal({})
    await nextTick()

    expect(wrapper.findAll('pre code')).toHaveLength(1)
    expect(command(wrapper)).toBe(
      "curl -fsSL https://example.com/saiai-cli/setup.sh | bash -s -- 'https://example.com' 'TEST_ONLY_API_KEY'"
    )
    expect(wrapper.text()).toContain('keys.useKeyModal.saiaiCliHint')
    expect(wrapper.text()).toContain('keys.useKeyModal.note')
    expect(wrapper.text()).not.toContain('keys.useKeyModal.cliTabs.v2Preview')
  })

  it('randomly selects an enabled API endpoint and lets the user switch it', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0.9)
    const wrapper = mountModal({
      apiEndpoints: [
        { id: 'dmit', name: 'DMIT', url: 'https://dmit.example.com', enabled: true },
        { id: 'vmiss', name: 'VMISS', url: 'https://vmiss.example.com', enabled: true },
        { id: 'disabled', name: 'Disabled', url: 'https://disabled.example.com', enabled: false }
      ]
    })
    await nextTick()

    expect(wrapper.find('select').element.value).toBe('vmiss')
    expect(command(wrapper)).toContain('https://vmiss.example.com')
    expect(wrapper.findAll('option')).toHaveLength(2)

    await wrapper.find('select').setValue('dmit')
    await nextTick()
    expect(command(wrapper)).toContain('https://dmit.example.com')
    vi.restoreAllMocks()
  })

  it('shell-quotes apostrophes in the Key', async () => {
    const wrapper = mountModal({ apiKey: "TEST_ONLY_'_KEY" })
    await nextTick()

    expect(command(wrapper)).toContain("'TEST_ONLY_'\\''_KEY'")
  })

  it('renders a PowerShell one-click command and doubles apostrophes', async () => {
    const wrapper = mountModal({ apiKey: "TEST_ONLY_'_KEY" })
    await nextTick()

    const tab = wrapper.findAll('button').find((button) => button.text().includes('PowerShell'))
    expect(tab).toBeDefined()
    await tab!.trigger('click')
    await nextTick()

    expect(command(wrapper)).toBe(
      "irm https://example.com/saiai-cli/setup.ps1 | iex; Invoke-Saiai 'https://example.com' 'TEST_ONLY_''_KEY'"
    )
  })

  it('renders the CMD form through PowerShell with the Key included', async () => {
    const wrapper = mountModal({})
    await nextTick()

    const tab = wrapper.findAll('button').find((button) => button.text().includes('Windows CMD'))
    expect(tab).toBeDefined()
    await tab!.trigger('click')
    await nextTick()

    expect(command(wrapper)).toBe(
      'powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://example.com/saiai-cli/setup.ps1 | iex; Invoke-Saiai \'https://example.com\' \'TEST_ONLY_API_KEY\'"'
    )
  })

  it('keeps OpenAI on Codex by default and includes the Gateway and Key', async () => {
    const codex = mountModal({ platform: 'openai' })
    await nextTick()
    expect(command(codex)).toContain(
      "init-codex 'https://example.com/v1' 'TEST_ONLY_API_KEY'"
    )
    expect(command(codex)).not.toContain('--websockets')
    expect(codex.findAll('button').map((button) => button.text()).filter((label) =>
      label.startsWith('keys.useKeyModal.cliTabs.')
    )).toEqual(['keys.useKeyModal.cliTabs.codexCli'])
    expect(codex.findAll('button').some((button) => button.text().includes('keys.useKeyModal.cliTabs.claudeCode'))).toBe(false)
  })

  it('adds the Responses /v1 suffix when the selected endpoint is a Gateway root', async () => {
    const codex = mountModal({ platform: 'openai', baseUrl: 'https://example.com' })
    await nextTick()

    expect(command(codex)).toContain(
      "init-codex 'https://example.com/v1' 'TEST_ONLY_API_KEY'"
    )
  })

  it('renders the short PowerShell Codex command with the Gateway and Key', async () => {
    const codex = mountModal({ platform: 'openai' })
    await nextTick()
    const tab = codex.findAll('button').find((button) => button.text().includes('PowerShell'))
    expect(tab).toBeDefined()
    await tab!.trigger('click')
    await nextTick()

    expect(command(codex)).toBe(
      "irm https://example.com/saiai-cli/setup.ps1 | iex; Invoke-Saiai init-codex 'https://example.com/v1' 'TEST_ONLY_API_KEY'"
    )
  })

  it('keeps direct Gemini configuration', async () => {
    const gemini = mountModal({ platform: 'gemini' })
    await nextTick()
    expect(command(gemini)).toContain('GOOGLE_GEMINI_BASE_URL')
    expect(command(gemini)).toContain('TEST_ONLY_API_KEY')
    expect(gemini.find('[data-testid="client-launch"]').exists()).toBe(false)
    expect(gemini.text()).toContain('keys.useKeyModal.steps.geminiStart')
    expect(gemini.find('details').exists()).toBe(false)
  })

  it.each(['antigravity', 'sora'])('does not generate setup instructions for retired %s keys', async (platform) => {
    const wrapper = mountModal({ platform })
    await nextTick()
    expect(wrapper.findAll('pre code')).toHaveLength(0)
  })

  it('does not expose withdrawn V2 setup or launcher commands', async () => {
    for (const platform of ['anthropic', 'openai'] as const) {
      const wrapper = mountModal({ platform })
      await nextTick()
      const output = wrapper.findAll('pre code').map((block) => block.text()).join('\n')
      for (const removed of ['setup claude', 'saiai claude', 'revoke --all', 'V2 Preview']) {
        expect(output).not.toContain(removed)
      }
      expect(output).toContain('TEST_ONLY_API_KEY')
    }
  })

  it('keeps recovery launch separate from setup and normal VSCode guidance', async () => {
    const wrapper = mountModal({})
    await nextTick()
    const launch = wrapper.find('[data-testid="client-launch"]')
    expect(launch.findAll('code').map((element) => element.text())).toEqual(['claude', 'saiai claude'])
    expect(launch.text()).toContain('keys.useKeyModal.launch.claudeVscode')
    expect(launch.text()).toContain('keys.useKeyModal.launch.claudeRecoveryHint')
    expect(launch.text()).not.toContain('saiai codex')
    expect(command(wrapper)).not.toContain('saiai claude')
    expect(copyToClipboard).not.toHaveBeenCalled()
    expect(wrapper.find('[data-tour="key-setup-command"]').element.compareDocumentPosition(launch.element) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
  })

  it('lists Codex terminal, Desktop and VSCode actions without a new setup mode', async () => {
    const wrapper = mountModal({ platform: 'openai' })
    await nextTick()
    const launch = wrapper.find('[data-testid="client-launch"]')
    expect(launch.findAll('code').map((element) => element.text())).toEqual(['saiai codex', 'saiai desktop codex'])
    expect(launch.text()).toContain('keys.useKeyModal.launch.codexVscode')
    expect(launch.text()).toContain('keys.useKeyModal.launch.desktopHint')
    expect(wrapper.text()).not.toContain('saiai claude')
    expect(wrapper.findAll('pre code')).toHaveLength(1)
  })

  it.each([
    { platform: 'anthropic', launchCommand: 'claude' },
    { platform: 'anthropic', launchCommand: 'saiai claude' },
    { platform: 'openai', launchCommand: 'saiai codex' },
    { platform: 'openai', launchCommand: 'saiai desktop codex' }
  ])('copies only $launchCommand without credentials or configuration completion', async ({ platform, launchCommand }) => {
    const wrapper = mountModal({ platform, newlyCreated: true })
    await wrapper.find(`[data-launch-command="${launchCommand}"]`).trigger('click')
    await flushPromises()
    expect(copyToClipboard).toHaveBeenCalledExactlyOnceWith(launchCommand, 'keys.useKeyModal.copied')
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.text()).toContain('keys.useKeyModal.createdTitle')
    expect(wrapper.find(`[data-launch-command="${launchCommand}"]`).attributes('aria-label')).toBe('keys.useKeyModal.launch.copy')
  })

  it('keeps details collapsed and the credential warning outside the disclosure', async () => {
    const wrapper = mountModal({})
    expect(wrapper.find('details').attributes('open')).toBeUndefined()
    expect(wrapper.find('details').text()).toContain('keys.useKeyModal.launch.claudeRecoveryDetails')
    expect(wrapper.find('details').text()).not.toContain('keys.useKeyModal.note')
    expect(wrapper.text()).toContain('keys.useKeyModal.note')
    const details = wrapper.find('details').element as HTMLDetailsElement
    details.open = true
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    expect(details.open).toBe(false)
    details.open = true
    await wrapper.setProps({ apiKey: 'TEST_ONLY_NEXT_KEY' })
    expect(details.open).toBe(false)
  })

  it('does not display copied launch feedback after clipboard failure', async () => {
    copyToClipboard.mockResolvedValue(false)
    const wrapper = mountModal({})
    await wrapper.find('[data-launch-command="claude"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).not.toContain('keys.useKeyModal.copied')
  })

  it.each([
    { label: 'close', changedProps: { show: false } },
    { label: 'key change', changedProps: { apiKey: 'TEST_ONLY_NEXT_KEY' } },
    { label: 'platform change', changedProps: { platform: 'openai' } },
    { label: 'route change', changedProps: { baseUrl: 'https://next.example.invalid' } }
  ])('ignores pending clipboard feedback after $label', async ({ changedProps }) => {
    let finishCopy: (success: boolean) => void = () => {}
    copyToClipboard.mockImplementation(() => new Promise<boolean>((resolve) => { finishCopy = resolve }))
    const wrapper = mountModal({})
    await wrapper.find('[data-launch-command="claude"]').trigger('click')
    await wrapper.setProps(changedProps)
    finishCopy(true)
    await flushPromises()
    expect(wrapper.text()).not.toContain('keys.useKeyModal.copied')
  })

  it('does not let an older copy timer clear newer launch feedback', async () => {
    vi.useFakeTimers()
    const wrapper = mountModal({ platform: 'openai' })
    const terminal = wrapper.find('[data-launch-command="saiai codex"]')
    const desktop = wrapper.find('[data-launch-command="saiai desktop codex"]')
    await terminal.trigger('click')
    await nextTick()
    await vi.advanceTimersByTimeAsync(1000)
    await desktop.trigger('click')
    await nextTick()
    await vi.advanceTimersByTimeAsync(1000)
    expect(terminal.text()).toBe('keys.useKeyModal.copy')
    expect(desktop.text()).toBe('keys.useKeyModal.copied')
    await vi.advanceTimersByTimeAsync(1000)
    expect(desktop.text()).toBe('keys.useKeyModal.copy')
  })

  it('does not let an older pending copy replace newer launch feedback', async () => {
    let finishFirstCopy: (success: boolean) => void = () => {}
    copyToClipboard.mockImplementationOnce(() => new Promise<boolean>((resolve) => { finishFirstCopy = resolve }))
    const wrapper = mountModal({ platform: 'openai' })
    const terminal = wrapper.find('[data-launch-command="saiai codex"]')
    const desktop = wrapper.find('[data-launch-command="saiai desktop codex"]')
    await terminal.trigger('click')
    await desktop.trigger('click')
    await flushPromises()
    finishFirstCopy(true)
    await flushPromises()
    expect(terminal.text()).toBe('keys.useKeyModal.copy')
    expect(desktop.text()).toBe('keys.useKeyModal.copied')
  })

  it('clears setup copy feedback when the selected shell changes', async () => {
    const wrapper = mountModal({})
    await wrapper.findAll('button').find((button) => button.text() === 'keys.useKeyModal.copySetup')!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('keys.useKeyModal.copied')
    await wrapper.findAll('button').find((button) => button.text() === 'PowerShell')!.trigger('click')
    expect(wrapper.text()).not.toContain('keys.useKeyModal.copied')
    expect(command(wrapper)).toContain('setup.ps1')
  })
})
