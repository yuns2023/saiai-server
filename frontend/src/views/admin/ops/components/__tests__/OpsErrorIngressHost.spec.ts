import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import type { OpsErrorDetail } from '@/api/admin/ops'
import zh from '@/i18n/locales/zh'
import OpsErrorDetailModal from '../OpsErrorDetailModal.vue'

const mockAPI = vi.hoisted(() => ({ getRequestErrorDetail: vi.fn(), listRequestErrorUpstreamErrors: vi.fn() }))
vi.mock('@/api/admin/ops', () => ({ opsAPI: mockAPI }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError: vi.fn() }) }))
vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key.split('.').reduce<unknown>((value, part) => value && typeof value === 'object' ? (value as Record<string, unknown>)[part] : undefined, zh) ?? key })
}))
const DialogStub = defineComponent({ props: ['show'], template: '<div v-if="show"><slot /></div>' })

describe('error detail ingress domain', () => {
  it.each([
    { diagnostics: { schema_version: 1, transport: 'http', transport_status_code: 400, request_host: 'gateway.example.test', forwarded_host: 'edge.example.test' }, expected: 'edge.example.test' },
    { diagnostics: undefined, expected: '未采集' },
    { diagnostics: { schema_version: 1, transport: 'http', transport_status_code: 400, request_host: 'gateway.example.test' }, expected: '未采集' },
    { diagnostics: { schema_version: 99, transport: 'http', transport_status_code: 400, forwarded_host: 'future.example.test' }, expected: '未采集' }
  ])('shows $expected for a failed upload', async ({ diagnostics, expected }) => {
    mockAPI.getRequestErrorDetail.mockResolvedValue({
      id: 42, created_at: '2026-10-21T00:00:00Z', phase: 'request', status_code: 400,
      message: 'Failed to read request body', request_path: '/v1/messages', diagnostics
    } as OpsErrorDetail)
    mockAPI.listRequestErrorUpstreamErrors.mockResolvedValue({ items: [] })
    const wrapper = mount(OpsErrorDetailModal, {
      props: { show: true, errorId: 42, errorType: 'request' },
      global: { stubs: { BaseDialog: DialogStub, OpsErrorDiagnostics: true } }
    })
    await flushPromises()
    expect(wrapper.get('[data-testid="error-ingress-host"]').text()).toBe(expected)
    expect(wrapper.text()).toContain('线路域名（代理报告）')
    wrapper.unmount()
  })
})
