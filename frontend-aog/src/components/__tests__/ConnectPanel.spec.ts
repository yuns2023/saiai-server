import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { i18n } from '@/i18n'
import ConnectPanel from '../ConnectPanel.vue'

function mountPanel(supported?: boolean) {
  setActivePinia(createPinia())
  return mount(ConnectPanel, { props: { apiKey: 'sk-test', supported }, global: { plugins: [i18n] } })
}

describe('ConnectPanel', () => {
  it('leads with the AITP command and keeps manual values collapsed', () => {
    const w = mountPanel()
    const install = w.find('[data-testid=connect-install]')
    expect(install.exists()).toBe(true)
    expect(install.find('pre').text()).toContain('https://github.com/2742zdz/AITP/releases/latest/download/setup.')
    expect(install.find('pre').text()).toContain("'sk-test'")
    expect(install.find('a').attributes('href')).toBe('https://github.com/2742zdz/AITP')
    const manual = w.find('[data-testid=connect-manual]')
    expect(manual.attributes('open')).toBeUndefined()
    expect(manual.text()).toContain('ANTHROPIC_AUTH_TOKEN')
    expect(w.text()).not.toMatch(/openai|codex|gemini/i)
  })

  it('shows a neutral note instead of setup steps for a key outside Claude Code groups', () => {
    const w = mountPanel(false)
    expect(w.find('[data-testid=connect-unsupported]').exists()).toBe(true)
    expect(w.find('pre').exists()).toBe(false)
  })
})
