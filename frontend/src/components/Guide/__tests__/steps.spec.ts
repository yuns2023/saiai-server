import { describe, expect, it } from 'vitest'
import { getUserSteps } from '../steps'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

describe('user setup tour', () => {
  it('continues from successful creation to the setup command', () => {
    const steps = getUserSteps((key) => key)
    expect(steps.at(-2)?.element).toBe('[data-tour="key-form-submit"]')
    expect(steps.at(-1)?.element).toBe('[data-tour="key-setup-command"]')
    expect(steps.at(-1)?.popover?.title).toBe('onboarding.user.keySetup.title')
  })

  it.each([{ language: 'en', messages: en }, { language: 'zh', messages: zh }])('keeps $language guidance accurate and warns about command credentials', ({ messages }) => {
    const tour = messages.onboarding.user
    const setup = messages.keys.useKeyModal
    expect(tour.createKey.description).not.toMatch(/only shown once|只显示一次/)
    expect(tour.keySubmit.description).not.toMatch(/only shown once|只显示一次/)
    expect(tour.keySetup.description).toBeTruthy()
    expect(setup.steps.codexStart).toContain('saiai codex')
    expect(setup.steps.codexStart).toContain('saiai desktop codex')
    expect(setup.steps.claudeStart).toContain('claude')
    expect(setup.note).toMatch(/clipboard|剪贴板/)
    expect(setup.openai.note).toMatch(/clipboard|剪贴板/)
  })
})
