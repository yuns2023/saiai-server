import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import ModerationEvidenceViewer from '../ModerationEvidenceViewer.vue'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'

const { getModerationEvidence, holdModerationEvidence, releaseModerationEvidence } = vi.hoisted(() => ({
  getModerationEvidence: vi.fn(), holdModerationEvidence: vi.fn(), releaseModerationEvidence: vi.fn()
}))
vi.mock('@/api/admin/usage', () => ({ getModerationEvidence, holdModerationEvidence, releaseModerationEvidence }))
vi.mock('@/utils/format', () => ({ formatDateTime: (s: string) => s }))

// The test runner uses the runtime-only i18n build: compile the strings used here.
function messages(source: typeof zh | typeof en) {
  const evidence = source.admin.groups.inputModeration.evidence
  const compile = (value: string) => (ctx: { named: (key: string) => unknown }) =>
    value.replace(/\{(\w+)\}/g, (_, key) => String(ctx.named(key)))
  const deep = (node: Record<string, unknown>): Record<string, unknown> => Object.fromEntries(Object.entries(node).map(
    ([key, value]) => [key, typeof value === 'string' ? compile(value) : deep(value as Record<string, unknown>)]))
  return { admin: { groups: { inputModeration: { evidence: deep(evidence as unknown as Record<string, unknown>) } } } }
}

function mountViewer() {
  const i18n = createI18n({ legacy: false, locale: 'zh', messages: { zh: messages(zh), en: messages(en) } })
  return mount(ModerationEvidenceViewer, { props: { eventId: 5 }, global: { plugins: [i18n] } })
}

describe('ModerationEvidenceViewer', () => {
  beforeEach(() => { getModerationEvidence.mockReset(); holdModerationEvidence.mockReset(); releaseModerationEvidence.mockReset() })

  it('asks for confirmation before fetching and labels check kinds', async () => {
    getModerationEvidence.mockResolvedValue({
      event_id: 5, truncated: true, omitted_categories: ['violence'], expires_at: '2027-01-01T00:00:00Z', hold_until: null,
      checks: [
        { check_index: 0, kinds: ['joint'], sources: [{ kind: 'joint', start: 0, end: 12 }], char_count: 12, text: 'TEST_ONLY joint', scores: { PII: 0.99 }, categories: ['PII'] },
        { check_index: 1, kinds: ['instructions'], sources: [{ kind: 'instructions', bundle: 2, start: 0, end: 9 }], char_count: 9, text: 'TEST_ONLY bundle', scores: { PII: 0.95 }, categories: ['PII'] }
      ]
    })
    const wrapper = mountViewer()
    await wrapper.get('button').trigger('click')
    expect(getModerationEvidence).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('查看会被记录')
    await wrapper.get('[data-testid=evidence-confirm]').trigger('click')
    await flushPromises()
    expect(getModerationEvidence).toHaveBeenCalledWith(5)
    expect(wrapper.text()).toContain('联合检查（指令+正文）')
    expect(wrapper.text()).toContain('指令包 #2')
    expect(wrapper.text()).toContain('TEST_ONLY bundle')
    expect(wrapper.find('[data-testid=evidence-truncated]').exists()).toBe(true)
  })

  it('holds with a reason and releases', async () => {
    getModerationEvidence.mockResolvedValue({ event_id: 5, truncated: false, expires_at: '2027-01-01T00:00:00Z', hold_until: null, checks: [] })
    holdModerationEvidence.mockResolvedValue({ event_id: 5, hold_until: '2027-02-01T00:00:00Z' })
    releaseModerationEvidence.mockResolvedValue({ event_id: 5 })
    const wrapper = mountViewer()
    await wrapper.get('button').trigger('click')
    await wrapper.get('[data-testid=evidence-confirm]').trigger('click')
    await flushPromises()
    await wrapper.get('input[type=text]').setValue('case 42')
    await wrapper.findAll('button').find(b => b.text() === '保全')!.trigger('click')
    await flushPromises()
    expect(holdModerationEvidence).toHaveBeenCalledWith(5, 90, 'case 42')
    expect(wrapper.get('[data-testid=evidence-hold]').text()).toContain('2027-02-01')
    await wrapper.findAll('button').find(b => b.text() === '解除保全')!.trigger('click')
    await flushPromises()
    expect(releaseModerationEvidence).toHaveBeenCalledWith(5, '')
    expect(wrapper.find('[data-testid=evidence-hold]').exists()).toBe(false)
  })

  it('shows the server reason when access is refused', async () => {
    getModerationEvidence.mockRejectedValue({ message: 'moderation evidence requires a personal administrator login' })
    const wrapper = mountViewer()
    await wrapper.get('button').trigger('click')
    await wrapper.get('[data-testid=evidence-confirm]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role=alert]').text()).toContain('personal administrator login')
  })
})
