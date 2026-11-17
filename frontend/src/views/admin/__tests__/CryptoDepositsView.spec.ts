import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import CryptoDepositsView from '../CryptoDepositsView.vue'

const { listDeposits, releaseDeposit, getSettings, showError, showSuccess, showInfo } = vi.hoisted(() => ({
  listDeposits: vi.fn(),
  releaseDeposit: vi.fn(),
  getSettings: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  showInfo: vi.fn()
}))
vi.mock('@/api/client', () => ({ apiClient: {} }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<div><slot /></div>' } }))
vi.mock('@/components/layout/TablePageLayout.vue', () => ({ default: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' } }))
vi.mock('@/components/common/Pagination.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/common/Select.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/icons/Icon.vue', () => ({ default: { template: '<i />' } }))
vi.mock('@/components/common/BaseDialog.vue', () => ({ default: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' } }))
vi.mock('@/components/common/DataTable.vue', () => ({
  default: {
    props: ['data', 'columns', 'loading'],
    template: '<div><div v-for="row in data" :key="row.id" class="row"><template v-for="col in columns" :key="col.key"><slot :name="`cell-${col.key}`" :row="row" :value="row[col.key]" /></template></div></div>'
  }
}))
vi.mock('@/utils/format', () => ({ formatDateTime: (s: string) => s }))
vi.mock('@/composables/usePersistedPageSize', () => ({ getPersistedPageSize: () => 20 }))
vi.mock('@/api/admin', () => ({ adminAPI: { cryptoPay: { listDeposits, releaseDeposit, getSettings } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess, showInfo }) }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key} ${JSON.stringify(params)}` : key),
    te: (key: string) => key.startsWith('admin.cryptoDeposits.holdReasons.') || key === 'admin.cryptoDeposits.errors.CRYPTO_PAY_USER_DELETED'
  })
}))

const heldRow = {
  id: 9, user_id: 5, user_email: 'alice@example.test', user_name: 'alice', user_status: 'disabled', user_deleted: false,
  network: 'arbitrum-one', asset: 'USDT', asset_contract: '0xc', address: '0xaddress000000000000000000000001', token_account: '',
  from_address: '0xf', units: '6000000000', amount: '6000', tx_hash: '0xdeadbeef00000000000000000000000000000000000000000000000000000001',
  event_key: 'log:0', block_number: 1, block_hash: '0xb', block_time: '2026-11-17T00:00:00Z', status: 'held',
  hold_reason: 'amount_over_threshold', observed_at: '2026-11-17T00:00:00Z', finalized_at: '2026-11-17T00:01:00Z',
  credited_at: null, orphaned_at: null, credited_by: '',
  explorer_tx_url: 'https://arbiscan.io/tx/{tx}', explorer_address_url: 'https://arbiscan.io/address/{addr}'
}

function mountView() {
  return mount(CryptoDepositsView)
}

beforeEach(() => {
  vi.clearAllMocks()
  listDeposits.mockResolvedValue({ items: [structuredClone(heldRow)], total: 1, page: 1, page_size: 20, pages: 1 })
  getSettings.mockResolvedValue({ feature_configured: true, hold_threshold_amount: '5000' })
})

describe('Held stablecoin deposits', () => {
  it('lists held deposits with user, explorer link and hold reason', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(listDeposits).toHaveBeenCalledWith(1, expect.any(Number), { hold: 'held' })
    expect(wrapper.text()).toContain('alice')
    expect(wrapper.text()).toContain('alice@example.test')
    expect(wrapper.text()).toContain('6000 USDT')
    expect(wrapper.text()).toContain('admin.cryptoDeposits.holdReasons.amount_over_threshold')
    expect(wrapper.get('[data-testid="tx-link"]').attributes('href')).toBe(`https://arbiscan.io/tx/${heldRow.tx_hash}`)
    wrapper.unmount()
  })

  it('confirms with amount + user, releases once, then refreshes', async () => {
    let resolveRelease!: (v: unknown) => void
    releaseDeposit.mockImplementation(() => new Promise((r) => { resolveRelease = r }))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find((b) => b.text() === 'admin.cryptoDeposits.release')!.trigger('click')
    const dialog = wrapper.get('[data-testid="release-dialog"]')
    expect(dialog.text()).toContain('"amount":"6000"')
    expect(dialog.text()).toContain('alice <alice@example.test>')
    expect(dialog.text()).toContain('5000')
    expect(wrapper.find('[data-testid="release-inactive-warning"]').exists()).toBe(true)

    const confirm = wrapper.get('[data-testid="release-confirm"]')
    await confirm.trigger('click')
    await confirm.trigger('click') // double click while in flight
    expect(releaseDeposit).toHaveBeenCalledTimes(1)
    expect(releaseDeposit).toHaveBeenCalledWith(9)
    expect(wrapper.get('[data-testid="release-confirm"]').attributes('disabled')).toBeDefined()

    resolveRelease({ deposit_id: 9, status: 'credited', hold_reason: 'amount_over_threshold', credited: true })
    await flushPromises()
    expect(showSuccess).toHaveBeenCalledTimes(1)
    expect(listDeposits).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-testid="release-dialog"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('reports an already-credited release and mapped errors', async () => {
    releaseDeposit.mockResolvedValueOnce({ deposit_id: 9, status: 'credited', hold_reason: '', credited: false })
    const wrapper = mountView()
    await flushPromises()
    const open = () => wrapper.findAll('button').find((b) => b.text() === 'admin.cryptoDeposits.release')!.trigger('click')
    await open()
    await wrapper.get('[data-testid="release-confirm"]').trigger('click')
    await flushPromises()
    expect(showInfo).toHaveBeenCalledWith('admin.cryptoDeposits.releaseAlready')
    expect(showSuccess).not.toHaveBeenCalled()

    releaseDeposit.mockRejectedValueOnce({ status: 409, reason: 'CRYPTO_PAY_USER_DELETED', message: 'the deposit\'s user is deleted' })
    await open()
    await wrapper.get('[data-testid="release-confirm"]').trigger('click')
    await flushPromises()
    expect(showError).toHaveBeenCalledWith('admin.cryptoDeposits.errors.CRYPTO_PAY_USER_DELETED')
    wrapper.unmount()
  })
})
