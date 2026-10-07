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
        <div v-for="tier in tiers" :key="tier" class="max-w-sm">
          <label :for="'chatgpt-price-' + tier" class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.settings.chatgptBilling.' + tier) }} · USD
          </label>
          <input
            :id="'chatgpt-price-' + tier"
            v-model="prices[tier]"
            :data-tier="tier"
            type="number"
            min="0"
            step="any"
            class="input"
            :disabled="saving"
            aria-describedby="chatgpt-price-hint"
          />
        </div>
        <p id="chatgpt-price-hint" class="text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.settings.chatgptBilling.priceHint') }}
        </p>
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
import type { ChatGPTBillingSettings, ChatGPTBillingTier } from '@/api/admin/settings'
import { useAppStore } from '@/stores'

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(true)
const loadFailed = ref(false)
const saving = ref(false)
const tiers = ['instant', 'medium', 'high', 'extreme', 'pro', 'default'] as const
const prices = ref<Record<typeof tiers[number], string | number>>({
  instant: '', medium: '', high: '', extreme: '', pro: '', default: ''
})
const validPrice = computed(() => tiers.every(tier => String(prices.value[tier]).trim() !== '' &&
  Number.isFinite(Number(prices.value[tier])) && Number(prices.value[tier]) >= 0))

function applySettings(settings: ChatGPTBillingSettings) {
  for (const tier of tiers) {
    prices.value[tier] = tier === 'default' ? settings.success_turn_price_usd :
      settings.tier_prices_usd?.[tier] ?? settings.success_turn_price_usd
  }
}

async function loadSettings() {
  loading.value = true
  loadFailed.value = false
  try {
    const settings = await getChatGPTBillingSettings()
    applySettings(settings)
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
    const tierPrices = Object.fromEntries(tiers.filter(tier => tier !== 'default')
      .map(tier => [tier, Number(prices.value[tier])])) as Record<ChatGPTBillingTier, number>
    const settings = await updateChatGPTBillingSettings({
      success_turn_price_usd: Number(prices.value.default), tier_prices_usd: tierPrices
    })
    applySettings(settings)
    appStore.showSuccess(t('admin.settings.chatgptBilling.saved'))
  } catch {
    appStore.showError(t('admin.settings.chatgptBilling.saveFailed'))
  } finally {
    saving.value = false
  }
}

onMounted(loadSettings)
</script>
