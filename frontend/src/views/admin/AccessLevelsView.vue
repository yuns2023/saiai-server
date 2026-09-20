<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-6 p-4 sm:p-6">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ t('admin.accessLevels.title') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accessLevels.description') }}</p>
        </div>
        <button class="btn btn-primary" @click="startCreate">{{ t('admin.accessLevels.create') }}</button>
      </div>

      <section class="rounded-xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800">
        <div class="flex items-center justify-between gap-4">
          <div>
            <h2 class="font-medium text-gray-900 dark:text-white">{{ t('admin.accessLevels.maintenanceMode') }}</h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {{ maintenanceEnabled ? t('admin.accessLevels.maintenanceOnHint') : t('admin.accessLevels.maintenanceOffHint') }}
            </p>
          </div>
          <button
            type="button"
            role="switch"
            :aria-checked="maintenanceEnabled"
            :disabled="savingMode"
            class="relative h-7 w-12 rounded-full transition-colors"
            :class="maintenanceEnabled ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-600'"
            @click="toggleMode"
          >
            <span class="absolute top-1 h-5 w-5 rounded-full bg-white transition-transform" :class="maintenanceEnabled ? 'translate-x-6' : 'translate-x-1'" />
          </button>
        </div>
      </section>

      <section class="overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
        <div v-if="loading" class="p-8 text-center text-gray-500">{{ t('common.loading') }}</div>
        <div v-else-if="levels.length === 0" class="p-8 text-center text-gray-500">{{ t('admin.accessLevels.empty') }}</div>
        <div v-else class="overflow-x-auto">
          <table class="min-w-full divide-y divide-gray-200 dark:divide-dark-700">
            <thead class="bg-gray-50 dark:bg-dark-900/40">
              <tr class="text-left text-xs uppercase tracking-wide text-gray-500">
                <th class="px-5 py-3">{{ t('admin.accessLevels.name') }}</th>
                <th class="px-5 py-3">{{ t('admin.accessLevels.rank') }}</th>
                <th class="px-5 py-3">{{ t('admin.accessLevels.threshold') }}</th>
                <th class="px-5 py-3">{{ t('admin.accessLevels.discount') }}</th>
                <th class="px-5 py-3 text-right">{{ t('common.actions') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="level in levels" :key="level.id" class="text-sm text-gray-700 dark:text-gray-200">
                <td class="px-5 py-4 font-medium">{{ level.name }}</td>
                <td class="px-5 py-4">{{ level.rank }}</td>
                <td class="px-5 py-4">{{ formatNumber(level.balance_threshold) }}</td>
                <td class="px-5 py-4">{{ formatDiscount(level.payg_discount_multiplier) }}</td>
                <td class="space-x-2 px-5 py-4 text-right">
                  <button class="btn btn-secondary btn-sm" @click="startEdit(level)">{{ t('common.edit') }}</button>
                  <button class="btn btn-danger btn-sm" :disabled="levels.length <= 1" @click="removeLevel(level)">{{ t('common.delete') }}</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <BaseDialog :show="showEditor" :title="editingId ? t('admin.accessLevels.edit') : t('admin.accessLevels.create')" @close="showEditor = false">
        <form id="access-level-form" class="space-y-4" @submit.prevent="saveLevel">
          <div><label class="input-label">{{ t('admin.accessLevels.name') }}</label><input v-model="form.name" class="input" maxlength="100" required /></div>
          <div class="grid gap-4 sm:grid-cols-2">
            <div><label class="input-label">{{ t('admin.accessLevels.rank') }}</label><input v-model.number="form.rank" type="number" min="0" class="input" required /></div>
            <div><label class="input-label">{{ t('admin.accessLevels.threshold') }}</label><input v-model.number="form.balance_threshold" type="number" min="0" step="0.00000001" class="input" required /></div>
          </div>
          <div><label class="input-label">{{ t('admin.accessLevels.discount') }}</label><input v-model.number="form.payg_discount_multiplier" type="number" min="0" max="1" step="0.0001" class="input" required /><p class="input-hint">{{ t('admin.accessLevels.discountHint') }}</p></div>
        </form>
        <template #footer>
          <button class="btn btn-secondary" @click="showEditor = false">{{ t('common.cancel') }}</button>
          <button class="btn btn-primary" form="access-level-form" :disabled="saving">{{ saving ? t('common.saving') : t('common.save') }}</button>
        </template>
      </BaseDialog>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import type { AccessLevel } from '@/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'

const { t } = useI18n()
const appStore = useAppStore()
const levels = ref<AccessLevel[]>([])
const loading = ref(false)
const saving = ref(false)
const savingMode = ref(false)
const maintenanceEnabled = ref(false)
const showEditor = ref(false)
const editingId = ref<number | null>(null)
const form = reactive({ name: '', rank: 0, balance_threshold: 0, payg_discount_multiplier: 1 })

const load = async () => {
  loading.value = true
  try {
    const [items, settings] = await Promise.all([adminAPI.accessLevels.list(), adminAPI.accessLevels.getSettings()])
    levels.value = items
    maintenanceEnabled.value = settings.balance_maintenance_enabled
  } catch (e: any) {
    appStore.showError(e.response?.data?.detail || t('admin.accessLevels.loadFailed'))
  } finally { loading.value = false }
}

const startCreate = () => {
  const last = levels.value[levels.value.length - 1]
  Object.assign(form, { name: '', rank: last ? last.rank + 1 : 0, balance_threshold: last ? last.balance_threshold + 1 : 0, payg_discount_multiplier: last?.payg_discount_multiplier ?? 1 })
  editingId.value = null
  showEditor.value = true
}
const startEdit = (level: AccessLevel) => {
  Object.assign(form, level)
  editingId.value = level.id
  showEditor.value = true
}
const saveLevel = async () => {
  saving.value = true
  try {
    if (editingId.value) await adminAPI.accessLevels.update(editingId.value, form)
    else await adminAPI.accessLevels.create(form)
    showEditor.value = false
    appStore.showSuccess(t('admin.accessLevels.saved'))
    await load()
  } catch (e: any) { appStore.showError(e.response?.data?.detail || t('admin.accessLevels.saveFailed')) }
  finally { saving.value = false }
}
const removeLevel = async (level: AccessLevel) => {
  if (!window.confirm(t('admin.accessLevels.deleteConfirm', { name: level.name }))) return
  try { await adminAPI.accessLevels.remove(level.id); appStore.showSuccess(t('admin.accessLevels.deleted')); await load() }
  catch (e: any) { appStore.showError(e.response?.data?.detail || t('admin.accessLevels.deleteFailed')) }
}
const toggleMode = async () => {
  savingMode.value = true
  try { const result = await adminAPI.accessLevels.updateSettings(!maintenanceEnabled.value); maintenanceEnabled.value = result.balance_maintenance_enabled; appStore.showSuccess(t('admin.accessLevels.modeSaved')) }
  catch (e: any) { appStore.showError(e.response?.data?.detail || t('admin.accessLevels.saveFailed')) }
  finally { savingMode.value = false }
}
const formatNumber = (value: number) => new Intl.NumberFormat().format(value)
const formatDiscount = (value: number) => `${value.toFixed(4)}x (${Number((value * 100).toFixed(2))}%)`
onMounted(load)
</script>
