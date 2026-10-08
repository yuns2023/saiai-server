import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import OpsRequestDetailsModal from '../OpsRequestDetailsModal.vue'

const mockAPI = vi.hoisted(() => ({ listRequestDetails: vi.fn() }))
vi.mock('@/api/admin/ops', () => ({ opsAPI: mockAPI }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError: vi.fn(), showWarning: vi.fn() }) }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))
vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key })
}))
const DialogStub = defineComponent({ props: ['show'], template: '<div v-if="show"><slot /></div>' })

describe('request details provider refusals', () => {
  it('shows the requested model, logical 400, WS origin and escaped reason and opens the exact error', async () => {
    mockAPI.listRequestDetails.mockResolvedValue({ total: 2, items: [
      { kind: 'error', created_at: '2026-10-08T17:00:00Z', model: 'test-luna', platform: 'openai', status_code: 400,
        error_owner: 'provider', error_source: 'upstream_ws', phase: 'upstream', error_id: 42,
        message: 'The test-luna model is not supported <script>bad</script>' },
      { kind: 'success', created_at: '2026-10-08T17:01:00Z', model: 'available', status_code: 200 }
    ] })
    const wrapper = mount(OpsRequestDetailsModal, {
      props: { modelValue: false, timeRange: '1h', preset: { title: 'Requests' } },
      global: { stubs: { BaseDialog: DialogStub, Pagination: true } }
    })
    await wrapper.setProps({ modelValue: true })
    await flushPromises()
    const row = wrapper.findAll('tbody tr')[0]
    expect(row.text()).toContain('test-luna')
    expect(row.text()).toContain('400')
    expect(row.text()).toContain('admin.ops.requestDetails.origin.provider')
    expect(row.text()).toContain('admin.ops.requestDetails.originDetail.upstream_ws')
    expect(row.text()).toContain('not supported <script>bad</script>')
    expect(row.find('script').exists()).toBe(false)
    await row.findAll('button').find(button => button.text() === 'admin.ops.requestDetails.viewError')!.trigger('click')
    expect(wrapper.emitted('openErrorDetail')).toEqual([[42]])
    wrapper.unmount()
  })
})
