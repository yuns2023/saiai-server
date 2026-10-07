<template>
  <div class="card" data-testid="chatgpt-billing-settings">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
        {{ t('admin.settings.chatgptBilling.title') }}
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.settings.chatgptBilling.description') }}
      </p>
    </div>
    <div class="space-y-4 p-6">
      <p v-if="loading" role="status">{{ t('common.loading') }}</p>
      <div v-else-if="loadFailed" class="space-y-3">
        <p role="alert" class="text-sm text-red-600">{{ t('admin.settings.chatgptBilling.loadFailed') }}</p>
        <button type="button" class="btn btn-secondary btn-sm" @click="loadSettings">
          {{ t('admin.settings.chatgptBilling.retry') }}
        </button>
      </div>
      <template v-else>
        <div class="max-w-sm">
          <label for="chatgpt-turn-price" class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.settings.chatgptBilling.price') }}
          </label>
          <input
            id="chatgpt-turn-price"
            v-model="price"
            type="number"
            min="0"
            step="any"
            class="input"
            :disabled="saving"
            aria-describedby="chatgpt-turn-price-hint"
          />
          <p id="chatgpt-turn-price-hint" class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.settings.chatgptBilling.priceHint') }}
          </p>
        </div>
        <button type="button" class="btn btn-primary btn-sm" :disabled="saving || !validPrice" @click="saveSettings">
          {{ saving ? t('admin.settings.saving') : t('admin.settings.chatgptBilling.save') }}
        </button>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getChatGPTBillingSettings, updateChatGPTBillingSettings } from '@/api/admin/settings'
import { useAppStore } from '@/stores'

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(true)
const loadFailed = ref(false)
const saving = ref(false)
const price = ref<string | number>('')
const validPrice = computed(() => String(price.value).trim() !== '' &&
  Number.isFinite(Number(price.value)) && Number(price.value) >= 0)

async function loadSettings() {
  loading.value = true
  loadFailed.value = false
  try {
    const settings = await getChatGPTBillingSettings()
    price.value = settings.success_turn_price_usd
  } catch {
    loadFailed.value = true
  } finally {
    loading.value = false
  }
}

async function saveSettings() {
  if (saving.value || !validPrice.value) return
  saving.value = true
  try {
    const settings = await updateChatGPTBillingSettings({ success_turn_price_usd: Number(price.value) })
    price.value = settings.success_turn_price_usd
    appStore.showSuccess(t('admin.settings.chatgptBilling.saved'))
  } catch {
    appStore.showError(t('admin.settings.chatgptBilling.saveFailed'))
  } finally {
    saving.value = false
  }
}

onMounted(loadSettings)
</script>
