import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const groupsApi = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  delete: vi.fn(),
  getAll: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  updateSortOrder: vi.fn()
}))
const appStore = vi.hoisted(() => ({ showError: vi.fn(), showSuccess: vi.fn() }))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: groupsApi,
    accessLevels: { list: vi.fn().mockResolvedValue([]) },
    accounts: { list: vi.fn().mockResolvedValue({ items: [], total: 0 }), getById: vi.fn() }
  }
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => appStore }))
vi.mock('@/stores/onboarding', () => ({ useOnboardingStore: () => ({ isCurrentStep: () => false, nextStep: vi.fn() }) }))
vi.mock('@/composables/usePersistedPageSize', () => ({ getPersistedPageSize: () => 20 }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string, p?: Record<string, unknown>) => (p ? `${key}:${JSON.stringify(p)}` : key), te: () => false }) }
})

import GroupsView from '../GroupsView.vue'

const stubs = {
  AppLayout: { template: '<div><slot /></div>' },
  TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
  DataTable: {
    props: ['data', 'columns', 'loading'],
    template:
      '<div><div v-for="row in data" :key="row.id" class="row" :data-id="row.id"><template v-for="col in columns" :key="col.key"><slot :name="`cell-${col.key}`" :row="row" :value="row[col.key]" /></template></div></div>'
  },
  BaseDialog: { props: ['show', 'title'], template: '<div v-if="show" class="dialog"><slot /><slot name="footer" /></div>' },
  ConfirmDialog: true,
  Pagination: true,
  EmptyState: true,
  Select: true,
  PlatformIcon: true,
  Icon: true,
  GroupRateMultipliersModal: true,
  ModerationPolicyEditor: true,
  ClaudeEntrypointPolicyEditor: true,
  GroupCapacityBadge: true,
  VueDraggable: true,
  RouterLink: true
}

function group(over: Record<string, unknown> = {}) {
  return {
    id: 21,
    name: 'T2-Max',
    description: '',
    platform: 'anthropic',
    rate_multiplier: 1.6,
    is_exclusive: false,
    hosted_usage_enabled: false,
    market_tier: true,
    market_plan: 'max',
    show_account_quota: false,
    status: 'active',
    required_level_id: null,
    subscription_type: 'standard',
    fallback_group_id: null,
    fallback_group_id_on_invalid_request: null,
    model_routing: null,
    ...over
  }
}

async function mountView(rows = [group(), group({ id: 22, name: 'T9-NoPlan', market_plan: null }), group({ id: 23, name: 'plain', market_tier: false, market_plan: null })]) {
  groupsApi.list.mockResolvedValue({ items: rows, total: rows.length, page: 1, page_size: 20, pages: 1 })
  const w = mount(GroupsView, { global: { stubs } })
  await flushPromises()
  return w
}

const row = (w: Awaited<ReturnType<typeof mountView>>, id: number) => w.get(`.row[data-id="${id}"]`)

async function openEdit(w: Awaited<ReturnType<typeof mountView>>, id: number) {
  const edit = row(w, id).findAll('button').find((b) => b.text().includes('common.edit'))
  await edit!.trigger('click')
  await flushPromises()
}

describe('GroupsView: 套餐 of a market tier', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    groupsApi.getUsageSummary.mockResolvedValue([])
    groupsApi.getCapacitySummary.mockResolvedValue([])
    groupsApi.getAll.mockResolvedValue([])
    groupsApi.update.mockResolvedValue(group())
    groupsApi.create.mockResolvedValue(group())
  })

  it('the list shows the plan next to the tier badge, and flags a tier without one', async () => {
    const w = await mountView()
    expect(row(w, 21).get('[data-testid=market-tier-badge]').exists()).toBe(true)
    expect(row(w, 21).get('[data-testid=market-plan-badge]').text()).toContain('"plan":"max"')
    expect(row(w, 22).get('[data-testid=market-no-plan-badge]').exists()).toBe(true)
    expect(row(w, 23).find('[data-testid=market-plan-badge]').exists()).toBe(false)
    expect(row(w, 23).find('[data-testid=market-no-plan-badge]').exists()).toBe(false)
  })

  it('edit: the field shows only with 共享档位; an unchanged plan is not sent, a changed one is', async () => {
    const w = await mountView()
    await openEdit(w, 21)
    const input = w.get('#edit-group-form [data-testid=market-plan-input]')
    expect((input.element as HTMLInputElement).value).toBe('max')
    await w.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(groupsApi.update.mock.calls[0][1]).not.toHaveProperty('market_plan')

    await openEdit(w, 21)
    await w.get('#edit-group-form [data-testid=market-plan-input]').setValue('ultra')
    await w.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(groupsApi.update.mock.calls[1][1]).toMatchObject({ market_plan: 'ultra' })
  })

  it('edit: clearing the plan sends null; turning the tier off also clears it (field hidden)', async () => {
    const w = await mountView()
    await openEdit(w, 21)
    await w.get('#edit-group-form [data-testid=market-plan-input]').setValue('')
    await w.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(groupsApi.update.mock.calls[0][1]).toMatchObject({ market_plan: null })

    await openEdit(w, 21)
    await w.get('#edit-group-form [data-testid=market-tier-checkbox]').setValue(false)
    expect(w.find('#edit-group-form [data-testid=market-plan-input]').exists()).toBe(false)
    await w.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(groupsApi.update.mock.calls[1][1]).toMatchObject({ market_tier: false, market_plan: null })
  })

  it('edit: an invalid slug blocks the save with a message', async () => {
    const w = await mountView()
    await openEdit(w, 21)
    await w.get('#edit-group-form [data-testid=market-plan-input]').setValue('Max Plan')
    expect(w.get('#edit-group-form [data-testid=market-plan-invalid]').text()).toBe('market.groupTier.planInvalid')
    await w.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(groupsApi.update).not.toHaveBeenCalled()
    expect(appStore.showError).toHaveBeenCalledWith('market.groupTier.planInvalid')
  })

  it('a server MARKET_PLAN_REQUIRES_TIER is shown in plain words', async () => {
    groupsApi.update.mockRejectedValueOnce({ status: 400, reason: 'MARKET_PLAN_REQUIRES_TIER', message: 'x' })
    const w = await mountView()
    await openEdit(w, 21)
    await w.get('#edit-group-form [data-testid=market-plan-input]').setValue('pro')
    await w.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(appStore.showError).toHaveBeenCalledWith('market.errors.MARKET_PLAN_REQUIRES_TIER')
  })

  it('create: market_plan is sent only for a tier with a plan', async () => {
    const w = await mountView()
    const createButton = w.findAll('button').find((b) => b.text().includes('admin.groups.createGroup'))
    await createButton!.trigger('click')
    await w.get('#create-group-form [data-tour=group-form-name]').setValue('T1-Pro')
    // Not a tier: no plan field, nothing sent.
    expect(w.find('#create-group-form [data-testid=market-plan-input]').exists()).toBe(false)
    await w.get('#create-group-form').trigger('submit')
    await flushPromises()
    expect(groupsApi.create.mock.calls[0][0]).not.toHaveProperty('market_plan')

    await createButton!.trigger('click')
    await w.get('#create-group-form [data-tour=group-form-name]').setValue('T1-Pro')
    await w.get('#create-group-form [data-testid=market-tier-checkbox]').setValue(true)
    // A tier with an empty plan: still nothing sent (it sells no account until a plan is set).
    await w.get('#create-group-form').trigger('submit')
    await flushPromises()
    expect(groupsApi.create.mock.calls[1][0]).not.toHaveProperty('market_plan')

    await createButton!.trigger('click')
    await w.get('#create-group-form [data-tour=group-form-name]').setValue('T1-Pro')
    await w.get('#create-group-form [data-testid=market-tier-checkbox]').setValue(true)
    await w.get('#create-group-form [data-testid=market-plan-input]').setValue('pro')
    await w.get('#create-group-form').trigger('submit')
    await flushPromises()
    expect(groupsApi.create.mock.calls[2][0]).toMatchObject({ market_tier: true, market_plan: 'pro' })
  })
})
