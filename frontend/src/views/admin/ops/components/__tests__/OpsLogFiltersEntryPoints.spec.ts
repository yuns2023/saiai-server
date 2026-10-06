import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import OpsErrorDetailModal from '../OpsErrorDetailModal.vue'

const mockAPI = vi.hoisted(() => ({ getUpstreamErrorDetail: vi.fn(), getLogFilterProposal: vi.fn() }))
const showError = vi.hoisted(() => vi.fn())
vi.mock('@/api/admin/ops', () => ({ opsAPI: mockAPI }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError }) }))
vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key })
}))

const DialogStub = defineComponent({
  props: ['show'], emits: ['close'], template: '<div v-if="show"><slot /></div>'
})
const opsDirectory = resolve(process.cwd(), 'src/views/admin/ops')

describe('Ops log filtering entry points', () => {
  beforeEach(() => vi.resetAllMocks())

  it('opens the shared proposal flow from error details without deriving a filter from bodies', async () => {
    mockAPI.getUpstreamErrorDetail.mockResolvedValue({
      id: 42, created_at: '2026-10-06T00:00:00Z', phase: 'upstream', status_code: 403,
      message: 'Upstream denied', platform: 'anthropic', group_id: 7,
      request_body: 'sensitive synthetic request', error_body: 'sensitive synthetic response'
    })
    const wrapper = mount(OpsErrorDetailModal, {
      props: { show: true, errorId: 42, errorType: 'upstream' },
      global: { stubs: { BaseDialog: DialogStub } }
    })
    await flushPromises()
    const filterButton = wrapper.findAll('button').find(button => button.text() === 'admin.ops.logFilters.filterSimilar')
    expect(filterButton).toBeDefined()
    await filterButton!.trigger('click')
    expect(wrapper.emitted('filterError')).toEqual([[42]])
    expect(mockAPI.getLogFilterProposal).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('keeps the header entry visible in fullscreen and places the shared dialog outside the fullscreen exclusion', () => {
    const header = readFileSync(resolve(opsDirectory, 'components/OpsDashboardHeader.vue'), 'utf8')
    const button = header.match(/<button\b[^>]*@click="emit\('openLogFilters'\)"[\s\S]*?<\/button>/)?.[0]
    expect(button).toContain('admin.ops.logFilters.title')
    expect(button).not.toContain('v-if')
    const dashboard = readFileSync(resolve(opsDirectory, 'OpsDashboard.vue'), 'utf8')
    const dialogPosition = dashboard.indexOf('<OpsLogFiltersDialog')
    const exclusionEnd = dashboard.indexOf('</template>', dashboard.indexOf('<template v-if="!isFullscreen">'))
    expect(dialogPosition).toBeGreaterThan(exclusionEnd)
    expect(dashboard).toContain('@filter-error="openLogFilters($event)"')
    expect(dashboard).toContain('@open-log-filters="openLogFilters()"')
    expect(dashboard).toContain("e.key === 'Escape' && isFullscreen.value && !showLogFiltersDialog.value")
    expect(dashboard).toContain("window.addEventListener('keydown', handleKeydown, true)")
    expect(dashboard).toContain("window.removeEventListener('keydown', handleKeydown, true)")
  })

  it('routes the old Advanced error filtering section to the same manager', () => {
    const settings = readFileSync(resolve(opsDirectory, 'components/OpsSettingsDialog.vue'), 'utf8')
    expect(settings).toContain('@click="emit(\'openLogFilters\')"')
    for (const field of ['ignore_count_tokens_errors', 'ignore_context_canceled', 'ignore_no_available_accounts', 'ignore_invalid_api_key_errors', 'ignore_insufficient_balance_errors']) {
      expect(settings).not.toContain(`v-model="advancedSettings.${field}"`)
    }
    expect(settings).toContain('mergeLegacyLogFilters(advancedSettings.value, await opsAPI.getAdvancedSettings())')
  })
})
