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
}

const command = (wrapper: VueWrapper) => wrapper.find('pre code').text()

describe('UseKeyModal', () => {
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

  it('keeps OpenAI on Codex by default and offers Messages-compatible Claude only when enabled', async () => {
    const codex = mountModal({ platform: 'openai', allowMessagesDispatch: false })
    await nextTick()
    expect(command(codex)).toContain(
      "init-codex 'https://example.com/v1' 'TEST_ONLY_API_KEY'"
    )
    expect(command(codex)).not.toContain('--websockets')
    expect(codex.findAll('button').some((button) => button.text().includes('keys.useKeyModal.cliTabs.claudeCode'))).toBe(false)

    const compatible = mountModal({ platform: 'openai', allowMessagesDispatch: true })
    await nextTick()
    const claude = compatible.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.claudeCode')
    )
    expect(claude).toBeDefined()
    await claude!.trigger('click')
    await nextTick()
    expect(command(compatible)).toContain("'https://example.com' 'TEST_ONLY_API_KEY'")
    expect(command(compatible)).not.toContain('init-codex')
  })

  it('adds the Codex WebSocket option only on its explicit tab', async () => {
    const wrapper = mountModal({ platform: 'openai' })
    await nextTick()
    const websocket = wrapper.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.codexCliWs')
    )
    expect(websocket).toBeDefined()
    await websocket!.trigger('click')
    await nextTick()
    expect(command(wrapper)).toContain('init-codex')
    expect(command(wrapper)).toContain('--websockets')
  })

  it('offers Antigravity Claude config plus its independent Gemini entry', async () => {
    const wrapper = mountModal({ platform: 'antigravity' })
    await nextTick()
    expect(command(wrapper)).toContain("'https://example.com/antigravity' 'TEST_ONLY_API_KEY'")

    const gemini = wrapper.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.geminiCli')
    )
    expect(gemini).toBeDefined()
    await gemini!.trigger('click')
    await nextTick()
    expect(command(wrapper)).toContain('GOOGLE_GEMINI_BASE_URL')
    expect(command(wrapper)).toContain('TEST_ONLY_API_KEY')
  })

  it('keeps direct Gemini configuration and does not misroute Sora', async () => {
    const gemini = mountModal({ platform: 'gemini' })
    await nextTick()
    expect(command(gemini)).toContain('GOOGLE_GEMINI_BASE_URL')
    expect(command(gemini)).toContain('TEST_ONLY_API_KEY')

    const sora = mountModal({ platform: 'sora' })
    await nextTick()
    expect(sora.findAll('pre code')).toHaveLength(0)
    expect(sora.text()).toContain('keys.useKeyModal.sora.description')
  })

  it('does not expose withdrawn V2 setup or launcher commands', async () => {
    for (const platform of ['anthropic', 'openai'] as const) {
      const wrapper = mountModal({ platform, allowMessagesDispatch: true })
      await nextTick()
      const output = wrapper.findAll('pre code').map((block) => block.text()).join('\n')
      for (const removed of ['setup claude', 'saiai claude', 'revoke --all', 'V2 Preview']) {
        expect(output).not.toContain(removed)
      }
      expect(output).toContain('TEST_ONLY_API_KEY')
    }
  })
})
