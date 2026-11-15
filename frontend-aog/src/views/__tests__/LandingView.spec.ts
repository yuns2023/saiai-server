import { beforeEach, describe, expect, it } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { i18n } from '@/i18n'
import { useAppStore } from '@/stores/app'
import type { PublicSettings } from '@/types'
import LandingView from '../LandingView.vue'

function mountLanding(settings: Partial<PublicSettings>) {
  const pinia = createPinia()
  setActivePinia(pinia)
  useAppStore().settings = settings as PublicSettings
  return mount(LandingView, { global: { plugins: [pinia, i18n], stubs: { RouterLink: RouterLinkStub } } })
}

describe('LandingView', () => {
  beforeEach(() => {
    i18n.global.locale.value = 'zh'
  })

  it('keeps "Claude Code" on one line in the configured headline', () => {
    const w = mountLanding({ site_name: 'CCYou.dev', site_subtitle: '你的 Claude Code 专属入口', registration_enabled: true })
    const h1 = w.find('h1')
    expect(h1.text()).toBe('你的 Claude Code 专属入口')
    expect(h1.find('span.whitespace-nowrap').text()).toBe('Claude Code')
  })

  it('describes a Claude Code only service in both languages', () => {
    for (const locale of ['zh', 'en'] as const) {
      i18n.global.locale.value = locale
      const text = mountLanding({ site_name: 'CCYou.dev', registration_enabled: true }).text()
      expect(text).not.toMatch(/OpenAI|Gemini|Google|Codex/)
      expect(text).toContain('claude')
    }
  })

  it('shows the invite-only note only when invitation codes are required', () => {
    expect(mountLanding({ registration_enabled: true, invitation_code_enabled: true }).text()).toContain('仅限邀请注册')
    expect(mountLanding({ registration_enabled: true, invitation_code_enabled: false }).text()).not.toContain('仅限邀请注册')
  })

  it('links the key lookup and the AITP repository in the footer', () => {
    const w = mountLanding({ site_name: 'CCYou.dev', registration_enabled: true })
    const footer = w.find('footer')
    expect(footer.findAllComponents(RouterLinkStub).map((l) => l.props('to'))).toContain('/key-usage')
    expect(footer.find('a[href="https://github.com/2742zdz/AITP"]').exists()).toBe(true)
    expect(footer.text()).toMatch(/© \d{4} CCYou\.dev/)
  })
})
