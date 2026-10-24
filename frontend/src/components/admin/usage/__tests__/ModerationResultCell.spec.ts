import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import ModerationResultCell from '../ModerationResultCell.vue'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import type { AdminUsageLog, ModerationDiagnostics } from '@/types'

function render(scores?: Record<string, number>, status = 'passed', reason?: string, diagnostics?: ModerationDiagnostics, events?: AdminUsageLog['moderation_events']) {
  // The test runner uses the runtime-only i18n build: provide compiled messages.
  function messages(source: typeof zh | typeof en) {
    const moderation = source.admin.groups.inputModeration
    return { admin: { groups: { inputModeration: {
      highestRisk: () => moderation.highestRisk,
      chunkedPassed: () => moderation.chunkedPassed,
      reasons: { chunked_input: () => moderation.reasons.chunked_input, joint_context_skipped: () => moderation.reasons.joint_context_skipped, input_too_large: () => moderation.reasons.input_too_large, detector_budget_exceeded: () => moderation.reasons.detector_budget_exceeded },
      diagnostics: Object.fromEntries(Object.entries(moderation.diagnostics).map(([name, value]) => [name, () => value])),
      limits: Object.fromEntries(Object.entries(moderation.limits).map(([name, value]) => [name, () => value])),
      evidence: { statuses: Object.fromEntries(Object.entries(moderation.evidence.statuses).map(([name, value]) => [name, () => value])), view: () => moderation.evidence.view },
      actions: { warning: () => 'warning' },
      viewDetails: () => moderation.viewDetails,
      resultThreshold: () => moderation.resultThreshold,
      statuses: { passed: () => moderation.statuses.passed, flagged: () => moderation.statuses.flagged, failed: () => moderation.statuses.failed }
    } } } }
  }
  const i18n = createI18n({ legacy: false, locale: 'zh', messages: { zh: messages(zh), en: messages(en) } })
  const wrapper = mount(ModerationResultCell, {
    props: { row: { moderation_result: { status, reason, scores, diagnostics, min_confidence: 90 }, moderation_events: events } as AdminUsageLog },
    global: { plugins: [i18n], stubs: { BaseDialog: { props: ['show'], template: '<section v-if="show"><slot /></section>' } } }
  })
  return { wrapper, i18n }
}

describe('moderation summary and localized details', () => {
  it('discloses a skipped joint check on a passed result in both locales', async () => {
    const { wrapper, i18n } = render({ PII: 0.1 }, 'passed', 'joint_context_skipped', {
      input_chars: 400000, instruction_chars: 130000, instruction_blocks: 136, instruction_bundles: 17, joint_skipped: true,
      planned_checks: 31, completed_checks: 31, cache_hits: 17, detector_calls: 14, chunk_size: 32768, max_input_chars: 516608,
      max_detector_calls: 16, max_planned_checks: 64, duration_ms: 100, queue_wait_ms: 10
    })
    expect(wrapper.text()).toContain('分段审核通过')
    await wrapper.get('button').trigger('click')
    expect(wrapper.text()).toContain('已分别检测新文本和指令')
    expect(wrapper.text()).toContain('指令合并检测包数')
    i18n.global.locale.value = 'en'
    await wrapper.vm.$nextTick()
    expect(wrapper.text()).toContain('checked separately, not jointly')
  })

  it('explains the failed coverage budget with numeric diagnostics, without claiming approval', async () => {
    const { wrapper, i18n } = render(undefined, 'failed', 'detector_budget_exceeded', {
      input_chars: 20, instruction_chars: 800, instruction_blocks: 17, planned_checks: 18, completed_checks: 16,
      cache_hits: 0, detector_calls: 16, chunk_size: 32768, max_input_chars: 516608, max_detector_calls: 16, max_planned_checks: 64,
      duration_ms: 100, queue_wait_ms: 10, limit: 'detector_calls'
    })
    await wrapper.get('button').trigger('click')
    expect(wrapper.text()).toContain('新增检测调用超过单任务预算')
    expect(wrapper.get('[data-testid=moderation-diagnostics]').text()).toContain('新增用户文本字符数')
    expect(wrapper.get('[data-testid=moderation-diagnostics]').text()).toContain('新增检测调用预算')
    expect(wrapper.find('[data-testid=highest-risk]').exists()).toBe(false)
    i18n.global.locale.value = 'en'
    await wrapper.vm.$nextTick()
    expect(wrapper.text()).toContain('New detector call budget')
    wrapper.unmount()
  })
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

it('distinguishes chunk coverage from whole-context approval in both languages', async () => {
 const { wrapper, i18n } = render({ PII: .1 }, 'passed', 'chunked_input')
 expect(wrapper.text()).toContain('分段审核通过')
 await wrapper.get('button').trigger('click')
 expect(wrapper.text()).toContain('不代表全文联合判断')
 i18n.global.locale.value = 'en'
 await wrapper.vm.$nextTick()
 expect(wrapper.text()).toContain('Chunk checks passed')
 expect(wrapper.text()).toContain('not full-document joint assessment')
 wrapper.unmount()
})

it('shows evidence status and offers the viewer only for events with stored evidence', async () => {
 const diagnostics = { input_chars: 4, instruction_chars: 0, instruction_blocks: 0, planned_checks: 1, completed_checks: 1, cache_hits: 0, detector_calls: 1,
   chunk_size: 32768, max_input_chars: 516608, max_detector_calls: 16, max_planned_checks: 64, duration_ms: 1, queue_wait_ms: 1,
   evidence_status: 'stored', evidence_count: 1, evidence_omitted_categories: ['PII'] }
 const event = { categories: ['PII'], scores: { PII: .99 }, min_confidence: 90, action: 'warning', counted_as_strike: true, model_version: 'x' }
 const { wrapper } = render({ PII: .99 }, 'flagged', undefined, diagnostics, [{ ...event, event_id: 5, evidence_count: 1, evidence_status: 'stored' }, { ...event, event_id: 6, evidence_count: 0, evidence_status: 'expired' }])
 await wrapper.get('button').trigger('click')
 expect(wrapper.get('[data-testid=moderation-diagnostics]').text()).toContain('已加密保存')
 expect(wrapper.get('[data-testid=moderation-diagnostics]').text()).toContain('隐私滥用')
 expect(wrapper.text()).toContain('已过期删除')
 expect(wrapper.findAll('[data-testid=moderation-evidence]')).toHaveLength(1)
 wrapper.unmount()
})
