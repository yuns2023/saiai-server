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

  it('falls back to the translated headline and keeps text without the product name intact', () => {
    expect(mountLanding({ site_subtitle: '' }).find('h1').text()).toBe('你的 Claude Code 专属入口')
    const plain = mountLanding({ site_subtitle: '编码专用入口' }).find('h1')
    expect(plain.text()).toBe('编码专用入口')
    expect(plain.find('span.whitespace-nowrap').exists()).toBe(false)
    const twice = mountLanding({ site_subtitle: 'Claude Code 用户的 Claude Code 入口' }).find('h1')
    expect(twice.findAll('span.whitespace-nowrap')).toHaveLength(2)
    expect(twice.text()).toBe('Claude Code 用户的 Claude Code 入口')
  })

  it('offers only sign-in when registration is closed', () => {
    const links = mountLanding({ registration_enabled: false, invitation_code_enabled: true }).findAllComponents(RouterLinkStub).map((l) => l.props('to'))
    expect(links).not.toContain('/register')
    expect(links).toContain('/login')
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

  it('words step 1 after the registration settings', () => {
    const step1 = (settings: Partial<PublicSettings>) => mountLanding(settings).find('ol li').text()
    expect(step1({ registration_enabled: true, invitation_code_enabled: true })).toContain('用邀请码注册')
    const open = step1({ registration_enabled: true, invitation_code_enabled: false })
    expect(open).toContain('注册账号')
    expect(open).not.toContain('邀请码')
    const closed = mountLanding({ registration_enabled: false, invitation_code_enabled: true })
    expect(closed.find('ol li').text()).toContain('暂停开放注册')
    expect(closed.findAllComponents(RouterLinkStub).map((l) => l.props('to'))).not.toContain('/register')
  })

  it('renders the docs link only for http(s) or site-relative URLs', () => {
    const docs = (doc_url: string) => mountLanding({ registration_enabled: true, doc_url }).find('footer').findAll('a').filter((a) => a.text() === '使用文档')
    expect(docs('javascript:alert(1)')).toHaveLength(0)
    expect(docs(' JavaScript:alert(1) ')).toHaveLength(0)
    expect(docs('//evil.example/docs')).toHaveLength(0)
    expect(docs('data:text/html,hi')).toHaveLength(0)
    expect(docs('https://docs.example.com/cc')[0].attributes('href')).toBe('https://docs.example.com/cc')
    expect(docs('/docs')[0].attributes('href')).toBe('/docs')
  })

  it('links the key lookup and the AITP repository in the footer', () => {
    const w = mountLanding({ site_name: 'CCYou.dev', registration_enabled: true })
    const footer = w.find('footer')
    expect(footer.findAllComponents(RouterLinkStub).map((l) => l.props('to'))).toContain('/key-usage')
    expect(footer.find('a[href="https://github.com/2742zdz/AITP"]').exists()).toBe(true)
    expect(footer.text()).toMatch(/© \d{4} CCYou\.dev/)
  })
})
