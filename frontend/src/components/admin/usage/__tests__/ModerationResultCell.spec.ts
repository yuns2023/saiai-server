import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import ModerationResultCell from '../ModerationResultCell.vue'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import type { AdminUsageLog } from '@/types'

function render(scores?: Record<string, number>, status = 'passed') {
  // The test runner uses the runtime-only i18n build: provide compiled messages.
  function messages(source: typeof zh | typeof en) {
    const moderation = source.admin.groups.inputModeration
    return { admin: { groups: { inputModeration: {
      highestRisk: () => moderation.highestRisk,
      viewDetails: () => moderation.viewDetails,
      resultThreshold: () => moderation.resultThreshold,
      statuses: { passed: () => moderation.statuses.passed, flagged: () => moderation.statuses.flagged }
    } } } }
  }
  const i18n = createI18n({ legacy: false, locale: 'zh', messages: { zh: messages(zh), en: messages(en) } })
  const wrapper = mount(ModerationResultCell, {
    props: { row: { moderation_result: { status, scores, min_confidence: 90 } } as AdminUsageLog },
    global: { plugins: [i18n], stubs: { BaseDialog: { props: ['show'], template: '<section v-if="show"><slot /></section>' } } }
  })
  return { wrapper, i18n }
}

describe('moderation summary and localized details', () => {
  it('shows only the maximum score until opened, then translates categories and supports locale changes', async () => {
    const { wrapper, i18n } = render({ Jailbreak: .23, 'copyright/unauthorized': .03, 'self-harm/intent': .05, TEST_UNKNOWN: .02 })
    expect(wrapper.get('[data-testid=highest-risk]').text()).toBe('· 最高风险评分 23.0%')
    expect(wrapper.text()).not.toContain('明确的版权滥用风险')
    expect(wrapper.text()).not.toContain('越狱与指令绕过')
    await wrapper.get('button').trigger('click')
    expect(wrapper.text()).toContain('越狱与指令绕过 23.0%')
    expect(wrapper.text()).toContain('明确的版权滥用风险 3.0%')
    expect(wrapper.text()).toContain('自伤意图（仅记录） 5.0%')
    expect(wrapper.text()).toContain('TEST_UNKNOWN')
    i18n.global.locale.value = 'en'
    await wrapper.vm.$nextTick()
    expect(wrapper.text()).toContain('Jailbreak 23.0%')
    wrapper.unmount()
  })

  it.each([undefined, {}, { invalid: Number.NaN, outOfRange: 2 }])('does not invent a maximum when valid scores are absent', scores => {
    const { wrapper } = render(scores)
    expect(wrapper.find('[data-testid=highest-risk]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('retains a valid zero maximum', () => {
    const { wrapper } = render({ PII: 0 })
    expect(wrapper.get('[data-testid=highest-risk]').text()).toContain('0.0%')
    wrapper.unmount()
  })

  it('localizes hit labels without adding a passed summary to flagged results', () => {
    const { wrapper } = render({ violence: .95, PII: .02 }, 'flagged')
    expect(wrapper.text()).toContain('暴力描写或鼓励')
    expect(wrapper.text()).not.toContain('隐私滥用')
    expect(wrapper.find('[data-testid=highest-risk]').exists()).toBe(false)
    wrapper.unmount()
  })
})
