import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import HostedApplicationsView from '../HostedApplicationsView.vue'

const { list, approve, reject, getAll, getSettings, showError, showSuccess, store } = vi.hoisted(() => ({
  list: vi.fn(),
  approve: vi.fn(),
  reject: vi.fn(),
  getAll: vi.fn(),
  getSettings: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  store: { hostedSelfOnboardEnabled: true }
}))
vi.mock('@/api/client', () => ({ apiClient: {} }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<div><slot /></div>' } }))
vi.mock('@/components/layout/TablePageLayout.vue', () => ({ default: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' } }))
vi.mock('@/components/common/Pagination.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/icons/Icon.vue', () => ({ default: { template: '<i />' } }))
// A native <select> stand-in so tests can pick a proxy.
vi.mock('@/components/common/Select.vue', () => ({
  default: {
    props: ['modelValue', 'options'],
    emits: ['update:modelValue', 'change'],
    template:
      '<select :value="modelValue ?? \'\'" @change="$emit(\'update:modelValue\', $event.target.value === \'\' || isNaN(Number($event.target.value)) ? $event.target.value : Number($event.target.value)); $emit(\'change\')"><option value=""></option><option v-for="o in options" :key="String(o.value)" :value="o.value">{{ o.label }}</option></select>'
  }
}))
vi.mock('@/components/common/BaseDialog.vue', () => ({
  default: { props: ['show', 'title'], template: '<div v-if="show" class="dialog"><h3>{{ title }}</h3><slot /><slot name="footer" /></div>' }
}))
vi.mock('@/components/common/DataTable.vue', () => ({
  default: {
    props: ['data', 'columns', 'loading'],
    template:
      '<div><div v-for="row in data" :key="row.id" class="row" :data-id="row.id"><template v-for="col in columns" :key="col.key"><slot :name="`cell-${col.key}`" :row="row" :value="row[col.key]" /></template></div><slot v-if="!data.length" name="empty" /></div>'
  }
}))
vi.mock('@/utils/format', () => ({ formatDateTime: (s: string) => s }))
vi.mock('@/composables/usePersistedPageSize', () => ({ getPersistedPageSize: () => 20 }))
vi.mock('@/api/admin', () => ({
  adminAPI: { hostedApplications: { list, approve, reject }, proxies: { getAll }, settings: { getSettings } }
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
vi.mock('@/stores/adminSettings', () => ({ useAdminSettingsStore: () => store }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key} ${JSON.stringify(params)}` : key),
    te: (key: string) => key.startsWith('admin.hostedApplications.statuses.') || (key.startsWith('admin.hostedApplications.errors.SELF_ONBOARD_') || key.startsWith('admin.hostedApplications.errors.UPSTREAM_'))
  })
}))

const withProxy = {
  id: 1, status: 'pending', display_name: '我的 Max 号', upstream_email: 'owner@example.com',
  proxy_display: 'socks5h://proxy.example.com:1080', reject_reason: '', account_id: null,
  created_at: '2026-11-18T01:00:00Z', reviewed_at: null,
  user_id: 42, user_email: 'alice@example.com', proxy_protocol: 'socks5h', proxy_host: 'proxy.example.com', proxy_port: 1080, has_proxy_auth: true
}
const withoutProxy = { ...withProxy, id: 2, proxy_display: '', proxy_protocol: '', proxy_host: '', proxy_port: 0, has_proxy_auth: false }
const approvedRow = { ...withProxy, id: 3, status: 'approved', account_id: 77, reviewed_at: '2026-11-18T02:00:00Z' }

const settings = {
  hosted_self_onboard_enabled: true,
  hosted_self_onboard_default_concurrency: 4,
  hosted_self_onboard_default_priority: 2,
  hosted_self_onboard_default_device_limit: 6,
  hosted_self_onboard_default_admissions_per_cycle: 2,
  hosted_self_onboard_default_interval_hours: 12
}

async function mountView() {
  const w = mount(HostedApplicationsView, { global: { stubs: { RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' } } } })
  await flushPromises()
  return w
}

function row(w: Awaited<ReturnType<typeof mountView>>, id: number) {
  return w.get(`.row[data-id="${id}"]`)
}

beforeEach(() => {
  vi.clearAllMocks()
  store.hostedSelfOnboardEnabled = true
  list.mockResolvedValue({ items: [structuredClone(withProxy), structuredClone(withoutProxy), structuredClone(approvedRow)], total: 3, page: 1, page_size: 20, pages: 1 })
  getAll.mockResolvedValue([{ id: 8, name: 'HK-1', protocol: 'socks5', host: 'hk.example.com', port: 1080, status: 'active' }])
  getSettings.mockResolvedValue(structuredClone(settings))
})

describe('Hosting applications (admin)', () => {
  it('lists pending applications by default with applicant, proxy and result', async () => {
    const w = await mountView()
    expect(list).toHaveBeenCalledWith(1, 20, { status: 'pending' })
    expect(row(w, 1).text()).toContain('alice@example.com')
    expect(row(w, 1).text()).toContain('owner@example.com')
    expect(row(w, 1).text()).toContain('socks5h://proxy.example.com:1080')
    expect(row(w, 1).text()).toContain('admin.hostedApplications.proxyAuth')
    expect(row(w, 2).text()).toContain('admin.hostedApplications.noProxy')
    const link = row(w, 3).get('[data-testid=account-link]')
    expect(link.attributes('href')).toBe('/admin/accounts')
    expect(link.text()).toContain('77')
    // Only pending rows have actions.
    expect(row(w, 3).find('[data-testid=approve]').exists()).toBe(false)
  })

  it('prefills the approve dialog from the settings and uses the submitted proxy when there is one', async () => {
    approve.mockResolvedValue({ application: { ...withProxy, status: 'approved', account_id: 91 }, account_id: 91, group_id: 15 })
    const w = await mountView()
    await row(w, 1).get('[data-testid=approve]').trigger('click')
    expect((w.get('[data-testid=mode-submitted]').element as HTMLInputElement).checked).toBe(true)
    expect((w.get('[data-testid=field-concurrency]').element as HTMLInputElement).value).toBe('4')
    expect((w.get('[data-testid=field-carpool_interval_hours]').element as HTMLInputElement).value).toBe('12')
    await w.get('[data-testid=approve-dialog]').trigger('submit')
    await flushPromises()
    expect(approve).toHaveBeenCalledWith(1, {
      proxy_mode: 'submitted',
      concurrency: 4,
      priority: 2,
      carpool_device_limit: 6,
      carpool_admissions_per_cycle: 2,
      carpool_interval_hours: 12
    })
    const result = w.get('[data-testid=approve-result]')
    expect(result.text()).toContain('"account":91')
    expect(result.get('[data-testid=approve-result-link]').attributes('href')).toBe('/admin/accounts')
  })

  it('cannot approve without a proxy: no submitted proxy and none chosen', async () => {
    const w = await mountView()
    await row(w, 2).get('[data-testid=approve]').trigger('click')
    expect((w.get('[data-testid=mode-submitted]').element as HTMLInputElement).disabled).toBe(true)
    expect((w.get('[data-testid=mode-existing]').element as HTMLInputElement).checked).toBe(true)
    await w.get('[data-testid=approve-dialog]').trigger('submit')
    await flushPromises()
    expect(approve).not.toHaveBeenCalled()
    expect(w.find('[data-testid=proxy-error]').exists()).toBe(true)

    await w.get('[data-testid=proxy-select]').setValue('8')
    await w.get('[data-testid=approve-dialog]').trigger('submit')
    await flushPromises()
    expect(approve).toHaveBeenCalledTimes(1)
    expect(approve.mock.calls[0][1]).toMatchObject({ proxy_mode: 'existing', proxy_id: 8 })
  })

  it('blocks invalid numbers', async () => {
    const w = await mountView()
    await row(w, 1).get('[data-testid=approve]').trigger('click')
    await w.get('[data-testid=field-carpool_device_limit]').setValue('40')
    await w.get('[data-testid=approve-dialog]').trigger('submit')
    await flushPromises()
    expect(approve).not.toHaveBeenCalled()
    expect(w.find('[data-testid=number-error]').exists()).toBe(true)
  })

  it('shows a mapped error when the account is already hosted', async () => {
    approve.mockRejectedValue({ status: 409, code: 409, reason: 'SELF_ONBOARD_ALREADY_HOSTED', message: 'conflict' })
    const w = await mountView()
    await row(w, 1).get('[data-testid=approve]').trigger('click')
    await w.get('[data-testid=approve-dialog]').trigger('submit')
    await flushPromises()
    expect(showError).toHaveBeenCalledWith('admin.hostedApplications.errors.SELF_ONBOARD_ALREADY_HOSTED')
  })

  it('rejects only with a reason, which is sent to the server', async () => {
    reject.mockResolvedValue({ ...withProxy, status: 'rejected', reject_reason: '代理连不上' })
    const w = await mountView()
    await row(w, 1).get('[data-testid=reject]').trigger('click')
    await w.get('[data-testid=reject-dialog]').trigger('submit')
    await flushPromises()
    expect(reject).not.toHaveBeenCalled()
    await w.get('[data-testid=reject-reason]').setValue('  代理连不上  ')
    await w.get('[data-testid=reject-dialog]').trigger('submit')
    await flushPromises()
    expect(reject).toHaveBeenCalledWith(1, '代理连不上')
    expect(w.find('[data-testid=reject-dialog]').exists()).toBe(false)
  })

  it('switch off: approve is disabled, reject still works', async () => {
    store.hostedSelfOnboardEnabled = false
    getSettings.mockResolvedValue({ ...settings, hosted_self_onboard_enabled: false })
    const w = await mountView()
    expect(w.find('[data-testid=switch-off]').exists()).toBe(true)
    expect((row(w, 1).get('[data-testid=approve]').element as HTMLButtonElement).disabled).toBe(true)
    expect((row(w, 1).get('[data-testid=reject]').element as HTMLButtonElement).disabled).toBe(false)
  })

  it('keeps approve disabled until this page has read the settings', async () => {
    let release!: (v: unknown) => void
    getSettings.mockReturnValue(new Promise((r) => { release = r }))
    const w = mount(HostedApplicationsView, { global: { stubs: { RouterLink: true } } })
    await flushPromises()
    expect((row(w, 1).get('[data-testid=approve]').element as HTMLButtonElement).disabled).toBe(true)
    release(structuredClone(settings))
    await flushPromises()
    expect((row(w, 1).get('[data-testid=approve]').element as HTMLButtonElement).disabled).toBe(false)
  })

  it('settings read failed: approve stays off until a retry succeeds, reject still works', async () => {
    getSettings.mockRejectedValueOnce({ status: 500, message: 'boom' })
    const w = await mountView()
    expect(w.find('[data-testid=context-failed]').exists()).toBe(true)
    expect((row(w, 1).get('[data-testid=approve]').element as HTMLButtonElement).disabled).toBe(true)
    expect((row(w, 1).get('[data-testid=reject]').element as HTMLButtonElement).disabled).toBe(false)
    await w.get('[data-testid=context-failed] button').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid=context-failed]').exists()).toBe(false)
    expect((row(w, 1).get('[data-testid=approve]').element as HTMLButtonElement).disabled).toBe(false)
  })

  it('reloads with the chosen status filter', async () => {
    const w = await mountView()
    await w.get('[data-testid=status-filter]').setValue('')
    await flushPromises()
    expect(list).toHaveBeenLastCalledWith(1, 20, { status: '' })
  })
})
