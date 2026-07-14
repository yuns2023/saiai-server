import { describe, expect, it, vi } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard: vi.fn().mockResolvedValue(true)
  })
}))

import UseKeyModal from '../UseKeyModal.vue'

function mountModal(props: Record<string, unknown>): VueWrapper {
  return mount(UseKeyModal, {
    props: {
      show: true,
      apiKey: 'sk-test',
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
}

describe('UseKeyModal', () => {
  it('renders saiai one-click install command for claude code', () => {
    const wrapper = mountModal({})

    const codeBlock = wrapper.find('pre code')
    expect(codeBlock.exists()).toBe(true)
    expect(codeBlock.text()).toContain('setup.sh')
    expect(codeBlock.text()).toContain("bash -s -- 'https://example.com/v1' 'sk-test'")
    expect(codeBlock.text()).not.toContain('init-proxy')
    expect(codeBlock.text()).not.toContain('--proxy-url')
    expect(codeBlock.text()).not.toContain('SAIAI_DOWNLOAD_BASE')
    expect(codeBlock.text()).not.toContain('ANTHROPIC_AUTH_TOKEN')
  })

  it('does not render claude code proxy mode controls', async () => {
    const wrapper = mountModal({})
    await nextTick()

    expect(wrapper.find('input#saiai-cli-proxy-url').exists()).toBe(false)
    const buttons = wrapper.findAll('button').map((button) => button.text())
    expect(buttons.some((text) => text.includes('keys.useKeyModal.proxyMode.direct'))).toBe(false)
    expect(buttons.some((text) => text.includes('keys.useKeyModal.proxyMode.proxy'))).toBe(false)
  })

  it('does not render opencode client tab', async () => {
    const wrapper = mountModal({
      platform: 'openai',
      allowMessagesDispatch: true
    })

    await nextTick()
    const buttons = wrapper.findAll('button').map((button) => button.text())
    expect(buttons.some((text) => text.includes('keys.useKeyModal.cliTabs.opencode'))).toBe(false)
  })

  it('offers a clean V2 Preview without putting the API key on the command line', async () => {
    const wrapper = mountModal({
      platform: 'openai',
      allowMessagesDispatch: true
    })
    await nextTick()

    const buttons = wrapper.findAll('button').map((button) => button.text())
    expect(buttons.some((text) => text.includes('keys.useKeyModal.cliTabs.v2Preview'))).toBe(true)

    const text = wrapper.find('pre code').text()
    expect(text).toBe('curl -fsSL https://example.com/saiai-cli/setup.sh | bash -s -- install')
    expect(text).not.toContain('sk-test')
    expect(text).not.toContain('SAIAI_DOWNLOAD_BASE')
    expect(wrapper.text()).toContain('keys.useKeyModal.v2.hint')
    expect(wrapper.text()).toContain('keys.useKeyModal.v2.note')
  })

  it('renders the V2 install-only PowerShell form', async () => {
    const wrapper = mountModal({
      platform: 'openai',
      allowMessagesDispatch: true
    })
    await nextTick()

    const powershellBtn = wrapper.findAll('button').find((button) => button.text().includes('PowerShell'))
    expect(powershellBtn).toBeDefined()
    await powershellBtn!.trigger('click')
    await nextTick()

    const text = wrapper.find('pre code').text()
    expect(text).toBe('irm https://example.com/saiai-cli/setup.ps1 | iex; Invoke-Saiai install')
    expect(text).not.toContain('sk-test')
  })

  it('renders codex CLI one-click command (no --websockets) on default codex tab for openai', async () => {
    const wrapper = mountModal({ platform: 'openai' })
    await nextTick()

    const code = wrapper.find('pre code')
    expect(code.exists()).toBe(true)
    const text = code.text()
    // Single one-click command, not separate config.toml + auth.json files
    expect(text).toContain('setup.sh')
    expect(text).toContain("init-codex 'https://example.com/v1' 'sk-test'")
    // No OPENAI_API_KEY env-clearing: provider config writes no env_key, so
    // Codex CLI's standard Responses path never reads the env var. auth.json
    // is the single key source.
    expect(text).not.toContain('OPENAI_API_KEY')
    // saiai-cli helper origin must drop the /v1 path
    expect(text).not.toContain('SAIAI_DOWNLOAD_BASE')
    expect(text).not.toContain('https://example.com/v1/saiai-cli')
    // --base-url passed to the CLI keeps the original path
    expect(text).toContain("'https://example.com/v1'")
    // Default codex tab does NOT enable websockets
    expect(text).not.toContain('--websockets')
  })

  it('appends --websockets when codex-ws tab is active', async () => {
    const wrapper = mountModal({ platform: 'openai' })
    await nextTick()

    // Click the codex-ws tab
    const wsBtn = wrapper.findAll('button').find((b) =>
      b.text().includes('keys.useKeyModal.cliTabs.codexCliWs')
    )
    expect(wsBtn).toBeDefined()
    await wsBtn!.trigger('click')
    await nextTick()

    const text = wrapper.find('pre code').text()
    expect(text).toContain('init-codex')
    expect(text).toContain('--websockets')
  })

  it('shows macOS Linux and Windows install tabs for codex setup', async () => {
    const wrapper = mountModal({ platform: 'openai' })
    await nextTick()

    const buttons = wrapper.findAll('button').map((b) => b.text())
    expect(buttons.some((t) => t.includes('macOS / Linux'))).toBe(true)
    expect(buttons.some((t) => t.includes('Windows CMD'))).toBe(true)
    expect(buttons.some((t) => t.includes('PowerShell'))).toBe(true)
  })

  it('renders PowerShell install form for codex setup', async () => {
    const wrapper = mountModal({ platform: 'openai' })
    await nextTick()

    const powershellBtn = wrapper.findAll('button').find((b) => b.text().includes('PowerShell'))
    expect(powershellBtn).toBeDefined()
    await powershellBtn!.trigger('click')
    await nextTick()

    const text = wrapper.find('pre code').text()
    expect(text).toContain("init-codex 'https://example.com/v1' 'sk-test'")
    expect(text).toContain('Invoke-Saiai')
    expect(text).not.toContain('OPENAI_API_KEY')
  })
})
