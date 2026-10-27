<template>
  <BaseDialog :show="show" :title="t('admin.users.clientProof.modal.title')" width="wide" @close="emit('close')">
    <div v-if="user" class="space-y-4">
      <div class="flex items-center justify-between gap-3 rounded-xl bg-gray-50 p-4 dark:bg-dark-700">
        <div>
          <p class="font-medium text-gray-900 dark:text-white">{{ user.email }}</p>
          <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('admin.users.clientProof.modal.userId', { id: user.id }) }}</p>
        </div>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="load">
          {{ loading ? t('admin.users.clientProof.modal.loading') : t('admin.users.clientProof.modal.refresh') }}
        </button>
      </div>

      <div v-if="loading && devices.length === 0" class="py-8 text-center text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.users.clientProof.modal.loading') }}
      </div>
      <div v-else-if="devices.length === 0" class="py-8 text-center text-sm text-gray-500 dark:text-gray-400" data-testid="client-devices-empty">
        {{ t('admin.users.clientProof.modal.empty') }}
      </div>
      <div v-else class="max-h-112 space-y-3 overflow-y-auto">
        <div v-for="device in devices" :key="device.id" class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-600 dark:bg-dark-800" data-testid="client-device-row">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0 space-y-1">
              <div class="flex flex-wrap items-center gap-2">
                <span class="break-all font-mono text-sm font-medium text-gray-900 dark:text-white">{{ device.device_id }}</span>
                <span :class="['badge text-xs', device.revoked_at ? 'badge-danger' : 'badge-success']">
                  {{ device.revoked_at ? t('admin.users.clientProof.modal.revoked') : t('admin.users.clientProof.modal.active') }}
                </span>
              </div>
              <div class="text-xs text-gray-500 dark:text-gray-400">
                {{ t('admin.users.clientProof.modal.client', { version: device.client_version || '—', os: device.os || '—', arch: device.arch || '—' }) }}
              </div>
              <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.users.clientProof.modal.created', { date: formatOptional(device.created_at) }) }}</div>
              <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.users.clientProof.modal.lastSeen', { date: formatOptional(device.last_seen_at) }) }}</div>
              <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.users.clientProof.modal.lastCodex', { date: formatOptional(device.last_codex_at) }) }}</div>
              <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.users.clientProof.modal.lastClaude', { date: formatOptional(device.last_claude_at) }) }}</div>
              <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.users.clientProof.modal.lastIp', { ip: device.last_ip || t('admin.users.clientProof.modal.never') }) }}</div>
              <div v-if="device.revoked_at" class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.users.clientProof.modal.revokedAt', { date: formatOptional(device.revoked_at) }) }}</div>
            </div>
            <button
              v-if="!device.revoked_at"
              type="button"
              class="btn btn-danger btn-sm shrink-0"
              data-testid="client-device-revoke"
              :disabled="revokingId === device.id"
              @click="revoke(device)"
            >
              {{ revokingId === device.id ? t('admin.users.clientProof.modal.revoking') : t('admin.users.clientProof.modal.revoke') }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import { formatDateTime } from '@/utils/format'
import type { AdminUser } from '@/types'
import type { ClientDevice } from '@/api/admin/users'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean; user: AdminUser | null }>()
const emit = defineEmits<{ close: []; changed: [] }>()
const appStore = useAppStore()
const { t } = useI18n()
const devices = ref<ClientDevice[]>([])
const loading = ref(false)
const revokingId = ref<number | null>(null)

const formatOptional = (value: string | null | undefined) => (value ? formatDateTime(value) : t('admin.users.clientProof.modal.never'))

watch(() => [props.show, props.user?.id], ([show]) => {
  if (show && props.user) load()
}, { immediate: true })

const load = async () => {
  if (!props.user) return
  loading.value = true
  try {
    devices.value = await adminAPI.users.getClientDevices(props.user.id)
  } catch (error: any) {
    appStore.showError(error?.response?.data?.detail || error?.message || t('admin.users.clientProof.modal.loadFailed'))
  } finally {
    loading.value = false
  }
}

const revoke = async (device: ClientDevice) => {
  if (!props.user || !window.confirm(t('admin.users.clientProof.modal.revokeConfirm', { id: device.device_id }))) return
  revokingId.value = device.id
  try {
    await adminAPI.users.revokeClientDevice(props.user.id, device.id)
    appStore.showSuccess(t('admin.users.clientProof.modal.revokeSuccess'))
    emit('changed')
    await load()
  } catch (error: any) {
    appStore.showError(error?.response?.data?.detail || error?.message || t('admin.users.clientProof.modal.revokeFailed'))
  } finally {
    revokingId.value = null
  }
}
</script>
