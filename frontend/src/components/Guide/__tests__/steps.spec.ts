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
    expect(setup.launch.title).toBeTruthy()
    expect(setup.launch.claudeVscode).toContain('Claude Code')
    expect(setup.launch.codexVscode).toContain('Codex')
    expect(setup.launch.claudeRecoveryDetails).toContain('saiai claude')
    expect(setup.launch.claudeRecoveryDetails).toMatch(/User\/project settings still apply|用户／项目设置仍生效/)
    expect(setup.launch.claudeRecoveryDetails).toMatch(/does not repair the VSCode|不用于修复 VSCode/)
    expect(setup.launch.desktopHint).toMatch(/Codex features only|仅支持 Codex 功能/)
    expect(setup.saiaiCliHint).toMatch(/Re-run|可重复执行/)
    expect(setup.description).toMatch(/Install Claude Code first|请先安装 Claude Code/)
    expect(setup.openai.description).toMatch(/Install the Codex client|请先安装要使用的 Codex 客户端/)
    expect(setup.note).toMatch(/clipboard|剪贴板/)
    expect(setup.openai.note).toMatch(/clipboard|剪贴板/)
    expect(setup.note).toMatch(/terminal history|终端历史/)
    expect(setup.openai.note).not.toMatch(/WebSocket/)
  })
})
