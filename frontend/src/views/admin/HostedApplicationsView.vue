<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="flex flex-wrap items-center gap-3">
          <p class="flex-1 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.hostedApplications.description') }}</p>
          <Select v-model="statusFilter" :options="statusOptions" class="w-36" data-testid="status-filter" @change="reload" />
          <button class="btn btn-secondary" :disabled="loading" :title="t('common.refresh')" @click="load">
            <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
          </button>
        </div>
        <p
          v-if="!switchOn"
          class="mt-3 rounded-md bg-amber-50 p-2 text-sm text-amber-700 dark:bg-amber-900/20 dark:text-amber-300"
          data-testid="switch-off"
        >
          {{ t('admin.hostedApplications.switchOff') }}
        </p>
        <p
          v-if="contextFailed"
          class="mt-3 flex items-center gap-3 rounded-md bg-red-50 p-2 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300"
          data-testid="context-failed"
        >
          {{ t('admin.hostedApplications.settingsLoadFailed') }}
          <button type="button" class="btn btn-secondary btn-sm" @click="loadApproveContext">{{ t('admin.hostedApplications.retry') }}</button>
        </p>
      </template>

      <template #table>
        <DataTable :columns="columns" :data="items" :loading="loading">
          <template #cell-applicant="{ row }">
            <div class="text-sm">
              <div class="font-medium text-gray-900 dark:text-white">{{ row.user_email || `#${row.user_id}` }}</div>
              <div class="text-xs text-gray-400">#{{ row.user_id }}</div>
            </div>
          </template>

          <template #cell-display_name="{ row }">
            <div class="max-w-[14rem] whitespace-normal text-sm [overflow-wrap:anywhere]">
              <div class="font-medium text-gray-900 dark:text-white">{{ row.display_name }}</div>
              <div v-if="row.upstream_email" class="text-xs text-gray-500 dark:text-dark-400">{{ row.upstream_email }}</div>
            </div>
          </template>

          <template #cell-proxy="{ row }">
            <div v-if="row.proxy_display" class="flex max-w-[14rem] flex-col items-start gap-1 whitespace-normal text-sm [overflow-wrap:anywhere]">
              <span class="font-mono text-xs text-gray-700 dark:text-gray-300">{{ row.proxy_display }}</span>
              <span v-if="row.has_proxy_auth" class="badge badge-gray">{{ t('admin.hostedApplications.proxyAuth') }}</span>
            </div>
            <span v-else class="text-xs text-gray-400">{{ t('admin.hostedApplications.noProxy') }}</span>
          </template>

          <template #cell-created_at="{ row }">
            <span class="text-sm text-gray-500 dark:text-dark-400">{{ formatDateTime(row.created_at) }}</span>
          </template>

          <template #cell-status="{ row }">
            <div class="min-w-[10rem] whitespace-normal">
            <span :class="['badge', statusBadge(row.status)]" data-testid="status-badge">{{ statusLabel(row.status) }}</span>
            <div v-if="row.reviewed_at" class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ formatDateTime(row.reviewed_at) }}</div>
            <div
              v-if="row.status === 'rejected' && row.reject_reason"
              class="mt-1 max-w-[10rem] text-xs text-gray-600 dark:text-gray-300"
              :title="row.reject_reason"
            >
              {{ t('admin.hostedApplications.rejectReasonShort', { reason: row.reject_reason }) }}
            </div>
            <router-link
              v-if="row.account_id"
              to="/admin/accounts"
              class="mt-1 block text-xs text-primary-600 hover:underline dark:text-primary-400"
              data-testid="account-link"
            >
              {{ t('admin.hostedApplications.accountLink', { id: row.account_id }) }}
            </router-link>
            </div>
          </template>

          <template #cell-actions="{ row }">
            <div v-if="row.status === 'pending'" class="flex gap-2">
              <button class="btn btn-primary btn-sm" :disabled="!switchOn || !contextReady || busy" :title="switchOn ? '' : t('admin.hostedApplications.switchOff')" data-testid="approve" @click="openApprove(row)">
                {{ t('admin.hostedApplications.approve') }}
              </button>
              <button class="btn btn-secondary btn-sm" :disabled="busy" data-testid="reject" @click="openReject(row)">
                {{ t('admin.hostedApplications.reject') }}
              </button>
            </div>
          </template>

          <template #empty>
            <div class="py-10 text-center text-sm text-gray-500">{{ t('admin.hostedApplications.empty') }}</div>
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="pagination.total > 0"
          :page="pagination.page"
          :total="pagination.total"
          :page-size="pagination.page_size"
          @update:page="handlePageChange"
          @update:pageSize="handlePageSizeChange"
        />
      </template>
    </TablePageLayout>

    <!-- Approve -->
    <BaseDialog :show="!!approveTarget || !!approveResult" :title="t('admin.hostedApplications.approveTitle')" width="normal" @close="closeApprove">
      <div v-if="approveResult" class="space-y-3 text-sm" data-testid="approve-result">
        <p class="text-gray-700 dark:text-gray-300">
          {{ t('admin.hostedApplications.approved', { account: approveResult.account_id, group: approveResult.group_id }) }}
        </p>
        <router-link to="/admin/accounts" class="text-primary-600 hover:underline dark:text-primary-400" data-testid="approve-result-link" @click="closeApprove">
          {{ t('admin.hostedApplications.accountLink', { id: approveResult.account_id }) }}
        </router-link>
        <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.hostedApplications.approvedHint') }}</p>
      </div>
      <form v-else-if="approveTarget" id="approve-form" class="space-y-4 text-sm" data-testid="approve-dialog" @submit.prevent="confirmApprove">
        <div class="rounded-md bg-gray-50 p-3 dark:bg-dark-800">
          <div class="font-medium text-gray-900 dark:text-white">{{ approveTarget.display_name }}</div>
          <div class="text-xs text-gray-500 dark:text-dark-400">
            {{ approveTarget.upstream_email }} · {{ t('admin.hostedApplications.applicant') }} {{ approveTarget.user_email || `#${approveTarget.user_id}` }}
          </div>
        </div>

        <fieldset>
          <legend class="input-label">{{ t('admin.hostedApplications.proxyMode') }}</legend>
          <p class="input-hint mb-2">{{ t('admin.hostedApplications.proxyModeHint') }}</p>
          <label class="flex items-start gap-2 py-1">
            <input v-model="approveForm.proxy_mode" type="radio" value="existing" class="mt-1" data-testid="mode-existing" />
            <span class="flex-1">
              <span class="text-gray-900 dark:text-white">{{ t('admin.hostedApplications.modeExisting') }}</span>
              <Select
                v-if="approveForm.proxy_mode === 'existing'"
                v-model="approveForm.proxy_id"
                :options="proxyOptions"
                :placeholder="t('admin.hostedApplications.chooseProxy')"
                :error="tried && problems.includes('proxy')"
                searchable
                class="mt-2"
                data-testid="proxy-select"
              />
              <span v-if="approveForm.proxy_mode === 'existing' && !proxies.length" class="input-hint block">{{ t('admin.hostedApplications.noProxies') }}</span>
            </span>
          </label>
          <label class="flex items-start gap-2 py-1" :class="!hasSubmittedProxy(approveTarget) && 'opacity-60'">
            <input
              v-model="approveForm.proxy_mode"
              type="radio"
              value="submitted"
              class="mt-1"
              :disabled="!hasSubmittedProxy(approveTarget)"
              data-testid="mode-submitted"
            />
            <span class="flex-1">
              <span class="text-gray-900 dark:text-white">{{ t('admin.hostedApplications.modeSubmitted') }}</span>
              <span v-if="hasSubmittedProxy(approveTarget)" class="ml-1 font-mono text-xs text-gray-600 dark:text-gray-300">
                {{ approveTarget.proxy_display }}<template v-if="approveTarget.has_proxy_auth"> ({{ t('admin.hostedApplications.proxyAuth') }})</template>
              </span>
              <span v-else class="input-hint block">{{ t('admin.hostedApplications.noSubmittedProxy') }}</span>
            </span>
          </label>
          <p v-if="tried && problems.includes('proxy')" class="mt-1 text-xs text-red-600 dark:text-red-400" data-testid="proxy-error">
            {{ t('admin.hostedApplications.proxyRequired') }}
          </p>
        </fieldset>

        <div class="grid grid-cols-2 gap-3 sm:grid-cols-3">
          <div v-for="field in numberFields" :key="field.key">
            <label class="input-label">{{ field.label }}</label>
            <input
              v-model.number="approveForm[field.key]"
              type="number"
              :min="field.min"
              :max="field.max"
              step="1"
              :class="['input', tried && problems.includes(field.key) && 'input-error']"
              :data-testid="`field-${field.key}`"
            />
          </div>
        </div>
        <p v-if="tried && numberProblems.length" class="text-xs text-red-600 dark:text-red-400" data-testid="number-error">
          {{ t('admin.hostedApplications.numbersInvalid') }}
        </p>
        <p class="input-hint">{{ t('admin.hostedApplications.approveHint') }}</p>
      </form>
      <template #footer>
        <div class="flex justify-end gap-3">
          <template v-if="approveResult">
            <button type="button" class="btn btn-primary" @click="closeApprove">{{ t('common.close') }}</button>
          </template>
          <template v-else>
            <button type="button" class="btn btn-secondary" :disabled="busy" @click="closeApprove">{{ t('common.cancel') }}</button>
            <button type="submit" form="approve-form" class="btn btn-primary" :disabled="busy" data-testid="approve-confirm">
              {{ busy ? t('admin.hostedApplications.approving') : t('admin.hostedApplications.approve') }}
            </button>
          </template>
        </div>
      </template>
    </BaseDialog>

    <!-- Reject -->
    <BaseDialog :show="!!rejectTarget" :title="t('admin.hostedApplications.rejectTitle')" width="narrow" @close="closeReject">
      <form v-if="rejectTarget" id="reject-form" class="space-y-3 text-sm" data-testid="reject-dialog" @submit.prevent="confirmReject">
        <p class="text-gray-700 dark:text-gray-300">
          {{ t('admin.hostedApplications.rejectMessage', { name: rejectTarget.display_name, user: rejectTarget.user_email || `#${rejectTarget.user_id}` }) }}
        </p>
        <div>
          <label class="input-label">{{ t('admin.hostedApplications.rejectReason') }}</label>
          <textarea
            v-model="rejectReason"
            rows="3"
            :maxlength="REJECT_REASON_MAX"
            :class="['input', rejectTried && !rejectReason.trim() && 'input-error']"
            data-testid="reject-reason"
          />
          <p class="input-hint">{{ t('admin.hostedApplications.rejectReasonHint') }}</p>
        </div>
      </form>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" :disabled="busy" @click="closeReject">{{ t('common.cancel') }}</button>
          <button type="submit" form="reject-form" class="btn btn-danger" :disabled="busy" data-testid="reject-confirm">
            {{ busy ? t('admin.hostedApplications.rejecting') : t('admin.hostedApplications.reject') }}
          </button>
        </div>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useAdminSettingsStore } from '@/stores/adminSettings'
import { adminAPI } from '@/api/admin'
import {
  APPROVE_LIMITS,
  approvePayload,
  approveProblems,
  type AdminHostedApplication,
  type ApproveForm,
  type ApproveHostedApplicationResult,
  type HostedApplicationStatus
} from '@/api/admin/hostedApplications'
import type { Proxy } from '@/types'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { formatDateTime } from '@/utils/format'
import type { Column } from '@/components/common/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'

const REJECT_REASON_MAX = 500

const { t, te } = useI18n()
const appStore = useAppStore()
const adminSettings = useAdminSettingsStore()

const items = ref<AdminHostedApplication[]>([])
const loading = ref(false)
const statusFilter = ref<HostedApplicationStatus | ''>('pending')
const pagination = reactive({ page: 1, page_size: getPersistedPageSize(), total: 0 })
const busy = ref(false)
let loadSeq = 0

/** Fresh value from this page's settings read; until then the sidebar's cached one. */
const switchState = ref<boolean | null>(null)
/** Approving needs the switch on (the server refuses otherwise); rejecting does not. */
const switchOn = computed(() => switchState.value ?? adminSettings.hostedSelfOnboardEnabled)

const statusOptions = computed(() => [
  { value: 'pending', label: t('admin.hostedApplications.statuses.pending') },
  { value: 'approved', label: t('admin.hostedApplications.statuses.approved') },
  { value: 'rejected', label: t('admin.hostedApplications.statuses.rejected') },
  { value: 'cancelled', label: t('admin.hostedApplications.statuses.cancelled') },
  { value: 'expired', label: t('admin.hostedApplications.statuses.expired') },
  { value: '', label: t('admin.hostedApplications.statuses.all') }
])

const columns = computed<Column[]>(() => [
  { key: 'applicant', label: t('admin.hostedApplications.columns.applicant') },
  { key: 'display_name', label: t('admin.hostedApplications.columns.displayName') },
  { key: 'proxy', label: t('admin.hostedApplications.columns.proxy') },
  { key: 'created_at', label: t('admin.hostedApplications.columns.createdAt') },
  { key: 'status', label: t('admin.hostedApplications.columns.status') },
  { key: 'actions', label: t('admin.hostedApplications.columns.actions') }
])

function statusLabel(status: string): string {
  const key = `admin.hostedApplications.statuses.${status}`
  return te(key) ? t(key) : status
}

function statusBadge(status: string): string {
  if (status === 'pending') return 'badge-warning'
  if (status === 'approved') return 'badge-success'
  if (status === 'rejected') return 'badge-danger'
  return 'badge-gray'
}

function errorMessage(err: unknown, fallback: string): string {
  const e = (err ?? {}) as { reason?: unknown; code?: unknown; message?: unknown }
  for (const code of [e.reason, e.code]) {
    if (typeof code === 'string') {
      const key = `admin.hostedApplications.errors.${code}`
      if (te(key)) return t(key)
    }
  }
  return typeof e.message === 'string' && e.message ? `${fallback}: ${e.message}` : fallback
}

async function load() {
  const seq = ++loadSeq
  loading.value = true
  try {
    const res = await adminAPI.hostedApplications.list(pagination.page, pagination.page_size, { status: statusFilter.value })
    if (seq !== loadSeq) return
    const lastPage = Math.max(1, Math.ceil(res.total / pagination.page_size))
    if (res.items.length === 0 && pagination.page > lastPage) {
      pagination.page = lastPage
      return load()
    }
    items.value = res.items
    pagination.total = res.total
  } catch (err) {
    if (seq !== loadSeq) return
    appStore.showError(errorMessage(err, t('admin.hostedApplications.failedToLoad')))
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

function reload() {
  pagination.page = 1
  void load()
}

function handlePageChange(page: number) {
  pagination.page = page
  void load()
}

function handlePageSizeChange(size: number) {
  pagination.page_size = size
  pagination.page = 1
  void load()
}

/* ---------- Approve ---------- */
const proxies = ref<Proxy[]>([])
const defaults = reactive({
  concurrency: 3,
  priority: 1,
  carpool_device_limit: 5,
  carpool_admissions_per_cycle: 1,
  carpool_interval_hours: 24
})
/** Settings (defaults, switch) and proxies read by this page; approving waits for them. */
const contextReady = ref(false)
const contextFailed = ref(false)
const approveTarget = ref<AdminHostedApplication | null>(null)
const approveResult = ref<ApproveHostedApplicationResult | null>(null)
const approveForm = reactive<ApproveForm>({
  proxy_mode: 'existing',
  proxy_id: null,
  ...defaults
})
const tried = ref(false)

const proxyOptions = computed(() => proxies.value.map((p) => ({ value: p.id, label: `${p.name} · ${p.protocol}://${p.host}:${p.port}` })))

type NumberKey = keyof typeof APPROVE_LIMITS
const numberFields = computed<Array<{ key: NumberKey; label: string; min: number; max?: number }>>(() => [
  { key: 'concurrency', label: t('admin.hostedApplications.fields.concurrency'), min: 1 },
  { key: 'priority', label: t('admin.hostedApplications.fields.priority'), min: 1 },
  { key: 'carpool_device_limit', label: t('admin.hostedApplications.fields.deviceLimit'), min: 1, max: 32 },
  { key: 'carpool_admissions_per_cycle', label: t('admin.hostedApplications.fields.admissionsPerCycle'), min: 0, max: 32 },
  { key: 'carpool_interval_hours', label: t('admin.hostedApplications.fields.intervalHours'), min: 1, max: 720 }
])

const problems = computed(() => (approveTarget.value ? approveProblems(approveForm, hasSubmittedProxy(approveTarget.value)) : []))
const numberProblems = computed(() => problems.value.filter((p) => p !== 'proxy'))

function hasSubmittedProxy(a: AdminHostedApplication): boolean {
  return !!a.proxy_host && a.proxy_port > 0
}

function openApprove(row: AdminHostedApplication) {
  if (busy.value || !switchOn.value || !contextReady.value) return
  Object.assign(approveForm, {
    proxy_mode: hasSubmittedProxy(row) ? 'submitted' : 'existing',
    proxy_id: null,
    ...defaults
  })
  tried.value = false
  approveResult.value = null
  approveTarget.value = row
}

function closeApprove() {
  if (busy.value) return
  approveTarget.value = null
  approveResult.value = null
}

async function confirmApprove() {
  const target = approveTarget.value
  if (!target || busy.value) return
  tried.value = true
  if (problems.value.length) return
  if (!contextReady.value || !switchOn.value) {
    appStore.showError(t('admin.hostedApplications.errors.SELF_ONBOARD_DISABLED'))
    return
  }
  busy.value = true
  try {
    const result = await adminAPI.hostedApplications.approve(target.id, approvePayload(approveForm))
    approveResult.value = result
    approveTarget.value = null
    appStore.showSuccess(t('admin.hostedApplications.approveSuccess', { account: result.account_id }))
  } catch (err) {
    appStore.showError(errorMessage(err, t('admin.hostedApplications.approveFailed')))
  } finally {
    busy.value = false
  }
  await load()
}

/* ---------- Reject ---------- */
const rejectTarget = ref<AdminHostedApplication | null>(null)
const rejectReason = ref('')
const rejectTried = ref(false)

function openReject(row: AdminHostedApplication) {
  if (busy.value) return
  rejectReason.value = ''
  rejectTried.value = false
  rejectTarget.value = row
}

function closeReject() {
  if (busy.value) return
  rejectTarget.value = null
}

async function confirmReject() {
  const target = rejectTarget.value
  if (!target || busy.value) return
  rejectTried.value = true
  const reason = rejectReason.value.trim()
  if (!reason) return
  busy.value = true
  try {
    await adminAPI.hostedApplications.reject(target.id, reason)
    appStore.showSuccess(t('admin.hostedApplications.rejectSuccess'))
    busy.value = false
    rejectTarget.value = null
  } catch (err) {
    appStore.showError(errorMessage(err, t('admin.hostedApplications.rejectFailed')))
  } finally {
    busy.value = false
  }
  await load()
}

async function loadApproveContext() {
  try {
    proxies.value = (await adminAPI.proxies.getAll()) ?? []
  } catch {
    proxies.value = []
  }
  try {
    const s = await adminAPI.settings.getSettings()
    switchState.value = s.hosted_self_onboard_enabled === true
    const pick = (v: unknown, fallback: number) => (typeof v === 'number' && Number.isFinite(v) ? v : fallback)
    defaults.concurrency = pick(s.hosted_self_onboard_default_concurrency, defaults.concurrency)
    defaults.priority = pick(s.hosted_self_onboard_default_priority, defaults.priority)
    defaults.carpool_device_limit = pick(s.hosted_self_onboard_default_device_limit, defaults.carpool_device_limit)
    defaults.carpool_admissions_per_cycle = pick(s.hosted_self_onboard_default_admissions_per_cycle, defaults.carpool_admissions_per_cycle)
    defaults.carpool_interval_hours = pick(s.hosted_self_onboard_default_interval_hours, defaults.carpool_interval_hours)
    contextReady.value = true
    contextFailed.value = false
  } catch {
    // Without the site settings the defaults and the switch are unknown: approving waits for a retry.
    contextFailed.value = true
  }
}

onMounted(() => {
  void load()
  void loadApproveContext()
})
</script>
