<template>
  <BaseDialog :show="show" :title="t('market.account.title')" width="wide" :close-on-escape="!busy" @close="close">
    <div v-if="loading" class="py-8 text-center text-sm text-gray-500" data-testid="market-loading">{{ t('common.loading') }}</div>
    <div v-else-if="loadError" class="space-y-3 py-4 text-sm" data-testid="market-load-error">
      <p class="text-red-600">{{ loadError }}</p>
      <button type="button" class="btn btn-secondary btn-sm" @click="load">{{ t('market.retry') }}</button>
    </div>
    <form v-else-if="view" id="market-form" class="space-y-5 text-sm" novalidate data-testid="market-form" @submit.prevent="save">
      <!-- Status -->
      <div class="rounded-md bg-gray-50 p-3 dark:bg-dark-800" data-testid="market-status">
        <div class="flex flex-wrap items-center gap-2">
          <span class="font-medium text-gray-900 dark:text-white">{{ account?.name }}</span>
          <span class="text-xs text-gray-500">#{{ view.account_id }}</span>
          <span :class="['badge', statusBadge]" data-testid="market-status-badge">{{ statusText }}</span>
          <span v-if="view.listing" class="text-xs text-gray-500" data-testid="market-version">{{ t('market.account.version', { v: view.listing.version }) }}</span>
        </div>
        <p class="mt-1 text-xs text-gray-600 dark:text-gray-300">
          {{ t('market.account.owner', { owner: ownerLabel }) }}
          <template v-if="view.listing?.status === 'listed' && selectedTierName">
            · {{ t('market.account.sellingIn', { tier: selectedTierName }) }}
          </template>
        </p>
        <p v-if="!view.market_enabled" class="mt-2 text-xs text-amber-600 dark:text-amber-400" data-testid="market-switch-off">{{ t('market.account.switchOff') }}</p>
        <p v-if="view.ineligible_reason" class="mt-2 text-xs text-amber-600 dark:text-amber-400" data-testid="market-ineligible">
          {{ t('market.account.ineligible', { reason: reasonText(view.ineligible_reason) }) }}
        </p>
      </div>

      <!-- 套餐: decides the tiers; the owner switches between 自用 and 共享 -->
      <div>
        <label class="input-label" for="market-plan">{{ t('market.account.plan') }}</label>
        <p class="input-hint mb-2">{{ t('market.account.planHint') }}</p>
        <p v-if="tiersFailed" class="mb-2 flex items-center gap-2 text-xs text-red-600" data-testid="market-tiers-failed">
          {{ t('market.account.tiersLoadFailed') }}
          <button type="button" class="btn btn-secondary btn-sm" @click="loadTiers">{{ t('market.retry') }}</button>
        </p>
        <p v-else-if="!planOptions.length" class="mb-2 text-xs text-gray-500" data-testid="market-no-plans">{{ t('market.account.noPlans') }}</p>
        <select
          id="market-plan"
          v-model="plan"
          :class="['input', tried && !validMarketPlan(plan) && 'input-error']"
          :disabled="busy"
          data-testid="market-plan"
        >
          <option value="" disabled>{{ t('market.account.choosePlan') }}</option>
          <option v-for="opt in planOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
        </select>
        <p v-if="view.listing?.status === 'revoked'" class="mt-1 text-xs text-amber-600 dark:text-amber-400" data-testid="market-regrant-hint">{{ t('market.account.regrantHint') }}</p>
      </div>

      <!-- Tiers of the plan (read-only) -->
      <div data-testid="market-derived-tiers">
        <div class="input-label">{{ t('market.account.derivedTiers') }}</div>
        <p v-if="!plan" class="text-xs text-gray-500">{{ t('market.account.derivedTiersNone') }}</p>
        <p v-else-if="!derivedTiers.length" class="text-xs text-amber-600 dark:text-amber-400" data-testid="market-derived-empty">{{ t('market.account.derivedTiersEmpty') }}</p>
        <ul v-else class="space-y-1">
          <li v-for="tier in derivedTiers" :key="tier.id" class="flex flex-wrap items-center gap-2 text-gray-900 dark:text-white" :data-testid="`market-tier-${tier.id}`">
            <span>{{ tier.label }}</span>
            <span v-if="tier.note" class="text-xs text-amber-600 dark:text-amber-400">{{ tier.note }}</span>
          </li>
        </ul>
        <p v-if="plan && plan !== savedPlan" class="mt-1 text-xs text-gray-500" data-testid="market-plan-preview">{{ t('market.account.planPreview') }}</p>
      </div>

      <p v-if="error" role="alert" class="text-sm text-red-600" data-testid="market-error">{{ error }}</p>
    </form>

    <template #footer>
      <div class="flex flex-wrap justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="busy" @click="close">{{ t('common.cancel') }}</button>
        <button
          v-if="view?.listing && view.listing.status !== 'revoked'"
          type="button"
          class="btn btn-danger"
          :disabled="busy"
          data-testid="market-revoke"
          @click="confirmRevoke = true"
        >
          {{ t('market.account.revoke') }}
        </button>
        <button type="submit" form="market-form" class="btn btn-primary" :disabled="busy || !view" data-testid="market-save">
          {{ busy ? t('common.saving') : t('common.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>

  <ConfirmDialog
    :show="confirmRevoke"
    :title="t('market.account.revokeTitle')"
    :message="t('market.account.revokeMessage')"
    :confirm-text="t('market.account.revoke')"
    danger
    @confirm="revoke"
    @cancel="confirmRevoke = false"
  />
</template>

<script setup lang="ts">
/**
 * 共享设置 of one owned account: its 套餐 (plan). The tiers it can be sold in
 * are the live market tiers of that plan; the owner switches between 自用 and
 * 共享 on the hosted page. 撤销共享 removes the permission (history kept).
 */
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { adminAPI } from '@/api/admin'
import type { AdminMarketListingView } from '@/api/admin/market'
import { useAppStore } from '@/stores/app'
import type { Account, AdminGroup } from '@/types'
import { marketErrorText, marketPlanOptions, planTiers, validMarketPlan } from '@/utils/market'

const props = defineProps<{ show: boolean; account: Account | null }>()
const emit = defineEmits<{ close: []; updated: [view: AdminMarketListingView] }>()
const { t, te } = useI18n()
const appStore = useAppStore()

const view = ref<AdminMarketListingView | null>(null)
const loading = ref(false)
const loadError = ref('')
const busy = ref(false)
const error = ref('')
const tried = ref(false)
const confirmRevoke = ref(false)
const plan = ref('')
const groups = ref<AdminGroup[]>([])
const tiersFailed = ref(false)
let seq = 0

/** The owner as the listing endpoint reports it; the row's email only when the row names the same owner. */
const ownerLabel = computed(() => {
  const a = props.account
  const id = view.value?.owner_user_id ?? null
  if (!id) return t('market.account.noOwner')
  return a?.owner_email && a.owner_user_id === id ? `${a.owner_email} (#${id})` : `#${id}`
})

const statusText = computed(() => {
  const s = view.value?.listing?.status ?? 'not_configured'
  return te(`market.status.${s}`) ? t(`market.status.${s}`) : s
})
const statusBadge = computed(() => {
  const s = view.value?.listing?.status
  if (s === 'listed') return 'badge-success'
  if (s === 'revoked') return 'badge-danger'
  if (s === 'unlisted') return 'badge-primary'
  return 'badge-gray'
})

const selectedTierName = computed(() => {
  const id = view.value?.listing?.selected_group_id
  if (id == null) return ''
  const tier = view.value?.allowed_tiers.find((x) => x.group_id === id)
  return tier ? `${tier.name} ×${tier.rate_multiplier}` : `#${id}`
})

/** The plan stored on the listing ('' when none). */
const savedPlan = computed(() => view.value?.listing?.plan ?? '')
const platform = computed(() => props.account?.platform)

/** Plans of the platform's live tiers, plus the stored one if no live tier carries it any more. */
const planOptions = computed(() => {
  const opts = marketPlanOptions(groups.value, platform.value).map((p) => ({ value: p, label: p }))
  if (savedPlan.value && !opts.some((o) => o.value === savedPlan.value)) {
    // "no live tier" only when the group list was read; after a failed read it is just unknown.
    opts.push({ value: savedPlan.value, label: tiersFailed.value ? savedPlan.value : `${savedPlan.value}（${t('market.account.planNoTier')}）` })
  }
  return opts
})

/**
 * Tiers of the chosen plan: the server's own list for the stored plan (with
 * its notes), a preview from the group list for a plan not saved yet.
 */
const derivedTiers = computed(() => {
  if (!plan.value) return []
  if (plan.value === savedPlan.value && view.value) {
    return view.value.allowed_tiers.map((x) => ({
      id: x.group_id,
      label: `${x.name} ×${x.rate_multiplier}`,
      note: !x.available ? t('market.account.tierInactive') : x.admin_member ? t('market.account.tierAdminMember') : ''
    }))
  }
  return planTiers(groups.value, platform.value)
    .filter((g) => g.market_plan === plan.value)
    .map((g) => ({ id: g.id, label: `${g.name} ×${g.rate_multiplier}`, note: '' }))
})

function reasonText(code: string): string {
  return te(`market.reasons.${code}`) ? t(`market.reasons.${code}`) : t('market.reasons.other', { code })
}

/** Whether the owner or the listing (version, status, created/removed) changed since the form was loaded. */
function changedSince(loaded: AdminMarketListingView | null, latest: AdminMarketListingView): boolean {
  if ((loaded?.owner_user_id ?? null) !== (latest.owner_user_id ?? null)) return true
  const a = loaded?.listing ?? null
  const b = latest.listing
  if (!a || !b) return a !== b
  return a.version !== b.version || a.status !== b.status
}

function applyView(v: AdminMarketListingView): void {
  view.value = v
  plan.value = v.listing?.plan ?? ''
}

async function loadTiers(): Promise<void> {
  tiersFailed.value = false
  try {
    groups.value = (await adminAPI.groups.getAll()) ?? []
  } catch {
    groups.value = []
    tiersFailed.value = true
  }
}

async function load(): Promise<void> {
  const a = props.account
  if (!a) return
  const mine = ++seq
  loading.value = true
  loadError.value = ''
  error.value = ''
  tried.value = false
  view.value = null
  try {
    const [v] = await Promise.all([adminAPI.market.getListing(a.id), loadTiers()])
    if (mine !== seq) return
    applyView(v)
  } catch (e) {
    if (mine !== seq) return
    loadError.value = marketErrorText(e, t, t('market.account.loadFailed'))
  } finally {
    if (mine === seq) loading.value = false
  }
}

async function save(): Promise<void> {
  const a = props.account
  if (!a || !view.value || busy.value) return
  tried.value = true
  if (!validMarketPlan(plan.value)) {
    error.value = t('market.account.planRequired')
    return
  }
  busy.value = true
  error.value = ''
  try {
    // A form loaded before someone else changed the listing (e.g. revoked it) must not write it back.
    // Read it again right before saving; the server has no version check (remaining window accepted).
    const latest = await adminAPI.market.getListing(a.id)
    if (changedSince(view.value, latest)) {
      applyView(latest)
      error.value = t('market.account.staleReload')
      return
    }
    const v = await adminAPI.market.putListing(a.id, { plan: plan.value })
    applyView(v)
    appStore.showSuccess(t('market.account.saved'))
    emit('updated', v)
  } catch (e) {
    error.value = marketErrorText(e, t, t('market.account.saveFailed'))
  } finally {
    busy.value = false
  }
}
async function revoke(): Promise<void> {
  confirmRevoke.value = false
  const a = props.account
  if (!a || busy.value) return
  busy.value = true
  error.value = ''
  try {
    const v = await adminAPI.market.revokeListing(a.id)
    applyView(v)
    appStore.showSuccess(t('market.account.revoked'))
    emit('updated', v)
  } catch (e) {
    error.value = marketErrorText(e, t, t('market.account.saveFailed'))
  } finally {
    busy.value = false
  }
}

function close(): void {
  if (!busy.value) emit('close')
}

watch(
  () => [props.show, props.account?.id] as const,
  ([show]) => {
    if (show) void load()
    else seq++
  },
  { immediate: true }
)
</script>
