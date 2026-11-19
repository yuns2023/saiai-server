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

      <!-- Allowed tiers -->
      <fieldset>
        <legend class="input-label">{{ t('market.account.allowedTiers') }}</legend>
        <p class="input-hint mb-2">{{ t('market.account.allowedTiersHint') }}</p>
        <p v-if="tiersFailed" class="mb-2 flex items-center gap-2 text-xs text-red-600" data-testid="market-tiers-failed">
          {{ t('market.account.tiersLoadFailed') }}
          <button type="button" class="btn btn-secondary btn-sm" @click="loadTiers">{{ t('market.retry') }}</button>
        </p>
        <p v-else-if="!tierOptions.length" class="text-xs text-gray-500" data-testid="market-no-tiers">{{ t('market.account.noTiers') }}</p>
        <div v-else class="grid gap-2 sm:grid-cols-2">
          <label
            v-for="opt in tierOptions"
            :key="opt.id"
            class="flex items-center gap-2 rounded-md border border-gray-200 px-3 py-2 dark:border-dark-600"
            :data-testid="`market-allow-${opt.id}`"
          >
            <input v-model="allowed" type="checkbox" :value="opt.id" :disabled="busy" />
            <span class="flex-1 text-gray-900 dark:text-white">{{ opt.label }}</span>
            <span v-if="opt.note" class="text-xs text-amber-600 dark:text-amber-400">{{ opt.note }}</span>
          </label>
        </div>
      </fieldset>

      <!-- Protection -->
      <fieldset>
        <legend class="input-label">{{ t('market.account.protection') }}</legend>
        <p class="input-hint mb-2">{{ t('market.account.protectionHint') }}</p>
        <div class="grid gap-3 sm:grid-cols-3">
          <div v-for="key in PROTECTION_KEYS" :key="key">
            <label class="mb-1 block text-xs font-medium text-gray-700 dark:text-gray-300" :for="`market-${key}`">{{ t(`market.protection.${key}`) }}</label>
            <input
              :id="`market-${key}`"
              v-model.number="protection[key]"
              type="number"
              step="1"
              :min="MARKET_PROTECTION_BOUNDS[key].min"
              :max="upperBound(key)"
              :class="['input', tried && !protectionValueOk(key, protection[key]) && 'input-error']"
              :disabled="busy"
              :data-testid="`market-${key}`"
            />
            <p class="mt-1 text-xs text-gray-500">{{ boundHint(key) }}</p>
          </div>
        </div>
      </fieldset>

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
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { adminAPI } from '@/api/admin'
import { MAX_MARKET_ALLOWED_GROUPS, type AdminMarketListingView, type MarketProtection } from '@/api/admin/market'
import { useAppStore } from '@/stores/app'
import type { Account, AdminGroup } from '@/types'
import { firstBadProtection, MARKET_PROTECTION_BOUNDS, marketErrorText, PROTECTION_KEYS, protectionValueOk } from '@/utils/market'

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
const allowed = ref<number[]>([])
const protection = reactive({} as Record<keyof MarketProtection, number>)
const tierGroups = ref<AdminGroup[]>([])
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

/**
 * Checkbox options: the platform's live market tiers, plus any tier still
 * allowed on the listing that no longer shows up (deleted / demoted), so the
 * admin can see and remove it.
 */
const tierOptions = computed(() => {
  const opts: Array<{ id: number; label: string; note: string }> = []
  const listed = view.value?.allowed_tiers ?? []
  for (const g of tierGroups.value) {
    const fromView = listed.find((x) => x.group_id === g.id)
    opts.push({
      id: g.id,
      label: `${g.name} ×${g.rate_multiplier}`,
      note: g.status !== 'active' ? t('market.account.tierInactive') : fromView?.admin_member ? t('market.account.tierAdminMember') : ''
    })
  }
  for (const id of allowed.value) {
    if (opts.some((o) => o.id === id)) continue
    const tier = listed.find((x) => x.group_id === id)
    opts.push({ id, label: tier ? `${tier.name} ×${tier.rate_multiplier}` : `#${id}`, note: t('market.account.tierGone') })
  }
  return opts
})

function reasonText(code: string): string {
  return te(`market.reasons.${code}`) ? t(`market.reasons.${code}`) : t('market.reasons.other', { code })
}

/** Per-account upper bound where the account config is known (C − 1, device limit − 1); the static bound otherwise. */
function upperBound(key: keyof MarketProtection): number {
  const max = MARKET_PROTECTION_BOUNDS[key].max
  const a = props.account
  if (key === 'reserved_concurrency' && a && a.concurrency > 0) return Math.min(max, a.concurrency - 1)
  const devices = a?.claude_oauth_carpool_device_limit
  if (key === 'reserved_devices' && typeof devices === 'number' && devices > 0) return Math.min(max, devices - 1)
  return max
}

function boundHint(key: keyof MarketProtection): string {
  const min = MARKET_PROTECTION_BOUNDS[key].min
  const max = upperBound(key)
  const base = t('market.account.range', { min, max })
  if (key === 'reserved_concurrency' && max < MARKET_PROTECTION_BOUNDS[key].max) return `${base} · ${t('market.account.leaveOneSlot')}`
  if (key === 'reserved_devices' && max < MARKET_PROTECTION_BOUNDS[key].max) return `${base} · ${t('market.account.leaveOneDevice')}`
  return base
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
  allowed.value = [...(v.listing?.allowed_group_ids ?? [])]
  const p = v.listing?.protection ?? v.default_protection
  for (const key of PROTECTION_KEYS) protection[key] = p[key]
}

async function loadTiers(): Promise<void> {
  const platform = props.account?.platform
  tiersFailed.value = false
  try {
    const groups = await adminAPI.groups.getAll()
    tierGroups.value = (groups ?? []).filter((g) => g.market_tier === true && (!platform || g.platform === platform))
  } catch {
    tierGroups.value = []
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
  // Static bounds first, then the per-account ones shown next to the fields (C − 1, device limit − 1).
  const bad = firstBadProtection(protection) ?? PROTECTION_KEYS.find((k) => protection[k] > upperBound(k)) ?? null
  if (bad) {
    error.value = t('market.account.invalidField', { field: t(`market.protection.${bad}`) })
    return
  }
  if (allowed.value.length > MAX_MARKET_ALLOWED_GROUPS) {
    error.value = t('market.account.tooManyTiers', { max: MAX_MARKET_ALLOWED_GROUPS })
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
    const v = await adminAPI.market.putListing(a.id, { allowed_group_ids: [...allowed.value].sort((x, y) => x - y), ...protection })
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
