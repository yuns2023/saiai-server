<template>
  <div class="space-y-3" :data-testid="`${testidPrefix}-claude-entrypoint`">
    <div>
      <label class="input-label">{{ t('admin.groups.claudeEntrypoint.policy') }}</label>
      <select
        :value="policy"
        class="input w-full"
        :data-testid="`${testidPrefix}-claude-entrypoint-policy`"
        @change="emit('update:policy', ($event.target as HTMLSelectElement).value as ClaudeEntrypointPolicy)"
      >
        <option value="off">{{ t('admin.groups.claudeEntrypoint.policyOff') }}</option>
        <option value="observe">{{ t('admin.groups.claudeEntrypoint.policyObserve') }}</option>
        <option value="enforce">{{ t('admin.groups.claudeEntrypoint.policyEnforce') }}</option>
      </select>
      <p class="input-hint mt-1">{{ policyHint }}</p>
    </div>

    <div v-if="policy !== 'off'">
      <div class="flex items-center justify-between">
        <label class="input-label">{{ t('admin.groups.claudeEntrypoint.allowlist') }}</label>
        <button
          type="button"
          class="text-xs text-primary-600 hover:underline dark:text-primary-400"
          :data-testid="`${testidPrefix}-claude-entrypoint-restore`"
          @click="emit('update:allowlist', [...DEFAULT_ALLOWLIST])"
        >
          {{ t('admin.groups.claudeEntrypoint.restoreDefault') }}
        </button>
      </div>
      <div class="input flex min-h-[2.5rem] flex-wrap items-center gap-1.5 py-1.5">
        <span
          v-for="item in allowlist"
          :key="item"
          class="inline-flex items-center gap-1 rounded bg-primary-50 px-2 py-0.5 text-xs text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
        >
          {{ item }}
          <button
            type="button"
            class="text-primary-400 hover:text-primary-700"
            :aria-label="t('admin.groups.claudeEntrypoint.removeItem')"
            @click="removeItem(item)"
          >
            ×
          </button>
        </span>
        <input
          v-model="draft"
          type="text"
          autocomplete="off"
          class="min-w-[10rem] flex-1 border-none bg-transparent p-0 text-sm focus:outline-none focus:ring-0"
          :placeholder="t('admin.groups.claudeEntrypoint.allowlistPlaceholder')"
          :data-testid="`${testidPrefix}-claude-entrypoint-input`"
          @keydown.enter.prevent="commitDraft"
          @keydown="onKeydown"
          @blur="commitDraft"
        />
      </div>
      <p v-if="draftError" class="mt-1 text-xs text-red-500">{{ draftError }}</p>
      <p class="input-hint mt-1">{{ t('admin.groups.claudeEntrypoint.allowlistHint') }}</p>
    </div>

    <div v-if="groupId && policy !== 'off'" class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
      <div class="mb-2 flex flex-wrap items-center justify-between gap-2">
        <h5 class="text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.groups.claudeEntrypoint.observations') }}
        </h5>
        <div class="flex items-center gap-2">
          <select v-model.number="days" class="input w-auto py-1 text-xs" @change="loadObservations">
            <option v-for="option in DAY_OPTIONS" :key="option" :value="option">
              {{ t('admin.groups.claudeEntrypoint.days', { days: option }) }}
            </option>
          </select>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="loadObservations">
            {{ t('admin.groups.claudeEntrypoint.refresh') }}
          </button>
        </div>
      </div>
      <p class="input-hint mb-2">{{ t('admin.groups.claudeEntrypoint.observationsHint') }}</p>
      <p v-if="truncated" class="mb-2 text-xs text-amber-600 dark:text-amber-400" :data-testid="`${testidPrefix}-claude-entrypoint-truncated`">
        {{ t('admin.groups.claudeEntrypoint.observationsTruncated') }}
      </p>
      <p v-if="collector && collector.dropped_last_flush > 0" class="mb-2 text-xs text-amber-600 dark:text-amber-400">
        {{ t('admin.groups.claudeEntrypoint.collectorDropped', { count: collector.dropped_last_flush }) }}
      </p>
      <p v-if="collector && collector.last_error_at" class="mb-2 text-xs text-amber-600 dark:text-amber-400">
        {{ t('admin.groups.claudeEntrypoint.collectorError', { time: formatDateTime(collector.last_error_at) }) }}
      </p>
      <p v-if="loadError" class="text-xs text-red-500">{{ loadError }}</p>
      <p v-else-if="!loading && observations.length === 0" class="text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.groups.claudeEntrypoint.observationsEmpty') }}
      </p>
      <div v-else class="max-h-72 overflow-auto">
        <table class="w-full text-left text-xs" :data-testid="`${testidPrefix}-claude-entrypoint-observations`">
          <thead class="sticky top-0 bg-white text-gray-500 dark:bg-dark-800 dark:text-gray-400">
            <tr>
              <th class="px-2 py-1">{{ t('admin.groups.claudeEntrypoint.columns.uaEntrypoint') }}</th>
              <th class="px-2 py-1">{{ t('admin.groups.claudeEntrypoint.columns.bodyEntrypoint') }}</th>
              <th class="px-2 py-1">{{ t('admin.groups.claudeEntrypoint.columns.version') }}</th>
              <th class="px-2 py-1">{{ t('admin.groups.claudeEntrypoint.columns.result') }}</th>
              <th class="px-2 py-1">{{ t('admin.groups.claudeEntrypoint.columns.reason') }}</th>
              <th class="px-2 py-1 text-right">{{ t('admin.groups.claudeEntrypoint.columns.requests') }}</th>
              <th class="px-2 py-1">{{ t('admin.groups.claudeEntrypoint.columns.lastSeen') }}</th>
              <th class="px-2 py-1">{{ t('admin.groups.claudeEntrypoint.columns.sampleUserAgent') }}</th>
              <th class="px-2 py-1"></th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 text-gray-700 dark:divide-dark-600 dark:text-gray-300">
            <tr v-for="(row, index) in observations" :key="index">
              <td class="px-2 py-1 font-mono">{{ row.ua_entrypoint }}</td>
              <td class="px-2 py-1 font-mono">
                {{ row.body_status === 'valid' ? row.body_entrypoint : `<${row.body_status}>` }}
              </td>
              <td class="px-2 py-1 font-mono">{{ row.cli_version || '-' }}</td>
              <td class="px-2 py-1" :class="resultClass(row.result)">{{ resultLabel(row.result) }}</td>
              <td class="px-2 py-1">{{ reasonLabel(row.reason) }}</td>
              <td class="px-2 py-1 text-right">{{ row.requests }}</td>
              <td class="whitespace-nowrap px-2 py-1">{{ formatDateTime(row.last_seen_at) }}</td>
              <td class="max-w-[16rem] truncate px-2 py-1 font-mono" :title="row.sample_user_agent">
                {{ row.sample_user_agent }}
              </td>
              <td class="px-2 py-1">
                <button
                  v-if="canAdd(row.ua_entrypoint)"
                  type="button"
                  class="whitespace-nowrap text-primary-600 hover:underline dark:text-primary-400"
                  :data-testid="`${testidPrefix}-claude-entrypoint-add-${index}`"
                  @click="addFromObservation(row.ua_entrypoint)"
                >
                  {{ t('admin.groups.claudeEntrypoint.addToAllowlist') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { formatDateTime } from '@/utils/format'
import type { ClaudeEntrypointObservation, ClaudeEntrypointObservationList, ClaudeEntrypointPolicy } from '@/types'

const DEFAULT_ALLOWLIST = ['cli', 'claude-vscode', 'sdk-cli']
const MAX_ITEMS = 32
const ITEM_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/
const DAY_OPTIONS = [1, 7, 30, 90]

const props = withDefaults(
  defineProps<{
    policy: ClaudeEntrypointPolicy
    allowlist: string[]
    /** Shows the observation list when set (edit dialog only). */
    groupId?: number | null
    testidPrefix?: string
  }>(),
  { groupId: null, testidPrefix: 'group' }
)

const emit = defineEmits<{
  'update:policy': [value: ClaudeEntrypointPolicy]
  'update:allowlist': [value: string[]]
}>()

const { t } = useI18n()
const appStore = useAppStore()

const draft = ref('')
const draftError = ref('')
const days = ref(7)
const loading = ref(false)
const loadError = ref('')
const observations = ref<ClaudeEntrypointObservation[]>([])
const truncated = ref(false)
const collector = ref<ClaudeEntrypointObservationList['collector'] | null>(null)

const policyHint = computed(() => {
  switch (props.policy) {
    case 'observe':
      return t('admin.groups.claudeEntrypoint.policyObserveHint')
    case 'enforce':
      return t('admin.groups.claudeEntrypoint.policyEnforceHint')
    default:
      return t('admin.groups.claudeEntrypoint.policyOffHint')
  }
})

function normalize(value: string): string {
  return value.trim().toLowerCase()
}

/** Adds items; returns false (and shows why) when one is invalid. */
function addItems(values: string[]): boolean {
  const next = [...props.allowlist]
  for (const raw of values) {
    const value = normalize(raw)
    if (!value) continue
    if (!ITEM_PATTERN.test(value)) {
      draftError.value = t('admin.groups.claudeEntrypoint.allowlistInvalid', { value: raw.trim() })
      return false
    }
    if (next.includes(value)) continue
    if (next.length >= MAX_ITEMS) {
      draftError.value = t('admin.groups.claudeEntrypoint.allowlistTooMany')
      return false
    }
    next.push(value)
  }
  draftError.value = ''
  if (next.length !== props.allowlist.length) emit('update:allowlist', next)
  return true
}

function commitDraft() {
  if (!draft.value.trim()) {
    draftError.value = ''
    return
  }
  if (addItems(draft.value.split(','))) draft.value = ''
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === ',') {
    event.preventDefault()
    commitDraft()
  }
}

function removeItem(item: string) {
  emit('update:allowlist', props.allowlist.filter((value) => value !== item))
}

function canAdd(entrypoint: string): boolean {
  return ITEM_PATTERN.test(entrypoint) && !props.allowlist.includes(entrypoint)
}

function addFromObservation(entrypoint: string) {
  if (addItems([entrypoint])) appStore.showSuccess(t('admin.groups.claudeEntrypoint.addedToAllowlist'))
}

function resultLabel(result: string): string {
  return t(`admin.groups.claudeEntrypoint.results.${result}`)
}

function reasonLabel(reason: string): string {
  return t(`admin.groups.claudeEntrypoint.reasons.${reason}`)
}

function resultClass(result: string): string {
  if (result === 'rejected') return 'text-red-600 dark:text-red-400'
  if (result === 'violation_observed') return 'text-amber-600 dark:text-amber-400'
  return 'text-green-600 dark:text-green-400'
}

// Only the latest request may update the list (days or group may change
// while an older request is still in flight).
let loadSeq = 0

async function loadObservations() {
  if (!props.groupId) return
  const seq = ++loadSeq
  loading.value = true
  loadError.value = ''
  try {
    const data = await adminAPI.groups.getClaudeEntrypointObservations(props.groupId, days.value)
    if (seq !== loadSeq) return
    observations.value = data.items || []
    truncated.value = Boolean(data.truncated)
    collector.value = data.collector || null
  } catch {
    if (seq !== loadSeq) return
    observations.value = []
    truncated.value = false
    collector.value = null
    loadError.value = t('admin.groups.claudeEntrypoint.observationsLoadFailed')
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

watch(
  () => [props.groupId, props.policy !== 'off'] as const,
  ([groupId, active]) => {
    loadSeq++
    loading.value = false
    observations.value = []
    truncated.value = false
    collector.value = null
    if (groupId && active) void loadObservations()
  },
  { immediate: true }
)
</script>
