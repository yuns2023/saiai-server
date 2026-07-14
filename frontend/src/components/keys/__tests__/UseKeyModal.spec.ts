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

const codeBlocks = (wrapper: VueWrapper) =>
  wrapper.findAll('pre code').map((code) => code.text())

describe('UseKeyModal', () => {
  it('offers Anthropic keys only a clean Claude V2 flow', async () => {
    const wrapper = mountModal({})
    await nextTick()

    expect(codeBlocks(wrapper)).toEqual([
      'curl -fsSL https://example.com/saiai-cli/setup.sh | bash -s -- install',
      "$HOME/.local/bin/saiai setup claude --base-url 'https://example.com'"
    ])
    expect(wrapper.text()).toContain('keys.useKeyModal.v2.claudeDescription')
    expect(wrapper.text()).toContain('keys.useKeyModal.v2.claudeSetupHint')
    expect(wrapper.text()).toContain('keys.useKeyModal.v2.claudeNote')
    expect(wrapper.text()).not.toContain('keys.useKeyModal.cliTabs.codexCli')
    expect(codeBlocks(wrapper).join('\n')).not.toContain('TEST_ONLY_API_KEY')
  })

  it.each([false, true])('keeps OpenAI Codex-only regardless of Messages dispatch=%s', async (allowMessagesDispatch) => {
    const wrapper = mountModal({
      platform: 'openai',
      allowMessagesDispatch
    })
    await nextTick()

    const buttons = wrapper.findAll('button').map((button) => button.text())
    expect(buttons.some((text) => text.includes('keys.useKeyModal.cliTabs.v2Preview'))).toBe(true)
    expect(buttons.some((text) => text.includes('keys.useKeyModal.cliTabs.claudeCode'))).toBe(false)
    expect(buttons.some((text) => text.includes('keys.useKeyModal.cliTabs.codexCli'))).toBe(false)
    expect(buttons.some((text) => text.includes('keys.useKeyModal.cliTabs.codexCliWs'))).toBe(false)
    expect(codeBlocks(wrapper)).toEqual([
      'curl -fsSL https://example.com/saiai-cli/setup.sh | bash -s -- install',
      "$HOME/.local/bin/saiai setup codex --base-url 'https://example.com'"
    ])
    expect(codeBlocks(wrapper).join('\n')).not.toContain('TEST_ONLY_API_KEY')
    expect(codeBlocks(wrapper).join('\n')).not.toContain('setup claude')
  })

  it('uses the absolute installed binary for PowerShell first setup', async () => {
    const wrapper = mountModal({ platform: 'openai' })
    await nextTick()

    const powershell = wrapper.findAll('button').find((button) => button.text().includes('PowerShell'))
    expect(powershell).toBeDefined()
    await powershell!.trigger('click')
    await nextTick()

    expect(codeBlocks(wrapper)).toEqual([
      'irm https://example.com/saiai-cli/setup.ps1 | iex; Invoke-Saiai install',
      '& "$env:LOCALAPPDATA\\SAIAI\\bin\\saiai.exe" setup codex --base-url \'https://example.com\''
    ])
  })

  it.each([
    ['macOS / Linux', "$HOME/.local/bin/saiai setup codex --base-url 'https://example.com/tenant'"],
    ['PowerShell', '& "$env:LOCALAPPDATA\\SAIAI\\bin\\saiai.exe" setup codex --base-url \'https://example.com/tenant\''],
    ['Windows CMD', '"%LOCALAPPDATA%\\SAIAI\\bin\\saiai.exe" setup codex --base-url "https://example.com/tenant"']
  ])('preserves a Gateway path prefix for %s', async (tabLabel, expectedSetup) => {
    const wrapper = mountModal({
      platform: 'openai',
      baseUrl: 'https://example.com/tenant/v1/'
    })
    await nextTick()

    const tab = wrapper.findAll('button').find((button) => button.text().includes(tabLabel))
    expect(tab).toBeDefined()
    await tab!.trigger('click')
    await nextTick()

    const blocks = codeBlocks(wrapper)
    expect(blocks[0]).toContain('https://example.com/saiai-cli/setup.')
    expect(blocks[1]).toBe(expectedSetup)
    expect(blocks.join('\n')).not.toContain('/tenant/v1')
  })

  it('offers Antigravity Claude V2 plus its independent Gemini entry', async () => {
    const wrapper = mountModal({ platform: 'antigravity' })
    await nextTick()

    expect(codeBlocks(wrapper)[1]).toContain('setup claude')
    expect(codeBlocks(wrapper).join('\n')).not.toContain('TEST_ONLY_API_KEY')

    const gemini = wrapper.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.geminiCli')
    )
    expect(gemini).toBeDefined()
    await gemini!.trigger('click')
    await nextTick()

    expect(codeBlocks(wrapper).join('\n')).toContain('GOOGLE_GEMINI_BASE_URL')
    expect(codeBlocks(wrapper).join('\n')).toContain('TEST_ONLY_API_KEY')
  })

  it('keeps the direct Gemini configuration for Gemini groups', async () => {
    const wrapper = mountModal({ platform: 'gemini' })
    await nextTick()

    expect(codeBlocks(wrapper)).toHaveLength(1)
    expect(codeBlocks(wrapper)[0]).toContain('GOOGLE_GEMINI_BASE_URL')
    expect(codeBlocks(wrapper)[0]).toContain('TEST_ONLY_API_KEY')
    expect(codeBlocks(wrapper)[0]).not.toContain('saiai setup')
  })

  it('does not misroute Sora keys into Claude setup', async () => {
    const wrapper = mountModal({ platform: 'sora' })
    await nextTick()

    expect(codeBlocks(wrapper)).toEqual([])
    expect(wrapper.text()).toContain('keys.useKeyModal.sora.description')
    expect(wrapper.text()).toContain('keys.useKeyModal.sora.note')
    expect(wrapper.findAll('button').some((button) => button.text().includes('macOS / Linux'))).toBe(false)
  })

  it('never exposes removed legacy initialization commands in V2 product flows', async () => {
    for (const platform of ['anthropic', 'openai'] as const) {
      const wrapper = mountModal({ platform, allowMessagesDispatch: true })
      await nextTick()
      const output = codeBlocks(wrapper).join('\n')
      for (const forbidden of ['init-codex', 'saiai start', 'ANTHROPIC_AUTH_TOKEN', '--websockets']) {
        expect(output).not.toContain(forbidden)
      }
    }
  })
})
