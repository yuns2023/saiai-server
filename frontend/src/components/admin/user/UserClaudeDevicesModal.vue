<template>
  <BaseDialog :show="show" :title="t('admin.users.claudeDeviceModal.title')" width="wide" @close="emit('close')">
    <div v-if="user" class="space-y-4">
      <div class="flex items-center justify-between gap-3 rounded-xl bg-gray-50 p-4 dark:bg-dark-700">
        <div>
          <p class="font-medium text-gray-900 dark:text-white">{{ user.email }}</p>
          <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('admin.users.claudeDeviceModal.userId', { id: user.id }) }}</p>
        </div>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="load">
          {{ loading ? t('admin.users.claudeDeviceModal.loading') : t('admin.users.claudeDeviceModal.refresh') }}
        </button>
      </div>

      <div class="grid grid-cols-1 gap-3 md:grid-cols-3">
        <div class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-700">
          <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.users.claudeDeviceModal.activeDevices') }}</div>
          <div class="mt-1 font-medium text-gray-900 dark:text-white">{{ activeDevices.length }}</div>
        </div>
        <div class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-700">
          <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.users.claudeDeviceModal.revokedDevices') }}</div>
          <div class="mt-1 font-medium text-gray-900 dark:text-white">{{ revokedDevices.length }}</div>
        </div>
        <div class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-700">
          <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.users.claudeDeviceModal.totalRegistrations') }}</div>
          <div class="mt-1 font-medium text-gray-900 dark:text-white">{{ devices.length }}</div>
        </div>
      </div>

      <div v-if="loading" class="flex justify-center py-8">
        <svg class="h-8 w-8 animate-spin text-primary-500" fill="none" viewBox="0 0 24 24">
          <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
          <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 3 7.938l-2.647-2.647z" />
        </svg>
      </div>
      <div v-else-if="devices.length === 0" class="py-8 text-center text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.users.claudeDeviceModal.empty') }}
      </div>
      <div v-else class="max-h-[28rem] space-y-3 overflow-y-auto">
        <div v-for="device in devices" :key="device.id" class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-600 dark:bg-dark-800">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0 space-y-1">
              <div class="flex flex-wrap items-center gap-2">
                <span class="font-medium text-gray-900 dark:text-white">{{ t('admin.users.claudeDeviceModal.device', { id: device.id }) }}</span>
                <span :class="['badge text-xs', device.revoked_at ? 'badge-danger' : 'badge-success']">
                  {{ device.revoked_at ? t('admin.users.claudeDeviceModal.revoked') : t('admin.users.claudeDeviceModal.active') }}
                </span>
              </div>
              <div class="flex items-center gap-2 text-sm">
                <span class="break-all font-mono text-gray-700 dark:text-gray-200">{{ device.device_id || t('admin.users.claudeDeviceModal.unavailable') }}</span>
                <button v-if="device.device_id" type="button" class="btn btn-secondary btn-sm shrink-0" @click="copyDeviceID(device.device_id)">{{ t('admin.users.claudeDeviceModal.copy') }}</button>
              </div>
              <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.users.claudeDeviceModal.groupId', { id: device.group_id }) }}</div>
              <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.users.claudeDeviceModal.firstSeen', { date: formatDateTime(device.first_seen_at) }) }}</div>
              <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.users.claudeDeviceModal.lastSeen', { date: formatDateTime(device.last_seen_at) }) }}</div>
              <div v-if="device.revoked_at" class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.users.claudeDeviceModal.revokedAt', { date: formatDateTime(device.revoked_at) }) }}</div>
            </div>
            <button v-if="!device.revoked_at" type="button" class="btn btn-danger btn-sm shrink-0" :disabled="revokingId === device.id" @click="revoke(device)">
              {{ revokingId === device.id ? t('admin.users.claudeDeviceModal.revoking') : t('admin.users.claudeDeviceModal.revoke') }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import { formatDateTime } from '@/utils/format'
import type { AdminUser } from '@/types'
import type { ClaudeUserDevice } from '@/api/admin/users'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean; user: AdminUser | null }>()
const emit = defineEmits<{ close: [] }>()
const appStore = useAppStore()
const { t } = useI18n()
const devices = ref<ClaudeUserDevice[]>([])
const loading = ref(false)
const revokingId = ref<number | null>(null)

const activeDevices = computed(() => devices.value.filter((device) => !device.revoked_at))
const revokedDevices = computed(() => devices.value.filter((device) => Boolean(device.revoked_at)))

watch(() => [props.show, props.user?.id], ([show]) => {
  if (show && props.user) load()
})

const load = async () => {
  if (!props.user) return
  loading.value = true
  try {
    devices.value = await adminAPI.users.getClaudeDevices(props.user.id)
  } catch (error: any) {
    appStore.showError(error?.response?.data?.detail || error?.message || t('admin.users.claudeDeviceModal.loadFailed'))
  } finally {
    loading.value = false
  }
}

const revoke = async (device: ClaudeUserDevice) => {
  if (!props.user || !window.confirm(t('admin.users.claudeDeviceModal.revokeConfirm', { id: device.id }))) return
  revokingId.value = device.id
  try {
    await adminAPI.users.revokeClaudeDevice(props.user.id, device.id)
    appStore.showSuccess(t('admin.users.claudeDeviceModal.revokeSuccess'))
    await load()
  } catch (error: any) {
    appStore.showError(error?.response?.data?.detail || error?.message || t('admin.users.claudeDeviceModal.revokeFailed'))
  } finally {
    revokingId.value = null
  }
}

const copyDeviceID = async (deviceID: string) => {
  try {
    await navigator.clipboard.writeText(deviceID)
    appStore.showSuccess(t('admin.users.claudeDeviceModal.copySuccess'))
  } catch {
    appStore.showError(t('admin.users.claudeDeviceModal.copyFailed'))
  }
}
</script>
