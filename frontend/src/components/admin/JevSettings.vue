<template>
  <section class="card" data-testid="jev-settings">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-semibold">{{ t('admin.settings.jev.title') }}</h2>
      <p class="mt-1 text-sm text-gray-500">{{ t('admin.settings.jev.description') }}</p>
    </div>
    <div class="space-y-4 p-6">
      <p v-if="loading" role="status">{{ t('common.loading') }}</p>
      <template v-else-if="loadFailed">
        <p role="alert">{{ t('admin.settings.jev.loadFailed') }}</p>
        <button class="btn btn-secondary" @click="load">{{ t('admin.settings.jev.retry') }}</button>
      </template>
      <form v-else @submit.prevent="save">
        <fieldset :disabled="saving || !settings?.can_save" class="space-y-4">
          <div>
            <label for="jev-endpoint" class="mb-2 block">{{ t('admin.settings.jev.endpoint') }}</label>
            <input id="jev-endpoint" v-model="endpoint" type="url" required class="input" />
          </div>
          <div>
            <label for="jev-key" class="mb-2 block">{{ t('admin.settings.jev.key') }}</label>
            <input id="jev-key" v-model="key" type="password" autocomplete="new-password" class="input" :disabled="clearKey" />
            <p class="mt-1 text-xs text-gray-500">{{ t(settings?.key_configured ? 'admin.settings.jev.configured' : 'admin.settings.jev.notConfigured') }}</p>
            <p class="mt-1 text-xs text-gray-500">{{ t('admin.settings.jev.keyHint') }}</p>
          </div>
          <label class="flex items-center gap-2"><input v-model="clearKey" type="checkbox" @change="key = ''" />{{ t('admin.settings.jev.clearKey') }}</label>
          <p class="text-xs text-gray-500">{{ t(settings?.source === 'admin' ? 'admin.settings.jev.adminSource' : 'admin.settings.jev.envSource') }}</p>
          <button class="btn btn-primary" type="submit">{{ t(saving ? 'admin.settings.saving' : 'admin.settings.jev.save') }}</button>
        </fieldset>
        <p v-if="!settings?.can_save" role="alert">{{ t('admin.settings.jev.encryptionRequired') }}</p>
      </form>
      <section v-if="settings?.runtime_metrics" data-testid="moderation-metrics" class="space-y-2 border-t pt-4 text-sm">
        <h3 class="font-medium">{{ t('admin.settings.jev.metricsTitle') }}</h3>
        <p class="text-xs text-gray-500">{{ t('admin.settings.jev.metricsHint') }} {{ settings.runtime_metrics.since }}</p>
        <p>{{ t('admin.settings.jev.metricsChecks') }}: {{ settings.runtime_metrics.completed_checks }} · {{ t('admin.settings.jev.metricsCacheHits') }}: {{ settings.runtime_metrics.cache_hits }} · {{ t('admin.settings.jev.metricsCalls') }}: {{ settings.runtime_metrics.detector_calls }}</p>
        <p>{{ t('admin.settings.jev.metricsDuration') }}: {{ settings.runtime_metrics.duration_ms }} ms · {{ t('admin.settings.jev.metricsQueueWait') }}: {{ settings.runtime_metrics.queue_wait_ms }} ms</p>
        <div v-for="(count, outcome) in settings.runtime_metrics.results" :key="outcome">
          {{ outcome }}: {{ count }}
        </div>
        <button type="button" class="btn btn-secondary" :disabled="metricsLoading" @click="refreshMetrics">{{ t('common.refresh') }}</button>
        <p v-if="metricsFailed" role="alert">{{ t('admin.settings.jev.metricsFailed') }}</p>
      </section>
    </div>
  </section>
</template>
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores'
import { getJevSettings, updateJevSettings, type JevSettings } from '@/api/admin/settings'
const { t } = useI18n()
const app = useAppStore()
const loading = ref(true)
const saving = ref(false)
const loadFailed = ref(false)
const settings = ref<JevSettings>()
const endpoint = ref('')
const key = ref('')
const clearKey = ref(false)
const metricsLoading = ref(false)
const metricsFailed = ref(false)
async function refreshMetrics() {
  if (metricsLoading.value) return
  metricsLoading.value = true
  metricsFailed.value = false
  try {
    const value = await getJevSettings()
    if (settings.value) settings.value.runtime_metrics = value.runtime_metrics
  } catch { metricsFailed.value = true } finally { metricsLoading.value = false }
}
function apply(value: JevSettings) {
  settings.value = value
  endpoint.value = value.endpoint
  key.value = ''
  clearKey.value = false
}
async function load() {
  loading.value = true
  loadFailed.value = false
  try { apply(await getJevSettings()) } catch { loadFailed.value = true } finally { loading.value = false }
}
async function save() {
  if (saving.value || !settings.value?.can_save) return
  if (endpoint.value.trim() !== settings.value.endpoint && settings.value.key_configured && !key.value.trim() && !clearKey.value) {
    app.showError(t('admin.settings.jev.endpointKeyRequired'))
    return
  }
  saving.value = true
  try {
    apply(await updateJevSettings({ endpoint: endpoint.value, api_key: key.value, clear_key: clearKey.value }))
    app.showSuccess(t('admin.settings.jev.saved'))
  } catch { app.showError(t('admin.settings.jev.saveFailed')) } finally { key.value = ''; saving.value = false }
}
onMounted(load)
</script>
