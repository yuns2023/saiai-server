<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="space-y-3">
          <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('admin.claudeDevicePlatform.description') }}</p>
          <p class="rounded-md bg-amber-50 p-2 text-xs text-amber-700 dark:bg-amber-900/20 dark:text-amber-300" data-testid="cdp-notice">
            {{ t('admin.claudeDevicePlatform.notice') }}
          </p>
          <div class="flex flex-wrap items-center gap-4">
            <label
              v-for="reason in CLAUDE_DEVICE_FLAG_REASONS"
              :key="reason"
              class="flex items-center gap-1.5 text-sm text-gray-700 dark:text-gray-300"
            >
              <input
                type="checkbox"
                class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
                :data-testid="`cdp-reason-${reason}`"
                :checked="selected.includes(reason)"
                :disabled="loading || (selected.length === 1 && selected.includes(reason))"
                @change="toggleReason(reason)"
              />
              {{ reasonLabel(reason) }}
            </label>
            <button class="btn btn-secondary ml-auto" :disabled="loading" :title="t('common.refresh')" @click="reload">
              <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
            </button>
          </div>
        </div>
      </template>

      <template #table>
        <DataTable :columns="columns" :data="devices" :loading="loading && devices.length === 0">
          <template #cell-device="{ row }">
            <span class="font-mono text-sm text-gray-900 dark:text-white" :title="t('admin.claudeDevicePlatform.prefixHint')">{{ row.display_prefix }}…</span>
          </template>

          <template #cell-reasons="{ row }">
            <div class="flex flex-col gap-1">
              <div v-for="r in row.reasons" :key="r.reason" class="text-xs">
                <span :class="['badge', strongReasons.includes(r.reason) ? 'badge-warning' : 'badge-gray']">{{ reasonLabel(r.reason) }}</span>
                <span v-if="r.evidence" class="ml-1 font-mono text-gray-500 dark:text-dark-400">{{ r.evidence }}</span>
                <div class="text-gray-400">{{ formatDateTime(r.last_matched_at) }}</div>
              </div>
            </div>
          </template>

          <template #cell-platforms="{ row }">
            <div class="flex flex-col gap-1 text-xs">
              <div v-for="p in row.platforms" :key="`${p.family}|${p.arch}`">
                <span class="font-medium text-gray-900 dark:text-white">{{ familyLabel(p.family) }} · {{ archLabel(p.arch) }}</span>
                <div class="text-gray-400">{{ formatDateTime(p.first_seen_at) }} – {{ formatDateTime(p.last_seen_at) }}</div>
              </div>
              <div v-for="v in row.versions" :key="`${v.family}|${v.major}`" class="text-gray-600 dark:text-gray-300">
                {{ familyLabel(v.family) }} {{ v.major }}
                <div class="text-gray-400">{{ formatDateTime(v.first_seen_at) }} – {{ formatDateTime(v.last_seen_at) }}</div>
              </div>
              <span v-if="!row.platforms.length && !row.versions.length" class="text-gray-400">-</span>
            </div>
          </template>

          <template #cell-users="{ row }">
            <div class="flex flex-col gap-1 text-xs">
              <div v-for="u in row.users" :key="u.user_id">
                <span class="font-medium text-gray-900 dark:text-white">{{ u.username || u.email || `#${u.user_id}` }}</span>
                <span class="ml-1 text-gray-400">#{{ u.user_id }}</span>
                <div class="text-gray-400">{{ t('admin.claudeDevicePlatform.keyAndTime', { key: u.api_key_id, time: formatDateTime(u.last_seen_at) }) }}</div>
              </div>
              <div v-if="row.users_truncated" class="text-amber-600 dark:text-amber-400" data-testid="cdp-users-truncated">
                {{ t('admin.claudeDevicePlatform.usersTruncated') }}
              </div>
            </div>
          </template>

          <template #cell-last_matched_at="{ row }">
            <span class="text-sm text-gray-500 dark:text-dark-400">{{ formatDateTime(row.last_matched_at) }}</span>
          </template>

          <template #empty>
            <div class="py-10 text-center text-sm text-gray-500">{{ t('admin.claudeDevicePlatform.empty') }}</div>
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <div class="flex items-center justify-center py-3">
          <button
            v-if="nextCursor"
            class="btn btn-secondary btn-sm"
            :disabled="loading"
            data-testid="cdp-load-more"
            @click="loadMore"
          >
            {{ loading ? t('common.loading') : t('admin.claudeDevicePlatform.loadMore') }}
          </button>
        </div>
      </template>
    </TablePageLayout>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import {
  CLAUDE_DEVICE_DEFAULT_REASONS,
  CLAUDE_DEVICE_FLAG_REASONS,
  type ClaudeDeviceFlagReason,
  type ClaudeFlaggedDevice
} from '@/api/admin/claudeDevicePlatform'
import { formatDateTime } from '@/utils/format'
import type { Column } from '@/components/common/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Icon from '@/components/icons/Icon.vue'

const { t, te } = useI18n()
const appStore = useAppStore()

const strongReasons: string[] = CLAUDE_DEVICE_DEFAULT_REASONS
const selected = ref<ClaudeDeviceFlagReason[]>([...CLAUDE_DEVICE_DEFAULT_REASONS])
const devices = ref<ClaudeFlaggedDevice[]>([])
const nextCursor = ref('')
const loading = ref(false)
let loadSeq = 0

const columns = computed<Column[]>(() => [
  { key: 'device', label: t('admin.claudeDevicePlatform.columns.device') },
  { key: 'reasons', label: t('admin.claudeDevicePlatform.columns.reasons') },
  { key: 'platforms', label: t('admin.claudeDevicePlatform.columns.platforms') },
  { key: 'users', label: t('admin.claudeDevicePlatform.columns.users') },
  { key: 'last_matched_at', label: t('admin.claudeDevicePlatform.columns.lastMatchedAt') }
])

function reasonLabel(reason: string): string {
  const key = `admin.claudeDevicePlatform.reasons.${reason}`
  return te(key) ? t(key) : reason
}

function familyLabel(family: string): string {
  const key = `admin.claudeDevicePlatform.families.${family}`
  return te(key) ? t(key) : family
}

function archLabel(arch: string): string {
  return arch === 'unknown' ? t('admin.claudeDevicePlatform.families.unknown') : arch
}

function toggleReason(reason: ClaudeDeviceFlagReason) {
  if (selected.value.includes(reason)) {
    if (selected.value.length === 1) return
    selected.value = selected.value.filter((r) => r !== reason)
  } else {
    selected.value = CLAUDE_DEVICE_FLAG_REASONS.filter((r) => r === reason || selected.value.includes(r))
  }
  reload()
}

async function load(append: boolean) {
  const seq = ++loadSeq
  loading.value = true
  try {
    const res = await adminAPI.claudeDevicePlatform.listFlagged({
      reasons: selected.value,
      cursor: append ? nextCursor.value : undefined,
      limit: 50
    })
    if (seq !== loadSeq) return
    devices.value = append ? [...devices.value, ...res.items] : res.items
    nextCursor.value = res.next_cursor
  } catch (err) {
    if (seq !== loadSeq) return
    const message = (err as { message?: unknown })?.message
    appStore.showError(typeof message === 'string' && message ? `${t('admin.claudeDevicePlatform.failedToLoad')}: ${message}` : t('admin.claudeDevicePlatform.failedToLoad'))
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

function reload() {
  nextCursor.value = ''
  devices.value = []
  void load(false)
}

function loadMore() {
  if (!nextCursor.value || loading.value) return
  void load(true)
}

onMounted(reload)
</script>
