import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const market = vi.hoisted(() => ({ owners: vi.fn(), entries: vi.fn(), reverse: vi.fn() }))
const appStore = vi.hoisted(() => ({ showError: vi.fn(), showSuccess: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { market } }))
vi.mock('@/api/client', () => ({ apiClient: {} }))
vi.mock('@/stores/app', () => ({ useAppStore: () => appStore }))
vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return { ...actual, useI18n: () => ({ t: (key: string, p?: Record<string, unknown>) => (p ? `${key}:${JSON.stringify(p)}` : key), te: () => false }) }
})

import MarketEarningsView from '../MarketEarningsView.vue'

const stubs = {
  AppLayout: { template: '<div><slot /></div>' },
  Icon: true,
  Pagination: true,
  DataTable: {
    props: ['columns', 'data', 'loading'],
    template: `<table><tbody>
      <tr v-for="row in data" :key="row.id ?? row.owner_user_id" data-testid="row">
        <td v-for="c in columns" :key="c.key"><slot :name="'cell-' + c.key" :row="row" :value="row[c.key]">{{ row[c.key] }}</slot></td>
      </tr>
      <tr v-if="!data.length"><td><slot name="empty" /></td></tr>
    </tbody></table>`
  },
  BaseDialog: { props: ['show'], template: '<div v-if="show" data-testid="dialog"><slot /><slot name="footer" /></div>' },
  ConfirmDialog: { props: ['show', 'message'], emits: ['confirm', 'cancel'], template: '<div v-if="show" data-testid="confirm">{{ message }}<button data-testid="confirm-yes" @click="$emit(\'confirm\')" /></div>' }
}

const AS_OF = '2026-11-19T04:00:00.123456Z'
const owner = { owner_user_id: 7, email: 'o@example.com', frozen: '1.2', available: '3.5', converted_total: '2', reversed_total: '0.5', accrued_total: '7.2' }
const entry = (id: number, over: Record<string, unknown> = {}) => ({
  id,
  kind: 'accrue',
  amount: '0.8',
  status: 'frozen',
  available_at: '2026-11-26T00:00:00Z',
  created_at: '2026-11-19T00:00:00Z',
  account_id: 11,
  model: 'claude-sonnet-5',
  owner_user_id: 7,
  settlement_event_id: 100 + id,
  reverses_entry_id: null,
  convert_request_id: null,
  admin_user_id: null,
  note: null,
  ...over
})

async function mountView() {
  const w = mount(MarketEarningsView, { global: { stubs, mocks: { $t: (k: string) => k } } })
  await flushPromises()
  return w
}

describe('MarketEarningsView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    market.owners.mockResolvedValue({ items: [owner], total: 1, page: 1, page_size: 20, as_of: AS_OF })
    market.entries.mockResolvedValue({ items: [entry(9)], next_cursor: 9 })
  })

  it('lists owners and the ledger; entries page by keyset and can be scoped to an owner', async () => {
    const w = await mountView()
    expect(w.text()).toContain('o@example.com')
    expect(w.text()).toContain('$3.50')
    expect(w.text()).toContain('$0.50')
    expect(market.entries).toHaveBeenLastCalledWith({ ownerUserId: undefined, cursor: null, limit: 50 })
    market.entries.mockResolvedValueOnce({ items: [entry(8)], next_cursor: null })
    await w.get('[data-testid=entries-more]').trigger('click')
    await flushPromises()
    expect(market.entries).toHaveBeenLastCalledWith({ ownerUserId: undefined, cursor: 9, limit: 50 })
    expect(w.find('[data-testid=entries-more]').exists()).toBe(false)

    await w.get('[data-testid=entries-of-7]').trigger('click')
    await flushPromises()
    expect(market.entries).toHaveBeenLastCalledWith({ ownerUserId: 7, cursor: null, limit: 50 })
  })

  it('an empty site (market never used) shows empty states, not errors', async () => {
    market.owners.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, as_of: 'x' })
    market.entries.mockResolvedValue({ items: [], next_cursor: null })
    const w = await mountView()
    expect(w.find('[data-testid=owners-empty]').exists()).toBe(true)
    expect(w.find('[data-testid=entries-empty]').exists()).toBe(true)
    expect(w.find('[data-testid=owners-error]').exists()).toBe(false)
  })

  it('reverse requires an id and a note, then a confirmation; sends only the chosen scope', async () => {
    const w = await mountView()
    await w.get('[data-testid=reverse-of-7]').trigger('click')
    expect((w.get('[data-testid=reverse-owner-id]').element as HTMLInputElement).value).toBe('7')
    await w.get('[data-testid=reverse-dialog]').trigger('submit')
    expect(w.get('[data-testid=reverse-error]').text()).toBe('market.earnings.noteRequired')
    expect(w.find('[data-testid=confirm]').exists()).toBe(false)

    await w.get('[data-testid=reverse-note]').setValue('   ')
    await w.get('[data-testid=reverse-dialog]').trigger('submit')
    expect(w.find('[data-testid=confirm]').exists()).toBe(false)

    await w.get('[data-testid=reverse-note]').setValue('chargeback on buyer side')
    await w.get('[data-testid=reverse-dialog]').trigger('submit')
    expect(w.get('[data-testid=confirm]').text()).toContain('reverseConfirm_owner')
    expect(market.reverse).not.toHaveBeenCalled()

    market.reverse.mockResolvedValue({ cutoff: AS_OF, as_of: '2026-11-19T05:00:00Z', reversed_count: 2, reversed_amount: '1.2', owners: [{ owner_user_id: 7, count: 2, amount: '1.2' }] })
    await w.get('[data-testid=confirm-yes]').trigger('click')
    await flushPromises()
    // The cutoff is the server time of the overview on screen.
    expect(market.reverse).toHaveBeenCalledWith({ owner_user_id: 7, as_of: AS_OF, note: 'chargeback on buyer side' })
    expect(w.get('[data-testid=reverse-result]').text()).toContain('"count":2')
    // Owners and ledger are read again.
    expect(market.owners).toHaveBeenCalledTimes(2)
  })

  it('reverse by account: needs a positive account id; the confirmation warns about several past owners', async () => {
    const w = await mountView()
    await w.get('[data-testid=reverse-open]').trigger('click')
    await w.get('[data-testid=scope-account]').setValue(true)
    expect(w.find('[data-testid=reverse-owner-id]').exists()).toBe(false)
    await w.get('[data-testid=reverse-account-id]').setValue('abc')
    await w.get('[data-testid=reverse-note]').setValue('bad account')
    await w.get('[data-testid=reverse-dialog]').trigger('submit')
    expect(w.get('[data-testid=reverse-error]').text()).toBe('market.earnings.idRequired')
    await w.get('[data-testid=reverse-account-id]').setValue('11')
    await w.get('[data-testid=reverse-dialog]').trigger('submit')
    expect(w.get('[data-testid=confirm]').text()).toContain('reverseConfirm_account')
    market.reverse.mockResolvedValue({ cutoff: AS_OF, as_of: null, reversed_count: 0, reversed_amount: '0', owners: [] })
    await w.get('[data-testid=confirm-yes]').trigger('click')
    await flushPromises()
    expect(market.reverse).toHaveBeenCalledWith({ account_id: 11, as_of: AS_OF, note: 'bad account' })
  })

  it('switching the scope clears the IDs; owner + account sends both', async () => {
    const w = await mountView()
    await w.get('[data-testid=reverse-of-7]').trigger('click')
    await w.get('[data-testid=scope-account]').setValue(true)
    await w.get('[data-testid=scope-account]').trigger('change')
    await w.get('[data-testid=reverse-account-id]').setValue('11')
    await w.get('[data-testid=scope-both]').setValue(true)
    await w.get('[data-testid=scope-both]').trigger('change')
    expect((w.get('[data-testid=reverse-owner-id]').element as HTMLInputElement).value).toBe('')
    expect((w.get('[data-testid=reverse-account-id]').element as HTMLInputElement).value).toBe('')
    await w.get('[data-testid=reverse-owner-id]').setValue('7')
    await w.get('[data-testid=reverse-note]').setValue('owner on that account')
    await w.get('[data-testid=reverse-dialog]').trigger('submit')
    expect(w.get('[data-testid=reverse-error]').text()).toBe('market.earnings.idRequired')
    await w.get('[data-testid=reverse-account-id]').setValue('11')
    await w.get('[data-testid=reverse-dialog]').trigger('submit')
    expect(w.get('[data-testid=confirm]').text()).toContain('reverseConfirm_both')
    market.reverse.mockResolvedValue({ cutoff: AS_OF, as_of: AS_OF, reversed_count: 1, reversed_amount: '0.5', owners: [] })
    await w.get('[data-testid=confirm-yes]').trigger('click')
    await flushPromises()
    expect(market.reverse).toHaveBeenCalledWith({ owner_user_id: 7, account_id: 11, as_of: AS_OF, note: 'owner on that account' })
  })

  it('without an overview (read failed) there is no cutoff, so reversing is disabled; 404 explains instead of erroring', async () => {
    market.owners.mockRejectedValue({ status: 500, message: 'boom' })
    const w = await mountView()
    expect(w.get('[data-testid=reverse-open]').attributes('disabled')).toBeDefined()
    expect(w.find('[data-testid=owners-error]').exists()).toBe(true)
    market.owners.mockRejectedValue({ status: 404, message: 'not found' })
    market.entries.mockRejectedValue({ status: 404, message: 'not found' })
    const w2 = await mountView()
    expect(w2.find('[data-testid=earnings-not-available]').exists()).toBe(true)
    expect(w2.find('[data-testid=owners-error]').exists()).toBe(false)
    expect(w2.find('[data-testid=entries-error]').exists()).toBe(false)
  })

  it('a failed first-page refresh is retried as a first page, not appended', async () => {
    market.entries.mockResolvedValueOnce({ items: [entry(9)], next_cursor: null })
    const w = await mountView()
    market.entries.mockRejectedValueOnce({ status: 500, message: 'boom' })
    await w.get('[data-testid=refresh]').trigger('click')
    await flushPromises()
    market.entries.mockResolvedValueOnce({ items: [entry(10), entry(9)], next_cursor: null })
    await w.get('[data-testid=entries-more]').trigger('click')
    await flushPromises()
    expect(market.entries).toHaveBeenLastCalledWith({ ownerUserId: undefined, cursor: null, limit: 50 })
    expect(w.findAll('[data-testid=row]').length).toBe(3) // 1 owner row + 2 entries, no duplicate
  })

  it('server errors are shown in the dialog', async () => {
    const w = await mountView()
    await w.get('[data-testid=reverse-of-7]').trigger('click')
    await w.get('[data-testid=reverse-note]').setValue('x'.repeat(10))
    await w.get('[data-testid=reverse-dialog]').trigger('submit')
    market.reverse.mockRejectedValue({ status: 400, reason: 'MARKET_EARNING_NOTE_TOO_LONG', message: 'note is too long' })
    await w.get('[data-testid=confirm-yes]').trigger('click')
    await flushPromises()
    expect(w.get('[data-testid=reverse-error]').text()).toBe('market.errors.MARKET_EARNING_NOTE_TOO_LONG')
  })
})
