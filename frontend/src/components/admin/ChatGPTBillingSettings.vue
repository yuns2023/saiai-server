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
        <div class="space-y-3 border-t border-gray-100 pt-4 dark:border-dark-700">
          <label class="flex items-center gap-2 text-sm font-medium">
            <input v-model="imageBillingEnabled" data-testid="image-billing-enabled" type="checkbox" :disabled="saving" />
            {{ t('admin.settings.chatgptBilling.imageBillingEnabled') }}
          </label>
          <p class="text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.settings.chatgptBilling.imagePriceHint') }}
          </p>
          <div v-if="imageBillingEnabled" class="grid gap-4 sm:grid-cols-2">
            <div v-for="size in imageTiers" :key="size">
              <label :for="'chatgpt-image-price-' + size" class="mb-2 block text-sm font-medium">
                {{ size === 'unknown' ? t('admin.settings.chatgptBilling.unknownImageSize') : size }} · USD/{{ t('usage.imageUnit') }}
              </label>
              <input
                :id="'chatgpt-image-price-' + size"
                v-model="imagePrices[size]"
                :data-image-tier="size"
                type="number"
                min="0"
                step="any"
                class="input"
                :disabled="saving"
              />
            </div>
          </div>
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
import type { ChatGPTBillingSettings, ChatGPTBillingTier, ChatGPTImagePriceTier } from '@/api/admin/settings'
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
const imageTiers: ChatGPTImagePriceTier[] = ['1K', '2K', '4K', 'unknown']
const imageBillingEnabled = ref(false)
const imagePrices = ref<Record<ChatGPTImagePriceTier, string | number>>({ '1K': 0.02, '2K': 0.04, '4K': 0.08, unknown: 0.02 })
const validAmount = (value: string | number) => String(value).trim() !== '' && Number.isFinite(Number(value)) && Number(value) >= 0
const validPrice = computed(() => tiers.every(tier => validAmount(prices.value[tier])) &&
  (!imageBillingEnabled.value || imageTiers.every(size => validAmount(imagePrices.value[size]))))

function applySettings(settings: ChatGPTBillingSettings) {
  for (const tier of tiers) {
    prices.value[tier] = tier === 'default' ? settings.success_turn_price_usd :
      settings.tier_prices_usd?.[tier] ?? settings.success_turn_price_usd
  }
  imageBillingEnabled.value = settings.image_prices_usd != null
  if (settings.image_prices_usd) {
    for (const size of imageTiers) imagePrices.value[size] = settings.image_prices_usd[size]
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
      success_turn_price_usd: Number(prices.value.default), tier_prices_usd: tierPrices,
      image_prices_usd: imageBillingEnabled.value
        ? Object.fromEntries(imageTiers.map(size => [size, Number(imagePrices.value[size])])) as Record<ChatGPTImagePriceTier, number>
        : null
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
