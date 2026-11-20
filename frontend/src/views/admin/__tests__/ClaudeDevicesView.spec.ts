import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const claudeDevicePlatform = vi.hoisted(() => ({ listFlagged: vi.fn() }))
const appStore = vi.hoisted(() => ({ showError: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { claudeDevicePlatform } }))
vi.mock('@/api/client', () => ({ apiClient: {} }))
vi.mock('@/stores/app', () => ({ useAppStore: () => appStore }))
vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return { ...actual, useI18n: () => ({ t: (key: string, p?: Record<string, unknown>) => (p ? `${key}:${JSON.stringify(p)}` : key), te: () => false }) }
})

import ClaudeDevicesView from '../ClaudeDevicesView.vue'

const stubs = {
  AppLayout: { template: '<div><slot /></div>' },
  TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
  Icon: true,
  DataTable: {
    props: ['columns', 'data', 'loading'],
    template: `<table><tbody>
      <tr v-for="row in data" :key="row.display_prefix" data-testid="row">
        <td v-for="c in columns" :key="c.key"><slot :name="'cell-' + c.key" :row="row" :value="row[c.key]">{{ row[c.key] }}</slot></td>
      </tr>
      <tr v-if="!data.length"><td><slot name="empty" /></td></tr>
    </tbody></table>`
  }
}

const AT = '2026-11-19T04:00:00Z'
const device = (prefix: string, over: Record<string, unknown> = {}) => ({
  display_prefix: prefix,
  last_matched_at: AT,
  reasons: [{ reason: 'cross_family', last_matched_at: AT, evidence: 'macos+windows' }],
  platforms: [{ family: 'macos', arch: 'arm64', first_seen_at: AT, last_seen_at: AT }],
  versions: [{ family: 'macos', major: '15', first_seen_at: AT, last_seen_at: AT }],
  users: [{ user_id: 3, username: 'alice', email: 'a@example.com', api_key_id: 9, last_seen_at: AT }],
  users_truncated: false,
  ...over
})

async function mountView() {
  const w = mount(ClaudeDevicesView, { global: { stubs } })
  await flushPromises()
  return w
}

describe('ClaudeDevicesView', () => {
  beforeEach(() => {
    claudeDevicePlatform.listFlagged.mockReset()
    appStore.showError.mockReset()
  })

  it('loads the default reasons and renders a device', async () => {
    claudeDevicePlatform.listFlagged.mockResolvedValue({ items: [device('abcd1234', { users_truncated: true })], next_cursor: '', reasons: [], stats: {} })
    const w = await mountView()
    expect(claudeDevicePlatform.listFlagged).toHaveBeenCalledWith({ reasons: ['cross_family', 'version_alternation', 'cross_user'], cursor: undefined, limit: 50 })
    expect(w.findAll('[data-testid="row"]')).toHaveLength(1)
    expect(w.text()).toContain('abcd1234')
    expect(w.text()).toContain('alice')
    expect(w.text()).toContain('macos+windows')
    expect(w.find('[data-testid="cdp-users-truncated"]').exists()).toBe(true)
    expect(w.find('[data-testid="cdp-notice"]').exists()).toBe(true)
    expect((w.find('[data-testid="cdp-reason-arch_only"]').element as HTMLInputElement).checked).toBe(false)
    expect((w.find('[data-testid="cdp-reason-cross_user"]').element as HTMLInputElement).checked).toBe(true)
    expect(w.find('[data-testid="cdp-load-more"]').exists()).toBe(false)
  })

  it('adds optional reasons and pages with the cursor', async () => {
    claudeDevicePlatform.listFlagged
      .mockResolvedValueOnce({ items: [device('aaaaaaaa')], next_cursor: '', reasons: [], stats: {} })
      .mockResolvedValueOnce({ items: [device('bbbbbbbb')], next_cursor: '100:ff', reasons: [], stats: {} })
      .mockResolvedValueOnce({ items: [device('cccccccc')], next_cursor: '', reasons: [], stats: {} })
    const w = await mountView()
    await w.find('[data-testid="cdp-reason-arch_only"]').trigger('change')
    await flushPromises()
    expect(claudeDevicePlatform.listFlagged).toHaveBeenLastCalledWith({
      reasons: ['cross_family', 'version_alternation', 'cross_user', 'arch_only'],
      cursor: undefined,
      limit: 50
    })
    expect(w.text()).not.toContain('aaaaaaaa')
    await w.find('[data-testid="cdp-load-more"]').trigger('click')
    await flushPromises()
    expect(claudeDevicePlatform.listFlagged).toHaveBeenLastCalledWith({
      reasons: ['cross_family', 'version_alternation', 'cross_user', 'arch_only'],
      cursor: '100:ff',
      limit: 50
    })
    expect(w.findAll('[data-testid="row"]')).toHaveLength(2)
    expect(w.find('[data-testid="cdp-load-more"]').exists()).toBe(false)
  })

  it('shows an error when loading fails', async () => {
    claudeDevicePlatform.listFlagged.mockRejectedValue(new Error('boom'))
    await mountView()
    expect(appStore.showError).toHaveBeenCalledWith('admin.claudeDevicePlatform.failedToLoad: boom')
  })
})
