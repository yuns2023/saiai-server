import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import zh from '@/i18n/locales/zh'
import type { OpsErrorDetail } from '@/api/admin/ops'
import OpsErrorDiagnostics from '../OpsErrorDiagnostics.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key.split('.').reduce<unknown>((value, part) => value && typeof value === 'object' ? (value as Record<string, unknown>)[part] : undefined, zh) ?? key })
}))

function render(overrides: Partial<OpsErrorDetail> = {}) {
  return mount(OpsErrorDiagnostics, {
    props: { detail: { id: 1, phase: 'request', status_code: 400, error_body: '', ...overrides } as OpsErrorDetail }
  })
}

describe('Ops error diagnostic tables', () => {
  it('keeps the proxy-reported ingress separate from the rewritten Gateway host', () => {
    const wrapper = render({ diagnostics: {
      schema_version: 1, transport: 'http', transport_status_code: 400,
      request_host: 'gateway.example.test', forwarded_host: 'edge.example.test'
    } })
    expect(wrapper.text()).toContain('线路域名（代理报告）edge.example.test')
    expect(wrapper.text()).toContain('Gateway 接收域名gateway.example.test')
    expect(wrapper.text()).toContain('此记录未采集字段校验结果')
  })

  it('shows the specific missing identity field and successful checks', () => {
    const wrapper = render({ diagnostics: {
      schema_version: 1, transport: 'websocket', transport_status_code: 101,
      client_policy: 'local_proxy_only', client_version: '0.163.0-alpha.10.2', checks: [
        { field: 'User-Agent', expected: 'recognized_client', actual: 'recognized', passed: true },
        { field: 'originator', expected: 'recognized_if_present', actual: 'omitted', passed: true },
        { field: 'version', expected: 'non_empty', actual: 'present', passed: true },
        { field: 'chatgpt-account-id', expected: 'non_empty', actual: 'missing', passed: false }
      ]
    } })
    const rows = wrapper.get('[data-testid="diagnostic-checks"]').findAll('tbody tr')
    expect(rows[2].text()).toContain('version非空已提供通过')
    expect(rows[3].text()).toContain('chatgpt-account-id非空缺失失败')
    expect(rows[1].text()).toContain('未提供（允许省略）通过')
    expect(wrapper.text()).toContain('0.163.0-alpha.10.2')
    expect(wrapper.text()).not.toContain('此记录未采集字段校验结果')
  })

  it('shows captured historical WS evidence without inventing header checks', () => {
    const wrapper = render({
      phase: 'upstream', error_source: 'upstream_ws', error_owner: 'provider', upstream_status_code: 400,
      user_agent: 'codex-tui/0.157.1 (codex-tui; 0.163.0)', request_body_bytes: 0,
      error_body: JSON.stringify({ error: { type: 'invalid_request_error', code: 'model_not_supported', param: 'model' }, transport_status_code: 101, websocket_turn: 1, request_payload_sha256: 'e'.repeat(64) })
    })
    expect(wrapper.text()).toContain('transport_status_code101')
    expect(wrapper.text()).toContain('upstream_status_code400')
    expect(wrapper.text()).toContain('error.parammodel')
    expect(wrapper.text()).toContain('error.codemodel_not_supported')
    expect(wrapper.text()).toContain('websocket_turn1')
    expect(wrapper.text()).toContain('e'.repeat(64))
    expect(wrapper.text()).toContain('0.157.1 (codex-tui; 0.163.0)')
    expect(wrapper.text()).toContain('此记录未采集字段校验结果')
    expect(wrapper.find('[data-testid="diagnostic-checks"]').exists()).toBe(false)
  })

  it('handles plain text, missing snapshots and a future schema safely', () => {
    const wrapper = render({ error_body: '<script>TEST_ONLY</script>', diagnostics: {
      schema_version: 99, transport: '', transport_status_code: 0,
      checks: [{ field: 'version', expected: 'non_empty', actual: 'missing', passed: false }]
    } })
    expect(wrapper.text()).toContain('此记录未采集字段校验结果')
    expect(wrapper.text()).not.toContain('TEST_ONLY')
    expect(wrapper.text()).toContain('未采集')
  })
})
